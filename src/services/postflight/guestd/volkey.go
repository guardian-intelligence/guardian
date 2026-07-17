package guestd

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// EncryptionMode selects the workspace at-rest encryption behavior. The mode
// is baked into the golden image — it never arrives from the host, because
// an operator-supplied mode would be a downgrade lever on the very party the
// encryption is aimed at.
type EncryptionMode string

const (
	// EncryptionOff mounts the workspace zvol plaintext.
	EncryptionOff EncryptionMode = "off"
	// EncryptionDev keys LUKS2 from a public constant: real plumbing,
	// deliberately zero confidentiality. It exists so the format/open/reopen
	// pipeline runs everywhere while only SNP guests can hold a real secret.
	EncryptionDev EncryptionMode = "dev-insecure"
	// EncryptionSNP keys LUKS2 from the PSP-derived key bound to the launch
	// measurement: same-measurement guests on the same chip derive the same
	// key, the host never sees it.
	EncryptionSNP EncryptionMode = "snp"
)

// EncryptionModePath is where the golden image bakes the mode.
const EncryptionModePath = "/etc/postflight/workspace-encryption"

func (m EncryptionMode) enabled() bool { return m != "" && m != EncryptionOff }

// LoadEncryptionMode reads the baked mode; an absent file is EncryptionOff
// (images predating the file), an unrecognized value is an error so a typo
// can never silently mean plaintext.
func LoadEncryptionMode(path string) (EncryptionMode, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return EncryptionOff, nil
	}
	if err != nil {
		return "", fmt.Errorf("guestd: reading encryption mode: %w", err)
	}
	mode := EncryptionMode(strings.TrimSpace(string(raw)))
	switch mode {
	case EncryptionOff, EncryptionDev, EncryptionSNP:
		return mode, nil
	}
	return "", fmt.Errorf("guestd: unknown encryption mode %q in %s", mode, path)
}

// volumeKeyInfo domain-separates the workspace volume key from any other use
// of the same root secret.
const volumeKeyInfo = "postflight/workspace-volume/v1"

// workspaceKey derives the 256-bit LUKS2 key for the mode.
func workspaceKey(mode EncryptionMode) ([]byte, error) {
	switch mode {
	case EncryptionDev:
		// Public IKM: anyone with this source can decrypt a dev-mode volume.
		return hkdf.Key(sha256.New, []byte("postflight-dev-insecure-workspace"), nil, volumeKeyInfo, 32)
	case EncryptionSNP:
		root, err := snpDerivedKey()
		if err != nil {
			return nil, err
		}
		return hkdf.Key(sha256.New, root, nil, volumeKeyInfo, 32)
	}
	return nil, fmt.Errorf("guestd: no key source for encryption mode %q", mode)
}

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
