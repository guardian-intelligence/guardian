package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const (
	staticSealKeyBytes = 32
	defaultCluster     = "guardian-mgmt"
	defaultRegion      = "ash"
	defaultNodeDir     = "/var/lib/guardian/openbao/static-seal"
)

var safeSegmentRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type options struct {
	cluster  string
	region   string
	keyID    string
	home     string
	outDir   string
	filename string
	nodeDir  string
	now      time.Time
	random   io.Reader
	stdout   io.Writer
}

type metadata struct {
	Cluster          string `json:"cluster"`
	Region           string `json:"region"`
	KeyID            string `json:"key_id"`
	CreatedAt        string `json:"created_at"`
	Bytes            int    `json:"bytes"`
	SHA256           string `json:"sha256"`
	LocalKeyPath     string `json:"local_key_path"`
	LocalMetadata    string `json:"local_metadata_path"`
	NodeDirectory    string `json:"node_directory"`
	NodeFilename     string `json:"node_filename"`
	OpenBAOFileURI   string `json:"openbao_file_uri"`
	OpenBAOMountPath string `json:"openbao_mount_path"`
}

func main() {
	now := time.Now().UTC()
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve home directory: %v\n", err)
		os.Exit(1)
	}

	opts := options{
		cluster: defaultCluster,
		region:  defaultRegion,
		home:    home,
		nodeDir: defaultNodeDir,
		now:     now,
		random:  rand.Reader,
		stdout:  os.Stdout,
	}

	flag.StringVar(&opts.cluster, "cluster", opts.cluster, "management cluster name")
	flag.StringVar(&opts.region, "region", opts.region, "region code")
	flag.StringVar(&opts.keyID, "key-id", "", "expected static seal key identifier; defaults to the key SHA-256 fingerprint")
	flag.StringVar(&opts.home, "home", opts.home, "home directory containing .guardian")
	flag.StringVar(&opts.outDir, "out-dir", "", "output directory; defaults under ~/.guardian")
	flag.StringVar(&opts.filename, "filename", "", "key filename; defaults to unseal-<key-id>.key")
	flag.StringVar(&opts.nodeDir, "node-dir", opts.nodeDir, "directory where the key is mounted on OpenBao nodes")
	flag.Parse()

	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

func run(opts options) error {
	if opts.now.IsZero() {
		opts.now = time.Now().UTC()
	}
	if opts.random == nil {
		opts.random = rand.Reader
	}
	if opts.stdout == nil {
		opts.stdout = io.Discard
	}

	if err := validateSegment("cluster", opts.cluster); err != nil {
		return err
	}
	if err := validateSegment("region", opts.region); err != nil {
		return err
	}
	if opts.keyID != "" {
		if err := validateSegment("key-id", opts.keyID); err != nil {
			return err
		}
	}
	if opts.filename != "" {
		if err := validateFilename(opts.filename); err != nil {
			return err
		}
	}
	if opts.nodeDir == "" || !filepath.IsAbs(opts.nodeDir) {
		return fmt.Errorf("node-dir must be absolute: %q", opts.nodeDir)
	}

	key := make([]byte, staticSealKeyBytes)
	if _, err := io.ReadFull(opts.random, key); err != nil {
		return fmt.Errorf("read random key bytes: %w", err)
	}
	sum := sha256.Sum256(key)
	fingerprint := hex.EncodeToString(sum[:])
	if opts.keyID == "" {
		opts.keyID = fingerprint
	}
	if opts.keyID != fingerprint {
		return fmt.Errorf("key-id must match generated key SHA-256 fingerprint: got %q, want %q", opts.keyID, fingerprint)
	}

	if err := validateSegment("key-id", opts.keyID); err != nil {
		return err
	}
	if opts.filename == "" {
		opts.filename = "unseal-" + opts.keyID + ".key"
	}

	outDir := opts.outDir
	if outDir == "" {
		if opts.home == "" {
			return errors.New("home is required when out-dir is not set")
		}
		outDir = filepath.Join(opts.home, ".guardian", "openbao", opts.cluster+"-"+opts.region, "static-seal", opts.keyID)
	}

	keyPath := filepath.Join(outDir, opts.filename)
	metadataPath := filepath.Join(outDir, "metadata.json")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.Chmod(outDir, 0o700); err != nil {
		return fmt.Errorf("chmod output directory: %w", err)
	}

	if err := writeNewFile(keyPath, key, 0o600); err != nil {
		return err
	}

	m := metadata{
		Cluster:          opts.cluster,
		Region:           opts.region,
		KeyID:            opts.keyID,
		CreatedAt:        opts.now.UTC().Format(time.RFC3339),
		Bytes:            staticSealKeyBytes,
		SHA256:           fingerprint,
		LocalKeyPath:     keyPath,
		LocalMetadata:    metadataPath,
		NodeDirectory:    opts.nodeDir,
		NodeFilename:     opts.filename,
		OpenBAOMountPath: filepath.Join("/openbao/secrets", opts.filename),
	}
	m.OpenBAOFileURI = "file://" + m.OpenBAOMountPath

	payload, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	payload = append(payload, '\n')
	if err := writeNewFile(metadataPath, payload, 0o600); err != nil {
		return err
	}

	fmt.Fprintf(opts.stdout, "wrote OpenBao static seal key\n")
	fmt.Fprintf(opts.stdout, "key_id=%s\n", m.KeyID)
	fmt.Fprintf(opts.stdout, "key_path=%s\n", m.LocalKeyPath)
	fmt.Fprintf(opts.stdout, "metadata_path=%s\n", m.LocalMetadata)
	fmt.Fprintf(opts.stdout, "sha256=%s\n", m.SHA256)
	return nil
}

func validateSegment(name, value string) error {
	if !safeSegmentRE.MatchString(value) {
		return fmt.Errorf("%s must match %s: %q", name, safeSegmentRE.String(), value)
	}
	if value == "." || value == ".." {
		return fmt.Errorf("%s must not be a relative path segment: %q", name, value)
	}
	return nil
}

func validateFilename(value string) error {
	if filepath.Base(value) != value {
		return fmt.Errorf("filename must not contain path separators: %q", value)
	}
	return validateSegment("filename", value)
}

func writeNewFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("refusing to overwrite existing file: %s", path)
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()

	if err := f.Chmod(perm); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
