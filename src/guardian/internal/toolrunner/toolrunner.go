package toolrunner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
)

type Command struct {
	Name   string   `json:"name" yaml:"name" toml:"name"`
	Bin    string   `json:"bin" yaml:"bin" toml:"bin"`
	Args   []string `json:"args" yaml:"args" toml:"args"`
	Dir    string   `json:"dir" yaml:"dir" toml:"dir"`
	Stdin  []byte   `json:"-" yaml:"-" toml:"-"`
	Secret bool     `json:"secret,omitempty" yaml:"secret,omitempty" toml:"secret,omitempty"`
}

type Runner interface {
	Run(context.Context, Command) error
	Output(context.Context, Command) ([]byte, error)
}

type RealRunner struct {
	Stdout io.Writer
	Stderr io.Writer
}

func ToolPath(rlocation string) (string, error) {
	r, err := runfiles.New()
	if err != nil {
		return "", fmt.Errorf("runfiles: %w", err)
	}
	path, err := r.Rlocation(rlocation)
	if err != nil {
		return "", fmt.Errorf("runfiles %s: %w", rlocation, err)
	}
	return path, nil
}

func (r RealRunner) Run(ctx context.Context, c Command) error {
	stdout := r.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := r.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	fmt.Fprintf(stderr, "+ %s %s\n", c.Bin, strings.Join(c.Args, " "))
	cmd := exec.CommandContext(ctx, c.Bin, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", c.Bin, strings.Join(c.Args, " "), err)
	}
	return nil
}

func (r RealRunner) Output(ctx context.Context, c Command) ([]byte, error) {
	stderr := r.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	fmt.Fprintf(stderr, "+ %s %s\n", c.Bin, strings.Join(c.Args, " "))
	cmd := exec.CommandContext(ctx, c.Bin, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(c.Stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w\n%s", c.Bin, strings.Join(c.Args, " "), err, out)
	}
	return out, nil
}

func WaitTCP(ctx context.Context, address string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		dialer := net.Dialer{Timeout: 2 * time.Second}
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(address, "50000"))
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for %s:50000: %w", timeout, address, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
