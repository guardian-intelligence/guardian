package genesis

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
)

type File struct {
	Path string `json:"path" yaml:"path" toml:"path"`
	Size int64  `json:"size" yaml:"size" toml:"size"`
}

type Manifest struct {
	Version      int       `json:"version" yaml:"version" toml:"version"`
	ClusterName  string    `json:"clusterName" yaml:"clusterName" toml:"clusterName"`
	ConfigDigest string    `json:"configDigest" yaml:"configDigest" toml:"configDigest"`
	CreatedAt    time.Time `json:"createdAt" yaml:"createdAt" toml:"createdAt"`
	Files        []File    `json:"files" yaml:"files" toml:"files"`
}

type Bundle struct {
	OutputPath   string
	Root         string
	ClusterName  string
	ConfigDigest string
	CreatedAt    time.Time
	Recipients   []string
	Files        []string
}

func ValidateRecipients(raw []string) error {
	_, err := parseRecipients(raw)
	return err
}

func WriteEncrypted(b Bundle) (Manifest, error) {
	recipients, err := parseRecipients(b.Recipients)
	if err != nil {
		return Manifest{}, err
	}
	if strings.TrimSpace(b.OutputPath) == "" {
		return Manifest{}, fmt.Errorf("genesis bundle output path is required")
	}
	if strings.TrimSpace(b.Root) == "" {
		return Manifest{}, fmt.Errorf("genesis bundle root is required")
	}
	files, payloads, err := readPayloads(b.Root, b.Files)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{
		Version:      1,
		ClusterName:  b.ClusterName,
		ConfigDigest: b.ConfigDigest,
		CreatedAt:    b.CreatedAt.UTC(),
		Files:        files,
	}
	if err := os.MkdirAll(filepath.Dir(b.OutputPath), 0o700); err != nil {
		return Manifest{}, fmt.Errorf("create genesis bundle dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(b.OutputPath), ".genesis-*.tar.age")
	if err != nil {
		return Manifest{}, fmt.Errorf("create genesis bundle temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return Manifest{}, fmt.Errorf("chmod genesis bundle temp file: %w", err)
	}
	ageWriter, err := age.Encrypt(tmp, recipients...)
	if err != nil {
		_ = tmp.Close()
		return Manifest{}, fmt.Errorf("create age writer: %w", err)
	}
	tarWriter := tar.NewWriter(ageWriter)
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = tarWriter.Close()
		_ = ageWriter.Close()
		_ = tmp.Close()
		return Manifest{}, fmt.Errorf("encode genesis manifest: %w", err)
	}
	if err := writeTarFile(tarWriter, "manifest.json", append(manifestBytes, '\n')); err != nil {
		_ = tarWriter.Close()
		_ = ageWriter.Close()
		_ = tmp.Close()
		return Manifest{}, err
	}
	for _, file := range files {
		if err := writeTarFile(tarWriter, filepath.ToSlash(filepath.Join("files", file.Path)), payloads[file.Path]); err != nil {
			_ = tarWriter.Close()
			_ = ageWriter.Close()
			_ = tmp.Close()
			return Manifest{}, err
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = ageWriter.Close()
		_ = tmp.Close()
		return Manifest{}, fmt.Errorf("close genesis tar writer: %w", err)
	}
	if err := ageWriter.Close(); err != nil {
		_ = tmp.Close()
		return Manifest{}, fmt.Errorf("close genesis age writer: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Manifest{}, fmt.Errorf("close genesis bundle temp file: %w", err)
	}
	if err := os.Rename(tmpPath, b.OutputPath); err != nil {
		return Manifest{}, fmt.Errorf("install genesis bundle: %w", err)
	}
	if err := os.Chmod(b.OutputPath, 0o600); err != nil {
		return Manifest{}, fmt.Errorf("chmod genesis bundle: %w", err)
	}
	return manifest, nil
}

func parseRecipients(raw []string) ([]age.Recipient, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("bootstrap.genesis.ageRecipients must contain at least one age recipient before destructive bootstrap")
	}
	recipients := make([]age.Recipient, 0, len(raw))
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, fmt.Errorf("bootstrap.genesis.ageRecipients contains an empty recipient")
		}
		recipient, err := age.ParseX25519Recipient(value)
		if err != nil {
			return nil, fmt.Errorf("bootstrap.genesis.ageRecipients: invalid age recipient %q: %w", value, err)
		}
		recipients = append(recipients, recipient)
	}
	return recipients, nil
}

func readPayloads(root string, relPaths []string) ([]File, map[string][]byte, error) {
	if len(relPaths) == 0 {
		return nil, nil, fmt.Errorf("genesis bundle file list is empty")
	}
	seen := map[string]bool{}
	payloads := map[string][]byte{}
	files := make([]File, 0, len(relPaths))
	for _, rel := range relPaths {
		clean, err := cleanRelativePath(rel)
		if err != nil {
			return nil, nil, err
		}
		if seen[clean] {
			continue
		}
		seen[clean] = true
		path := filepath.Join(root, filepath.FromSlash(clean))
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("read genesis source %s: %w", clean, err)
		}
		payloads[clean] = bytes.Clone(raw)
		files = append(files, File{Path: clean, Size: int64(len(raw))})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, payloads, nil
}

func cleanRelativePath(path string) (string, error) {
	path = filepath.ToSlash(strings.TrimSpace(path))
	if path == "" {
		return "", fmt.Errorf("genesis bundle path is empty")
	}
	if strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("genesis bundle path %q must be relative", path)
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("genesis bundle path %q escapes the state root", path)
	}
	return clean, nil
}

func writeTarFile(w *tar.Writer, name string, body []byte) error {
	header := &tar.Header{
		Name: name,
		Mode: 0o600,
		Size: int64(len(body)),
	}
	if err := w.WriteHeader(header); err != nil {
		return fmt.Errorf("write tar header %s: %w", name, err)
	}
	if _, err := io.Copy(w, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("write tar payload %s: %w", name, err)
	}
	return nil
}
