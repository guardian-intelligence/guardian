package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

// Pinned binaries ride in the guardian binary's runfiles; nothing is taken
// from the operator's PATH.
func talosctlPath() (string, error) { return toolPath("talosctl_linux_amd64/file/talosctl") }
func kubectlPath() (string, error)  { return toolPath("kubectl_linux_amd64/file/kubectl") }

func toolPath(rlocation string) (string, error) {
	r, err := runfiles.New()
	if err != nil {
		return "", fmt.Errorf("runfiles (run guardian via bazelisk run): %w", err)
	}
	p, err := r.Rlocation(rlocation)
	if err != nil {
		return "", fmt.Errorf("runfiles: %s: %w", rlocation, err)
	}
	return p, nil
}

// runTool streams a subprocess's output, prefixed so interleaved talosctl and
// kubectl output stays attributable during long converge runs.
func runTool(bin string, args ...string) error {
	fmt.Fprintf(os.Stderr, "+ %s %s\n", bin, strings.Join(args, " "))
	cmd := exec.Command(bin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	return nil
}

func runToolInput(input []byte, bin string, args ...string) error {
	fmt.Fprintf(os.Stderr, "+ %s %s (with stdin)\n", bin, strings.Join(args, " "))
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(string(input))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", bin, strings.Join(args, " "), err)
	}
	return nil
}

func outputTool(bin string, args ...string) (string, error) {
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w\n%s", bin, strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// poll runs probe until it succeeds or the deadline passes, reporting the
// last probe error on timeout.
func poll(what string, timeout, interval time.Duration, probe func() error) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		last = probe()
		if last == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s: %w", timeout, what, last)
		}
		fmt.Fprintf(os.Stderr, "waiting for %s (retry in %s)\n", what, interval)
		time.Sleep(interval)
	}
}
