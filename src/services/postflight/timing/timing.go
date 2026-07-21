// Package timing records process-local, high-resolution event timestamps.
package timing

import (
	"fmt"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// Point is one observation on an originating process's clock.
type Point struct {
	Event       string `json:"event"`
	Source      string `json:"source"`
	BootID      string `json:"boot_id"`
	Sequence    uint64 `json:"sequence"`
	MonotonicNS int64  `json:"monotonic_ns"`
	UnixNS      int64  `json:"unix_ns"`
}

// Recorder creates ordered points for one process life. BootID identifies
// the CLOCK_BOOTTIME domain and must change when that clock resets; Sequence
// may restart with the process. Monotonic values are never compared across
// recorders with different source/boot tuples.
type Recorder struct {
	source string
	bootID string
	seq    atomic.Uint64
}

func New(source, bootID string) (*Recorder, error) {
	if source == "" || bootID == "" {
		return nil, fmt.Errorf("timing: source and boot id are required")
	}
	return &Recorder{source: source, bootID: bootID}, nil
}

func (r *Recorder) Point(event string) Point {
	if event == "" {
		panic("timing: empty event")
	}
	return Point{
		Event: event, Source: r.source, BootID: r.bootID,
		Sequence: r.seq.Add(1), MonotonicNS: boottimeNS(), UnixNS: time.Now().UnixNano(),
	}
}

func boottimeNS() int64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		// CLOCK_BOOTTIME exists on every supported Linux guest/host. Keep a
		// positive sample if a test kernel or seccomp profile denies it.
		return time.Since(processStarted).Nanoseconds() + 1
	}
	return ts.Nano()
}

var processStarted = time.Now()
