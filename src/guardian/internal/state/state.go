package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Layout struct {
	Root           string `json:"root"`
	TalmProject    string `json:"talmProject"`
	TalmValues     string `json:"talmValues"`
	NodeConfig     string `json:"nodeConfig"`
	HostPatch      string `json:"hostPatch"`
	Kubeconfig     string `json:"kubeconfig"`
	Platform       string `json:"platform"`
	HelloWorld     string `json:"helloWorld"`
	Operation      string `json:"operation"`
	GenesisArchive string `json:"genesisArchive"`
}

type Operation struct {
	ClusterName  string    `json:"clusterName"`
	ConfigDigest string    `json:"configDigest"`
	Stage        string    `json:"stage"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

func Open(clusterName string) (*Layout, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("state root: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	root := filepath.Join(base, "guardian", "clusters", clusterName)
	layout := &Layout{
		Root:           root,
		TalmProject:    filepath.Join(root, "talm"),
		TalmValues:     filepath.Join(root, "talm", "values.yaml"),
		NodeConfig:     filepath.Join(root, "talm", "nodes", "controlplane.yaml"),
		HostPatch:      filepath.Join(root, "talm", "guardian-host-patch.yaml"),
		Kubeconfig:     filepath.Join(root, "talm", "kubeconfig"),
		Platform:       filepath.Join(root, "cozystack-platform.yaml"),
		HelloWorld:     filepath.Join(root, "hello-world.yaml"),
		Operation:      filepath.Join(root, "operation.json"),
		GenesisArchive: filepath.Join(root, "genesis.bundle.tar.age"),
	}
	for _, dir := range []string{layout.Root, layout.TalmProject, filepath.Dir(layout.NodeConfig)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("state dir %s: %w", dir, err)
		}
	}
	return layout, nil
}

func WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func WriteOperation(path string, op Operation) error {
	raw, err := json.MarshalIndent(op, "", "  ")
	if err != nil {
		return fmt.Errorf("encode operation: %w", err)
	}
	return WriteFile(path, append(raw, '\n'))
}
