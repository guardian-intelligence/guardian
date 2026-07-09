package main

import (
	"bytes"
	"fmt"
	"strconv"
)

// cpuTotals is the aggregate "cpu" line of /proc/stat reduced to what the
// busy-share computation needs: jiffies not doing work (idle+iowait) and
// jiffies overall. Values are host-wide — procfs is not cgroup-aware, which
// is exactly why the sampler reads it: the widget shows the machine, not the
// pod.
type cpuTotals struct {
	idle  uint64
	total uint64
}

// parseProcStat reads the aggregate cpu line. Fields are user nice system
// idle iowait irq softirq steal [guest guest_nice]; guest time is already
// accounted inside user/nice, so only the first eight fields sum into total.
func parseProcStat(data []byte) (cpuTotals, error) {
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("cpu ")) {
			continue
		}
		fields := bytes.Fields(line)[1:]
		if len(fields) < 8 {
			return cpuTotals{}, fmt.Errorf("cpu line has %d fields, want >= 8", len(fields))
		}
		var t cpuTotals
		for i, f := range fields[:8] {
			v, err := strconv.ParseUint(string(f), 10, 64)
			if err != nil {
				return cpuTotals{}, fmt.Errorf("cpu field %d: %w", i, err)
			}
			t.total += v
			if i == 3 || i == 4 { // idle, iowait
				t.idle += v
			}
		}
		return t, nil
	}
	return cpuTotals{}, fmt.Errorf("no aggregate cpu line")
}

// cpuBusyBp derives the busy share between two readings in basis points.
// ok is false when the pair carries no signal — first sample, a counter
// reset/wrap (either delta negative), or no elapsed jiffies — and the caller
// keeps its previous value instead of dividing by zero or going negative.
func cpuBusyBp(prev, cur cpuTotals) (uint32, bool) {
	if cur.total < prev.total || cur.idle < prev.idle {
		return 0, false
	}
	dTotal := cur.total - prev.total
	dIdle := cur.idle - prev.idle
	if dTotal == 0 || dIdle > dTotal {
		return 0, false
	}
	return uint32((dTotal - dIdle) * 10000 / dTotal), true
}

// parseMeminfo reduces /proc/meminfo to used share in basis points:
// (MemTotal - MemAvailable) / MemTotal. MemAvailable is the kernel's own
// estimate of reclaimable headroom — the honest "used" for a dashboard,
// unlike MemFree which counts page cache as consumed.
func parseMeminfo(data []byte) (uint32, error) {
	var total, avail uint64
	var haveTotal, haveAvail bool
	for _, line := range bytes.Split(data, []byte("\n")) {
		var dst *uint64
		switch {
		case bytes.HasPrefix(line, []byte("MemTotal:")):
			dst, haveTotal = &total, true
		case bytes.HasPrefix(line, []byte("MemAvailable:")):
			dst, haveAvail = &avail, true
		default:
			continue
		}
		fields := bytes.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("malformed meminfo line %q", line)
		}
		v, err := strconv.ParseUint(string(fields[1]), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("meminfo value in %q: %w", line, err)
		}
		*dst = v
	}
	if !haveTotal || !haveAvail {
		return 0, fmt.Errorf("meminfo missing MemTotal or MemAvailable")
	}
	if total == 0 {
		return 0, fmt.Errorf("MemTotal is zero")
	}
	if avail > total {
		avail = total
	}
	return uint32((total - avail) * 10000 / total), nil
}
