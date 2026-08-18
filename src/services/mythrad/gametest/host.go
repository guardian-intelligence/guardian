package gametest

import (
	"context"
	"errors"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// requiredExports is the park-module ABI surface the authority host and
// every replica host rely on. sim_rate/sim_anchor_* are demanded only when
// the game declares a rate_set kind (a pre-rate module runs the genesis
// segment).
var requiredExports = []string{
	"abi_version",
	"io_buf", "io_cap", "terrain_buf", "terrain_cap",
	"sim_set_terrain", "sim_init", "sim_restore", "sim_snapshot",
	"sim_step", "sim_apply", "sim_hash", "sim_tick", "sim_epoch",
	"sim_terrain_id",
}

var rateExports = []string{"sim_rate", "sim_anchor_tick", "sim_anchor_ns"}

// host drives one instance of a game module through the sim ABI. Every
// call returns an error instead of panicking: in this suite a trap is a
// finding, never a crash.
type host struct {
	rt         wazero.Runtime
	mem        api.Memory
	fns        map[string]api.Function
	ioPtr      uint32
	ioCap      uint32
	terrainPtr uint32
	terrainCap uint32
}

func newHost(module []byte, names []string) (*host, error) {
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
	for _, name := range names {
		if f := mod.ExportedFunction(name); f != nil {
			h.fns[name] = f
		}
	}
	for _, buf := range []struct {
		ptr, cap *uint32
		pn, cn   string
	}{
		{&h.ioPtr, &h.ioCap, "io_buf", "io_cap"},
		{&h.terrainPtr, &h.terrainCap, "terrain_buf", "terrain_cap"},
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

func (h *host) setTerrain(blob []byte) (uint32, error) {
	if uint32(len(blob)) > h.terrainCap || !h.mem.Write(h.terrainPtr, blob) {
		return 0, fmt.Errorf("terrain blob (%d bytes) does not fit terrain buffer (%d)", len(blob), h.terrainCap)
	}
	return h.call32("sim_set_terrain", uint64(len(blob)))
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

// apply runs one event (kind u16 LE + payload) through sim_apply.
func (h *host) apply(kind uint16, payload []byte) (uint32, error) {
	event := make([]byte, 2+len(payload))
	event[0], event[1] = byte(kind), byte(kind>>8)
	copy(event[2:], payload)
	if uint32(len(event)) > h.ioCap || !h.mem.Write(h.ioPtr, event) {
		return 0, fmt.Errorf("event (%d bytes) does not fit io buffer (%d)", len(event), h.ioCap)
	}
	return h.call32("sim_apply", uint64(len(event)))
}

func (h *host) step() error {
	_, err := h.call("sim_step")
	return err
}

func (h *host) hash() (uint64, error)    { return h.call("sim_hash") }
func (h *host) tick() (uint64, error)    { return h.call("sim_tick") }
func (h *host) terrain() (uint64, error) { return h.call("sim_terrain_id") }

func (h *host) epoch() (uint32, error) { return h.call32("sim_epoch") }
func (h *host) rate() (uint32, error)  { return h.call32("sim_rate") }

func (h *host) anchorTick() (uint64, error) { return h.call("sim_anchor_tick") }
