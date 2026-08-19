package gametest

import (
	"context"
	"errors"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// The ABI surface, by generation. v1 is the WUM-era terrain-named
// surface; v2 is the chunkies-abi trait surface — content-named, rate
// exports mandatory, and the SimEvent envelope (kind u16 | actor u64 |
// payload) on sim_apply. Both are served until the v2 flag day retires
// v1 support here.
var requiredExportsV1 = []string{
	"abi_version",
	"io_buf", "io_cap", "terrain_buf", "terrain_cap",
	"sim_set_terrain", "sim_init", "sim_restore", "sim_snapshot",
	"sim_step", "sim_apply", "sim_hash", "sim_tick", "sim_epoch",
	"sim_terrain_id",
}

var rateExportsV1 = []string{"sim_rate", "sim_anchor_tick", "sim_anchor_ns"}

var requiredExportsV2 = []string{
	"abi_version",
	"io_buf", "io_cap", "content_buf", "content_cap",
	"sim_content_stage", "sim_content_id", "sim_init", "sim_restore",
	"sim_snapshot", "sim_step", "sim_apply", "sim_hash", "sim_tick",
	"sim_epoch", "sim_rate", "sim_anchor_tick", "sim_anchor_ns",
}

// requiredExports returns the surface a module of the given generation
// owes. The system events (rate_set included) are framework contract, so
// the rate surface is mandatory for every generation.
func requiredExports(abi uint32) []string {
	if abi >= 2 {
		return requiredExportsV2
	}
	return append(append([]string{}, requiredExportsV1...), rateExportsV1...)
}

// host drives one instance of a game module through the sim ABI. Every
// call returns an error instead of panicking: in this suite a trap is a
// finding, never a crash.
type host struct {
	rt         wazero.Runtime
	mem        api.Memory
	fns        map[string]api.Function
	abi        uint32
	ioPtr      uint32
	ioCap      uint32
	contentPtr uint32
	contentCap uint32
}

func newHost(module []byte) (*host, error) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	mod, err := rt.Instantiate(ctx, module)
	if err != nil {
		rt.Close(ctx)
		return nil, fmt.Errorf("instantiate: %w", err)
	}
	h := &host{rt: rt, mem: mod.Memory(), fns: map[string]api.Function{}}
	if h.mem == nil {
		rt.Close(ctx)
		return nil, errors.New("module exports no memory")
	}
	probe := append(append([]string{}, requiredExportsV1...), rateExportsV1...)
	probe = append(probe, requiredExportsV2...)
	for _, name := range probe {
		if f := mod.ExportedFunction(name); f != nil {
			h.fns[name] = f
		}
	}
	v, err := h.call32("abi_version")
	if err != nil {
		rt.Close(ctx)
		return nil, err
	}
	h.abi = v
	contentBuf, contentCap := "terrain_buf", "terrain_cap"
	if h.abi >= 2 {
		contentBuf, contentCap = "content_buf", "content_cap"
	}
	for _, buf := range []struct {
		ptr, cap *uint32
		pn, cn   string
	}{
		{&h.ioPtr, &h.ioCap, "io_buf", "io_cap"},
		{&h.contentPtr, &h.contentCap, contentBuf, contentCap},
	} {
		if _, ok := h.fns[buf.pn]; !ok {
			continue
		}
		p, err := h.call(buf.pn)
		if err != nil {
			rt.Close(ctx)
			return nil, err
		}
		c, err := h.call(buf.cn)
		if err != nil {
			rt.Close(ctx)
			return nil, err
		}
		*buf.ptr, *buf.cap = uint32(p), uint32(c)
	}
	return h, nil
}

func (h *host) close() { h.rt.Close(context.Background()) }

func (h *host) call(name string, args ...uint64) (uint64, error) {
	f, ok := h.fns[name]
	if !ok {
		return 0, fmt.Errorf("module does not export %s", name)
	}
	res, err := f.Call(context.Background(), args...)
	if err != nil {
		return 0, fmt.Errorf("%s trapped: %w", name, err)
	}
	if len(res) == 0 {
		return 0, nil
	}
	return res[0], nil
}

// call32 masks an i32-returning export: wazero leaves unspecified high
// bits in the raw result slot.
func (h *host) call32(name string, args ...uint64) (uint32, error) {
	v, err := h.call(name, args...)
	return uint32(v), err
}

// setContent stages and adopts a content blob (v1: terrain).
func (h *host) setContent(blob []byte) (uint32, error) {
	if uint32(len(blob)) > h.contentCap || !h.mem.Write(h.contentPtr, blob) {
		return 0, fmt.Errorf("content blob (%d bytes) does not fit content buffer (%d)", len(blob), h.contentCap)
	}
	verb := "sim_set_terrain"
	if h.abi >= 2 {
		verb = "sim_content_stage"
	}
	return h.call32(verb, uint64(len(blob)))
}

func (h *host) init(seed uint64, id int64, epoch uint32) (uint32, error) {
	return h.call32("sim_init", seed, uint64(id), uint64(epoch))
}

func (h *host) restore(state []byte) (uint32, error) {
	if uint32(len(state)) > h.ioCap || !h.mem.Write(h.ioPtr, state) {
		return 0, fmt.Errorf("snapshot (%d bytes) does not fit io buffer (%d)", len(state), h.ioCap)
	}
	return h.call32("sim_restore", uint64(len(state)))
}

func (h *host) snapshot() ([]byte, error) {
	n, err := h.call32("sim_snapshot")
	if err != nil {
		return nil, err
	}
	b, ok := h.mem.Read(h.ioPtr, n)
	if !ok {
		return nil, fmt.Errorf("snapshot length %d reads out of bounds", n)
	}
	out := make([]byte, n)
	copy(out, b)
	return out, nil
}

// apply runs one event through sim_apply, encoding by ABI generation:
// v1 is kind u16 LE + payload (any actor rides in the payload); v2 is
// the SimEvent, kind u16 | actor u64 | payload.
func (h *host) apply(ev Event) (uint32, error) {
	var event []byte
	if h.abi >= 2 {
		event = make([]byte, 10+len(ev.Payload))
		event[0], event[1] = byte(ev.Kind), byte(ev.Kind>>8)
		for i := 0; i < 8; i++ {
			event[2+i] = byte(ev.Actor >> (8 * i))
		}
		copy(event[10:], ev.Payload)
	} else {
		event = make([]byte, 2+len(ev.Payload))
		event[0], event[1] = byte(ev.Kind), byte(ev.Kind>>8)
		copy(event[2:], ev.Payload)
	}
	if uint32(len(event)) > h.ioCap || !h.mem.Write(h.ioPtr, event) {
		return 0, fmt.Errorf("event (%d bytes) does not fit io buffer (%d)", len(event), h.ioCap)
	}
	return h.call32("sim_apply", uint64(len(event)))
}

func (h *host) step() error {
	_, err := h.call("sim_step")
	return err
}

func (h *host) hash() (uint64, error) { return h.call("sim_hash") }
func (h *host) tick() (uint64, error) { return h.call("sim_tick") }

// contentID reads the module's adopted content address.
func (h *host) contentID() (uint64, error) {
	if h.abi >= 2 {
		return h.call("sim_content_id")
	}
	return h.call("sim_terrain_id")
}

func (h *host) epoch() (uint32, error) { return h.call32("sim_epoch") }
func (h *host) rate() (uint32, error)  { return h.call32("sim_rate") }

func (h *host) anchorTick() (uint64, error) { return h.call("sim_anchor_tick") }
