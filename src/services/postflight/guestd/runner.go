package guestd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// RunnerRoot is where the golden image installs the actions runner tree.
const RunnerRoot = "/opt/actions-runner"

// RunnerEvent is a runner lifecycle observation.
type RunnerEvent int

const (
	// EventListening: the runner registered with GitHub and is listening
	// for its job.
	EventListening RunnerEvent = iota
	// EventJobStarted: the runner picked up its job.
	EventJobStarted
)

// RunRunner starts the actions runner with the JIT registration blob and
// blocks until it exits, reporting lifecycle events as they are observed.
// The exit code is meaningful only when err is nil.
type RunRunner func(ctx context.Context, jitConfig string, env map[string]string, event func(RunnerEvent)) (int, error)

// ExecRunner is the production RunRunner: run.sh under the runner user,
// output scanned for the lifecycle lines. The JIT blob exists only in this
// process and the runner's argv/environment — never on any disk.
func ExecRunner(root, username string, logger *slog.Logger) RunRunner {
	return func(ctx context.Context, jitConfig string, env map[string]string, event func(RunnerEvent)) (int, error) {
		account, err := user.Lookup(username)
		if err != nil {
			return 0, fmt.Errorf("guestd: looking up %s: %w", username, err)
		}
		uid, err := strconv.ParseUint(account.Uid, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("guestd: uid of %s: %w", username, err)
		}
		gid, err := strconv.ParseUint(account.Gid, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("guestd: gid of %s: %w", username, err)
		}

		cmd := exec.CommandContext(ctx, filepath.Join(root, "run.sh"), "--jitconfig", jitConfig)
		cmd.Dir = root
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
		}
		cmd.Env = runnerEnviron(account, env)

		read, write, err := os.Pipe()
		if err != nil {
			return 0, err
		}
		cmd.Stdout = write
		cmd.Stderr = write
		if err := cmd.Start(); err != nil {
			read.Close()
			write.Close()
			return 0, fmt.Errorf("guestd: starting runner: %w", err)
		}
		write.Close()

		seen := map[RunnerEvent]bool{}
		scanner := bufio.NewScanner(read)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			logger.Info("runner", "line", line)
			if e, ok := runnerLineEvent(line); ok && !seen[e] {
				seen[e] = true
				event(e)
			}
		}
		read.Close()

		err = cmd.Wait()
		if err == nil {
			return 0, nil
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 0, fmt.Errorf("guestd: waiting on runner: %w", err)
	}
}

// runnerEnviron builds the runner's environment: the account's identity
// plus the assignment env, deterministically ordered.
func runnerEnviron(account *user.User, env map[string]string) []string {
	environ := []string{
		"HOME=" + account.HomeDir,
		"USER=" + account.Username,
		"LOGNAME=" + account.Username,
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		environ = append(environ, key+"="+env[key])
	}
	return environ
}

// runnerLineEvent maps runner output to lifecycle events. The runner has no
// machine-readable status channel; these are the stable human lines it
// prints when it registers and when the job lands.
func runnerLineEvent(line string) (RunnerEvent, bool) {
	switch {
	case strings.Contains(line, "Listening for Jobs"):
		return EventListening, true
	case strings.Contains(line, "Running job:"):
		return EventJobStarted, true
	}
	return 0, false
}
