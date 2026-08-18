// Package generation defines the confidential Postflight generation and the
// state machine that is allowed to publish or restore it.
package generation

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	SchemaVersion          = 1
	RunnerClass            = "postflight-4-ubuntu-24.04-github-confidential"
	OperatingSystem        = "ubuntu"
	OperatingSystemVersion = "24.04"
	CheckpointEngine       = "criu"
	ConfidentialTechnology = "sev-snp"
)

var (
	digestPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:/-]{0,255}$`)
)

// Manifest couples every byte and compatibility constraint required to
// restore one process generation. Signature authenticates CanonicalBytes.
type Manifest struct {
	SchemaVersion      uint32             `json:"schema_version"`
	GenerationID       string             `json:"generation_id"`
	ParentGenerationID string             `json:"parent_generation_id,omitempty"`
	GenerationNumber   uint64             `json:"generation_number"`
	Identity           Identity           `json:"identity"`
	Workspace          Volume             `json:"workspace"`
	Root               Volume             `json:"root"`
	Tools              []Volume           `json:"tools"`
	Process            ProcessSnapshot    `json:"process"`
	Platform           Platform           `json:"platform"`
	Confidential       ConfidentialPolicy `json:"confidential"`
	WrappedDEK         []byte             `json:"wrapped_dek"`
	SignerKeyID        string             `json:"signer_key_id"`
	Signature          []byte             `json:"signature,omitempty"`
}

type Identity struct {
	Tenant     string `json:"tenant"`
	Repository string `json:"repository"`
	Branch     string `json:"branch"`
}

type Volume struct {
	Role          string `json:"role"`
	Generation    string `json:"generation"`
	SnapshotGUID  string `json:"snapshot_guid"`
	ContentDigest string `json:"content_digest"`
}

type ProcessSnapshot struct {
	CiphertextDigest string `json:"ciphertext_digest"`
	Engine           string `json:"engine"`
	FormatVersion    string `json:"format_version"`
}

type Platform struct {
	RunnerClass            string `json:"runner_class"`
	OperatingSystem        string `json:"operating_system"`
	OperatingSystemVersion string `json:"operating_system_version"`
	GuestImageDigest       string `json:"guest_image_digest"`
	KernelDigest           string `json:"kernel_digest"`
	QEMUVersion            string `json:"qemu_version"`
	CRIUVersion            string `json:"criu_version"`
	MachineType            string `json:"machine_type"`
	CPUModel               string `json:"cpu_model"`
	CPUIDDigest            string `json:"cpuid_digest"`
}

type ConfidentialPolicy struct {
	Technology   string `json:"technology"`
	Measurement  string `json:"measurement"`
	MinimumTCB   TCB    `json:"minimum_tcb"`
	DebugAllowed bool   `json:"debug_allowed"`
}

// TCB is componentwise. Treating AMD's packed TCB as one integer permits a
// high value in one component to hide a downgrade in another.
type TCB struct {
	Bootloader uint32 `json:"bootloader"`
	TEE        uint32 `json:"tee"`
	SNP        uint32 `json:"snp"`
	Microcode  uint32 `json:"microcode"`
}

func (m Manifest) CanonicalBytes() ([]byte, error) {
	unsigned := m
	unsigned.Signature = nil
	return json.Marshal(unsigned)
}

func (m *Manifest) Sign(privateKey ed25519.PrivateKey) error {
	if err := m.Validate(); err != nil {
		return err
	}
	encoded, err := m.CanonicalBytes()
	if err != nil {
		return err
	}
	m.Signature = ed25519.Sign(privateKey, encoded)
	return nil
}

func (m Manifest) Verify(publicKey ed25519.PublicKey) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if len(m.Signature) != ed25519.SignatureSize {
		return errors.New("generation: missing or malformed manifest signature")
	}
	encoded, err := m.CanonicalBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, encoded, m.Signature) {
		return errors.New("generation: manifest signature does not verify")
	}
	return nil
}

func (m Manifest) Digest() (string, error) {
	encoded, err := m.CanonicalBytes()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (m Manifest) Validate() error {
	switch {
	case m.SchemaVersion != SchemaVersion:
		return fmt.Errorf("generation: schema version %d is not %d", m.SchemaVersion, SchemaVersion)
	case !validIdentity(m.GenerationID):
		return errors.New("generation: invalid generation id")
	case m.ParentGenerationID != "" && !validIdentity(m.ParentGenerationID):
		return errors.New("generation: invalid parent generation id")
	case m.ParentGenerationID == m.GenerationID:
		return errors.New("generation: generation cannot parent itself")
	case m.GenerationNumber == 0:
		return errors.New("generation: generation number must be positive")
	case !validIdentity(m.Identity.Tenant) || !validIdentity(m.Identity.Repository) || !validIdentity(m.Identity.Branch):
		return errors.New("generation: invalid tenant, repository, or branch identity")
	case m.Platform.RunnerClass != RunnerClass || m.Platform.OperatingSystem != OperatingSystem || m.Platform.OperatingSystemVersion != OperatingSystemVersion:
		return errors.New("generation: unsupported runner platform")
	case m.Process.Engine != CheckpointEngine:
		return errors.New("generation: unsupported checkpoint engine")
	case m.Confidential.Technology != ConfidentialTechnology:
		return errors.New("generation: SEV-SNP is required")
	case m.Confidential.DebugAllowed:
		return errors.New("generation: debug policy is forbidden")
	case len(m.WrappedDEK) < 32:
		return errors.New("generation: wrapped DEK is missing")
	case !validIdentity(m.SignerKeyID):
		return errors.New("generation: signer key id is invalid")
	}
	for name, digest := range map[string]string{
		"process":     m.Process.CiphertextDigest,
		"guest image": m.Platform.GuestImageDigest,
		"kernel":      m.Platform.KernelDigest,
		"cpuid":       m.Platform.CPUIDDigest,
		"measurement": m.Confidential.Measurement,
	} {
		if !digestPattern.MatchString(digest) {
			return fmt.Errorf("generation: invalid %s digest", name)
		}
	}
	if m.Process.FormatVersion == "" || m.Platform.QEMUVersion == "" || m.Platform.CRIUVersion == "" || m.Platform.MachineType == "" || m.Platform.CPUModel == "" {
		return errors.New("generation: incomplete checkpoint compatibility tuple")
	}
	volumes := append([]Volume{m.Workspace, m.Root}, m.Tools...)
	if len(m.Tools) == 0 {
		return errors.New("generation: at least one tool volume is required")
	}
	seenRoles := map[string]bool{}
	for _, volume := range volumes {
		if !validIdentity(volume.Role) || !validIdentity(volume.Generation) || volume.SnapshotGUID == "" || !digestPattern.MatchString(volume.ContentDigest) {
			return fmt.Errorf("generation: invalid %s volume", volume.Role)
		}
		if seenRoles[volume.Role] {
			return fmt.Errorf("generation: duplicate volume role %q", volume.Role)
		}
		seenRoles[volume.Role] = true
	}
	if m.Workspace.Role != "workspace" || m.Root.Role != "root" {
		return errors.New("generation: workspace and root roles are fixed")
	}
	return nil
}

func validIdentity(value string) bool {
	return identityPattern.MatchString(value) && !strings.Contains(value, "..")
}

func MeetsMinimumTCB(observed, minimum TCB) bool {
	return observed.Bootloader >= minimum.Bootloader && observed.TEE >= minimum.TEE && observed.SNP >= minimum.SNP && observed.Microcode >= minimum.Microcode
}
