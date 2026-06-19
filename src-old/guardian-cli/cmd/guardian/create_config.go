package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"
	"cuelang.org/go/cue/parser"
)

type createConfig struct {
	Spec       createSpec
	SpecDigest string
	Canonical  []byte
}

func loadCreateConfig(path string) (*createConfig, error) {
	resolved := resolvePath(path)
	args, err := createConfigCueArgs(resolved)
	if err != nil {
		return nil, fmt.Errorf("create config %s: %w", path, err)
	}
	ctx := cuecontext.New()
	instances := load.Instances(args, &load.Config{Dir: filepath.Dir(resolved)})
	if len(instances) != 1 {
		return nil, fmt.Errorf("create config %s: got %d CUE instances, want 1", path, len(instances))
	}
	if err := instances[0].Err; err != nil {
		return nil, fmt.Errorf("create config %s: %w", path, err)
	}
	value := ctx.BuildInstance(instances[0])
	if err := value.Err(); err != nil {
		return nil, fmt.Errorf("create config %s: %w", path, err)
	}
	var spec createSpec
	if err := value.Decode(&spec); err != nil {
		return nil, fmt.Errorf("create config %s: %w", path, err)
	}
	if err := validateCreateSpec(spec); err != nil {
		return nil, err
	}
	canonical, digest, err := canonicalCreateSpec(spec)
	if err != nil {
		return nil, err
	}
	return &createConfig{Spec: spec, SpecDigest: digest, Canonical: canonical}, nil
}

func createConfigCueArgs(resolved string) ([]string, error) {
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("entrypoint must be a CUE file, got directory")
	}
	if filepath.Ext(resolved) != ".cue" {
		return nil, fmt.Errorf("entrypoint must be a .cue file")
	}
	entrypoint, err := parser.ParseFile(resolved, nil)
	if err != nil {
		return nil, fmt.Errorf("parse entrypoint: %w", err)
	}
	pkg := entrypoint.PackageName()
	args := []string{filepath.Base(resolved)}
	if pkg == "" {
		return args, nil
	}
	dir := filepath.Dir(resolved)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var siblings []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() ||
			name == filepath.Base(resolved) ||
			filepath.Ext(name) != ".cue" ||
			strings.HasSuffix(name, "_test.cue") ||
			strings.HasPrefix(name, ".") ||
			strings.HasPrefix(name, "_") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(path, nil)
		if err != nil {
			return nil, fmt.Errorf("parse sibling %s: %w", name, err)
		}
		if file.PackageName() == pkg {
			siblings = append(siblings, name)
		}
	}
	sort.Strings(siblings)
	args = append(args, siblings...)
	return args, nil
}

func validateCreateSpec(spec createSpec) error {
	var missing []string
	require := func(path, value string) {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, path)
		}
	}
	require("provider.name", spec.Provider.Name)
	require("provider.project", spec.Provider.Project)
	require("cluster.name", spec.Cluster.Name)
	require("cluster.endpoint", spec.Cluster.Endpoint)
	require("host.address", spec.Host.Address)
	require("host.hostname", spec.Host.Hostname)
	require("host.interfaceMac", spec.Host.InterfaceMAC)
	require("host.installDiskSerial", spec.Host.InstallDiskSerial)
	require("talos.version", spec.Talos.Version)
	require("cozystack.version", spec.Cozystack.Version)
	if spec.Provider.ServerID == "" && spec.Create == nil {
		missing = append(missing, "provider.serverId or create")
	}
	if spec.Create != nil {
		require("create.hostname", spec.Create.Hostname)
		require("create.metro", spec.Create.Metro)
		require("create.plan", spec.Create.Plan)
	}
	if len(missing) > 0 {
		return fmt.Errorf("create config missing required fields: %s", strings.Join(missing, ", "))
	}
	if spec.Provider.Name != "latitude" {
		return fmt.Errorf("create config provider.name: got %q, want latitude", spec.Provider.Name)
	}
	if _, err := url.ParseRequestURI(spec.Cluster.Endpoint); err != nil || !strings.HasPrefix(spec.Cluster.Endpoint, "https://") {
		return fmt.Errorf("create config cluster.endpoint must be an https URL, got %q", spec.Cluster.Endpoint)
	}
	if ip := net.ParseIP(spec.Host.Address); ip == nil {
		return fmt.Errorf("create config host.address must be an IP address, got %q", spec.Host.Address)
	}
	if _, err := net.ParseMAC(spec.Host.InterfaceMAC); err != nil {
		return fmt.Errorf("create config host.interfaceMac: %w", err)
	}
	return nil
}

func canonicalCreateSpec(spec createSpec) ([]byte, string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(spec); err != nil {
		return nil, "", fmt.Errorf("canonical create spec: %w", err)
	}
	raw := bytes.TrimSpace(buf.Bytes())
	sum := sha256.Sum256(raw)
	return raw, hex.EncodeToString(sum[:]), nil
}

func createStateRoot() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("create state root: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	root := filepath.Join(base, "guardian", "create")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create state root: %w", err)
	}
	return root, nil
}

func newOperationID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "op_" + hex.EncodeToString(raw[:]), nil
}

func loadMatchingOperation(root, clusterName, specDigest string) (*operationRecord, error) {
	records, err := loadOperationsForCluster(root, clusterName)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.SpecDigest == specDigest {
			return record, nil
		}
	}
	return nil, nil
}

func loadMismatchedOperation(root, clusterName, specDigest string) (*operationRecord, error) {
	records, err := loadOperationsForCluster(root, clusterName)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if record.SpecDigest != specDigest {
			return record, nil
		}
	}
	return nil, nil
}

func loadOperationsForCluster(root, clusterName string) ([]*operationRecord, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read create state: %w", err)
	}
	var records []*operationRecord
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record, err := readOperationRecord(filepath.Join(root, entry.Name()))
		if err != nil {
			return nil, err
		}
		if record.ClusterName == clusterName {
			records = append(records, record)
		}
	}
	return records, nil
}

func createOperation(root, clusterName, specDigest string, now time.Time, id func() (string, error)) (*operationRecord, error) {
	opID, err := id()
	if err != nil {
		return nil, fmt.Errorf("create operation id: %w", err)
	}
	dir := filepath.Join(root, opID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create operation state: %w", err)
	}
	record := &operationRecord{
		OperationID: opID,
		ClusterName: clusterName,
		SpecDigest:  specDigest,
		StateDir:    dir,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := writeOperationRecord(record); err != nil {
		return nil, err
	}
	return record, nil
}

func readOperationRecord(dir string) (*operationRecord, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "operation.json"))
	if err != nil {
		return nil, fmt.Errorf("read operation record: %w", err)
	}
	var record operationRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("decode operation record: %w", err)
	}
	return &record, nil
}

func writeOperationRecord(record *operationRecord) error {
	if record == nil {
		return nil
	}
	if err := os.MkdirAll(record.StateDir, 0o700); err != nil {
		return fmt.Errorf("operation state: %w", err)
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode operation record: %w", err)
	}
	path := filepath.Join(record.StateDir, "operation.json")
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write operation record: %w", err)
	}
	return os.Chmod(path, 0o600)
}

func writeCanonicalSpec(record *operationRecord, canonical []byte) error {
	if record == nil {
		return nil
	}
	path := filepath.Join(record.StateDir, "spec.json")
	if err := os.WriteFile(path, append(canonical, '\n'), 0o600); err != nil {
		return fmt.Errorf("write canonical create spec: %w", err)
	}
	return os.Chmod(path, 0o600)
}
