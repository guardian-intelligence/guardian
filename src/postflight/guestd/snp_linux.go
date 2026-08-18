//go:build linux

package guestd

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// SNP_GET_DERIVED_KEY, include/uapi/linux/sev-guest.h. The PSP mixes the
// selected guest fields into a key only same-configuration guests on this
// chip can re-derive.
const (
	snpGuestDevice = "/dev/sev-guest"
	// _IOWR('S', 0x1, struct snp_guest_request_ioctl): dir 3<<30, size
	// 32<<16, type 'S'<<8, nr 1.
	snpGetDerivedKeyIoctl = 0xc0205301
	// guest_field_select bits mixed into the derivation. GUEST_POLICY keeps
	// a debug-policy relaunch from deriving the production key; MEASUREMENT
	// binds the key to the golden image's launch measurement.
	snpFieldGuestPolicy = 1 << 0
	snpFieldMeasurement = 1 << 3
)

type snpDerivedKeyReq struct {
	// RootKeySelect 0 = VCEK: chip-unique root, so the derived key is
	// per-chip. VLEK (1) would widen the root to CSP scope.
	RootKeySelect    uint32
	_                uint32
	GuestFieldSelect uint64
	VMPL             uint32
	GuestSVN         uint32
	TCBVersion       uint64
}

type snpDerivedKeyResp struct {
	Status uint32
	_      [28]byte
	Data   [32]byte
}

type snpGuestRequest struct {
	MsgVersion uint8
	_          [7]byte
	ReqData    uint64
	RespData   uint64
	ExitInfo2  uint64
}

// snpDerivedKey asks the PSP for the measurement-bound derived key. Only an
// SNP guest has /dev/sev-guest; anywhere else this fails and the mount never
// converges — fail closed, never fall back to a weaker key.
func snpDerivedKey() ([]byte, error) {
	fd, err := unix.Open(snpGuestDevice, unix.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("guestd: opening %s (not an SNP guest?): %w", snpGuestDevice, err)
	}
	defer unix.Close(fd)

	req := snpDerivedKeyReq{
		GuestFieldSelect: snpFieldGuestPolicy | snpFieldMeasurement,
	}
	var resp snpDerivedKeyResp
	request := snpGuestRequest{
		MsgVersion: 1,
		ReqData:    uint64(uintptr(unsafe.Pointer(&req))),
		RespData:   uint64(uintptr(unsafe.Pointer(&resp))),
	}
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), snpGetDerivedKeyIoctl, uintptr(unsafe.Pointer(&request))); errno != 0 {
		return nil, fmt.Errorf("guestd: SNP_GET_DERIVED_KEY: firmware error %#x: %w", request.ExitInfo2, errno)
	}
	if resp.Status != 0 {
		return nil, fmt.Errorf("guestd: SNP_GET_DERIVED_KEY: status %#x", resp.Status)
	}
	key := make([]byte, len(resp.Data))
	copy(key, resp.Data[:])
	return key, nil
}
