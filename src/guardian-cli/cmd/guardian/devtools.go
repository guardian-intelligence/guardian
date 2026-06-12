package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// guardian tools install|uninstall manages development symlinks. Each managed
// tool name (and "guardian" itself) is symlinked at the guardian binary;
// invoked under another name, the binary dispatches to `guardian run <name>`.
// Symlinks point into bazel-bin, so rebuilding the CLI refreshes them in
// place; pins still resolve per-workspace at invocation time.

func runToolsCmd(args []string) error {
	if len(args) == 0 || (args[0] != "install" && args[0] != "uninstall") {
		return fmt.Errorf("tools: expected install or uninstall")
	}
	verb := args[0]
	fs := flag.NewFlagSet("tools "+verb, flag.ContinueOnError)
	binDir := fs.String("bin-dir", defaultBinDir(), "directory for tool symlinks")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("tools %s: unexpected arguments %v", verb, fs.Args())
	}
	names := append(append([]string{}, managedTools...), "guardian")
	if verb == "install" {
		return installSymlinks(*binDir, names)
	}
	return uninstallSymlinks(*binDir, names)
}

func defaultBinDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".local/bin"
	}
	return filepath.Join(home, ".local", "bin")
}

func installSymlinks(binDir string, names []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}
	for _, name := range names {
		link := filepath.Join(binDir, name)
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("tools install: %w", err)
		}
		if err := os.Symlink(self, link); err != nil {
			return fmt.Errorf("tools install: %w", err)
		}
		fmt.Printf("installed\t%s -> %s\n", link, self)
	}
	if !dirInPath(binDir) {
		fmt.Fprintf(os.Stderr, "guardian: warning: %s is not on PATH\n", binDir)
	}
	return nil
}

// uninstallSymlinks removes only links that resolve to a guardian binary;
// anything else under a managed name was not installed by us and is left
// alone.
func uninstallSymlinks(binDir string, names []string) error {
	for _, name := range names {
		link := filepath.Join(binDir, name)
		fi, err := os.Lstat(link)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			fmt.Fprintf(os.Stderr, "guardian: skipping %s: not a symlink\n", link)
			continue
		}
		dest, err := os.Readlink(link)
		if err != nil {
			return err
		}
		if filepath.Base(dest) != "guardian" {
			fmt.Fprintf(os.Stderr, "guardian: skipping %s: points at %s, not a guardian binary\n", link, dest)
			continue
		}
		if err := os.Remove(link); err != nil {
			return err
		}
		fmt.Printf("removed\t%s\n", link)
	}
	return nil
}

func dirInPath(dir string) bool {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	for _, p := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if p == "" {
			continue
		}
		if pabs, err := filepath.Abs(p); err == nil && pabs == abs {
			return true
		}
	}
	return false
}
