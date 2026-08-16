//go:build linux

package timing

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// BootID identifies the Linux boot-wide CLOCK_BOOTTIME domain.
func BootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func monotonicNS() int64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		// Substituting a different clock would corrupt comparisons within a
		// source/boot domain, so a supported runtime must fail loudly.
		panic(fmt.Sprintf("timing: CLOCK_BOOTTIME unavailable: %v", err))
	}
	return ts.Nano()
}
