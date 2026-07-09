package main

import (
	"context"
	"os"
	"sync"
	"time"

	"log/slog"

	"connectrpc.com/connect"

	cockpitv1 "github.com/guardian-intelligence/guardian/src/proto/gen/go/guardian/cockpit/v1"
)

// tickFunc produces the next sample; implementations are the procfs reader
// and the synthetic generator.
type tickFunc func() (cpuBp, memBp uint32, ok bool)

// samplerService serves the node's live tick stream to any number of hubs.
// It holds no history — a stream starts at the next tick — so the process
// is stateless and restart-cheap.
type samplerService struct {
	node string

	mu   sync.Mutex
	subs map[chan *cockpitv1.StreamTicksResponse]struct{}
}

func newSamplerService(node string) *samplerService {
	return &samplerService{node: node, subs: map[chan *cockpitv1.StreamTicksResponse]struct{}{}}
}

// run samples at the tick interval and broadcasts. A hub that stops reading
// loses ticks (non-blocking send into a small buffer) rather than stalling
// sampling; the hub's keyframe cadence absorbs the gap.
func (s *samplerService) run(ctx context.Context, next tickFunc) {
	ticker := time.NewTicker(tickIntervalMs * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			cpu, mem, ok := next()
			if !ok {
				continue
			}
			msg := &cockpitv1.StreamTicksResponse{
				Node:      s.node,
				TsMs:      uint64(now.UnixMilli()),
				CpuBp:     cpu,
				MemUsedBp: mem,
			}
			s.mu.Lock()
			for ch := range s.subs {
				select {
				case ch <- msg:
				default:
				}
			}
			s.mu.Unlock()
		}
	}
}

func (s *samplerService) StreamTicks(
	ctx context.Context,
	_ *connect.Request[cockpitv1.StreamTicksRequest],
	stream *connect.ServerStream[cockpitv1.StreamTicksResponse],
) error {
	ch := make(chan *cockpitv1.StreamTicksResponse, 2*ticksPerFrame)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-ch:
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

// procfsTicker returns a tickFunc over the host's /proc. The first call
// only primes the jiffies baseline (a CPU share needs two readings), so it
// reports ok=false rather than a made-up value.
func procfsTicker(statPath, meminfoPath string) tickFunc {
	var prev cpuTotals
	var primed bool
	return func() (uint32, uint32, bool) {
		statRaw, err := os.ReadFile(statPath)
		if err != nil {
			slog.Warn("read stat", "path", statPath, "err", err)
			return 0, 0, false
		}
		memRaw, err := os.ReadFile(meminfoPath)
		if err != nil {
			slog.Warn("read meminfo", "path", meminfoPath, "err", err)
			return 0, 0, false
		}
		cur, err := parseProcStat(statRaw)
		if err != nil {
			slog.Warn("parse stat", "err", err)
			return 0, 0, false
		}
		mem, err := parseMeminfo(memRaw)
		if err != nil {
			slog.Warn("parse meminfo", "err", err)
			return 0, 0, false
		}
		wasPrimed := primed
		cpu, ok := cpuBusyBp(prev, cur)
		prev, primed = cur, true
		if !wasPrimed || !ok {
			return 0, 0, false
		}
		return cpu, mem, true
	}
}
