package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// guardianConfig is the operator-local config file. Stored paths are
// absolute so they bypass resolvePath's BUILD_WORKING_DIRECTORY concern.
type guardianConfig struct {
	Host string `yaml:"host,omitempty"`
}

func configPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("config: %w", err)
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "guardian", "config.yaml"), nil
}

func loadConfig() (*guardianConfig, error) {
	path, err := configPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &guardianConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var c guardianConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	// Stored paths must be absolute; a relative path would quietly resolve
	// against the invoking cwd.
	if c.Host != "" && !filepath.IsAbs(c.Host) {
		return nil, fmt.Errorf("config %s: host must be an absolute path, got %q", path, c.Host)
	}
	return &c, nil
}

func saveConfig(c *guardianConfig) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// WriteFile applies the mode only at creation; tighten pre-existing files.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// runConfigCmd prints the config with no args, or sets one key from a path.
// Setting a key replaces its previous value; there is no unset verb.
func runConfigCmd(args []string) error {
	if len(args) == 0 {
		path, err := configPath()
		if err != nil {
			return err
		}
		fmt.Println(path)
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) || (err == nil && len(bytes.TrimSpace(raw)) == 0) {
			fmt.Println("empty")
			return nil
		}
		if err != nil {
			return fmt.Errorf("config: %w", err)
		}
		fmt.Print(string(raw))
		return nil
	}
	key := args[0]
	if key != "host" {
		return fmt.Errorf("config: unknown key %q; the only key is host", key)
	}
	if len(args) != 2 {
		return fmt.Errorf("config: usage: guardian config %s <path>", key)
	}
	abs, err := filepath.Abs(resolvePath(args[1]))
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("config: %s is not a regular file", abs)
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	c.Host = abs
	if err := saveConfig(c); err != nil {
		return err
	}
	fmt.Printf("%s\t%s\n", key, abs)
	return nil
}
