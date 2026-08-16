package guestd

import (
	"crypto/hkdf"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"strings"
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

// ErrSNPUnavailable reports that the platform cannot ask an AMD Secure
// Processor for a measurement-bound key. EncryptionSNP must propagate this
// error; it must never fall back to the development key or plaintext.
var ErrSNPUnavailable = errors.New("guestd: AMD SEV-SNP key derivation unavailable")

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
