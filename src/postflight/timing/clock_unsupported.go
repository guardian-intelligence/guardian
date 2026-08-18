//go:build !linux

package timing

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"runtime"
	"sync"
	"time"
)

var processStarted = time.Now()

var processClockIdentity struct {
	sync.Once
	id  string
	err error
}

// BootID identifies the process-local test clock used by non-Linux builds.
func BootID() (string, error) {
	processClockIdentity.Do(func() {
		var randomID [16]byte
		if _, err := rand.Read(randomID[:]); err != nil {
			processClockIdentity.err = fmt.Errorf("timing: creating %s process clock identity: %w", runtime.GOOS, err)
			return
		}
		processClockIdentity.id = runtime.GOOS + "-process-" + hex.EncodeToString(randomID[:])
	})
	return processClockIdentity.id, processClockIdentity.err
}

// monotonicNS provides ordering for native portable tests. Postflight's
// production timing contract remains Linux CLOCK_BOOTTIME; binaries built for
// other operating systems are not runtime artifacts.
func monotonicNS() int64 {
	return time.Since(processStarted).Nanoseconds() + 1
}
