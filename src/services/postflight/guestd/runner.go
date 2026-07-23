package guestd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// RunnerRoot is where the golden image installs the actions runner tree.
const RunnerRoot = "/opt/actions-runner"

const runnerImageEnvironment = "/etc/environment"

// outputDrainGrace is how long buffered runner output may trail the runner's
// exit before the pipe is severed.
const outputDrainGrace = 2 * time.Second

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

// ExecRunner is the production RunRunner: the one-job Listener under the
// runner user, with output scanned for lifecycle lines. Running Listener
// directly is important: run.sh retries Listener exit code 2 forever, which
// would hide a pre-Worker protocol rejection from hostd and occupy the slot
// until its listening deadline. The JIT blob exists only in this process and
// the runner's argv/environment — never on any disk.
func ExecRunner(root, username string, logger *slog.Logger) RunRunner {
	return func(ctx context.Context, jitConfig string, env map[string]string, event func(RunnerEvent)) (int, error) {
		account, err := user.Lookup(username)
		if err != nil {
			return 0, fmt.Errorf("guestd: looking up %s: %w", username, err)
		}
		credential, err := accountCredential(account)
		if err != nil {
			return 0, fmt.Errorf("guestd: credentials of %s: %w", username, err)
		}
		imageEnv, err := readRunnerImageEnvironment(runnerImageEnvironment, account)
		if err != nil {
			return 0, fmt.Errorf("guestd: runner image environment: %w", err)
		}

		cmd := runnerCommand(ctx, root, jitConfig)
		cmd.Dir = root
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Credential: credential,
		}
		cmd.Env = runnerEnviron(account, imageEnv, env)

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

		observer := observeRunnerOutput(read, redactor(jitConfig, env), logger, event)
		err = cmd.Wait()
		observer.drain(outputDrainGrace)
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

func accountCredential(account *user.User) (*syscall.Credential, error) {
	uid, err := strconv.ParseUint(account.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("uid: %w", err)
	}
	gid, err := strconv.ParseUint(account.Gid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("gid: %w", err)
	}
	rawGroups, err := account.GroupIds()
	if err != nil {
		return nil, fmt.Errorf("supplementary groups: %w", err)
	}
	groups, err := parseGroupIDs(rawGroups)
	if err != nil {
		return nil, err
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: groups}, nil
}

func parseGroupIDs(raw []string) ([]uint32, error) {
	unique := make(map[uint32]struct{}, len(raw))
	for _, value := range raw {
		group, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("supplementary group %q: %w", value, err)
		}
		unique[uint32(group)] = struct{}{}
	}
	groups := make([]uint32, 0, len(unique))
	for group := range unique {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	return groups, nil
}

func runnerCommand(ctx context.Context, root, jitConfig string) *exec.Cmd {
	return exec.CommandContext(ctx, filepath.Join(root, "bin", "Runner.Listener"), "run", "--jitconfig", jitConfig)
}

// outputObserver mirrors runner output into the log and folds lifecycle
// lines into events without ever gating the runner's exit on its pipe: a
// leaked customer child that inherits stdout keeps the pipe open past the
// runner's death, and the exit report must outrank straggler output.
type outputObserver struct {
	read *os.File
	done chan struct{}
}

func observeRunnerOutput(read *os.File, redact *strings.Replacer, logger *slog.Logger, event func(RunnerEvent)) *outputObserver {
	observer := &outputObserver{read: read, done: make(chan struct{})}
	go func() {
		defer close(observer.done)
		seen := map[RunnerEvent]bool{}
		scanner := bufio.NewScanner(read)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			line := redact.Replace(scanner.Text())
			logger.Info("runner", "line", line)
			if e, ok := runnerLineEvent(line); ok && !seen[e] {
				seen[e] = true
				event(e)
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
			// The scan stopped (an over-long line, most likely) but the pipe
			// must keep draining or the runner blocks on a full buffer.
			logger.Warn("runner output scan stopped", "err", err)
			_, _ = io.Copy(io.Discard, read)
		}
	}()
	return observer
}

// drain gives buffered output a bounded window after the runner exits, then
// severs the pipe. No event fires after drain returns.
func (o *outputObserver) drain(grace time.Duration) {
	select {
	case <-o.done:
	case <-time.After(grace):
	}
	o.read.Close()
	<-o.done
}

// redactor scrubs every assignment-provided value from mirrored output. The
// runner masks GitHub-registered secrets, but the checkout token and JIT
// blob are ours, and a customer step that prints its environment must not
// land them in the guest journal.
func redactor(jitConfig string, env map[string]string) *strings.Replacer {
	values := make([]string, 0, len(env)+1)
	if jitConfig != "" {
		values = append(values, jitConfig)
	}
	for _, value := range env {
		if value != "" {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if len(values[i]) != len(values[j]) {
			return len(values[i]) > len(values[j])
		}
		return values[i] < values[j]
	})
	pairs := make([]string, 0, 2*len(values))
	for _, value := range values {
		pairs = append(pairs, value, "[redacted]")
	}
	return strings.NewReplacer(pairs...)
}

// readRunnerImageEnvironment imports the root-owned compatibility contract
// produced by actions/runner-images. PAM expands HOME for interactive users;
// guestd starts the listener directly, so it performs that expansion here.
func readRunnerImageEnvironment(path string, account *user.User) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	env := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || !validEnvironmentName(key) {
			return nil, fmt.Errorf("invalid environment entry %q", line)
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && value[0] == value[len(value)-1] && (value[0] == '\'' || value[0] == '"') {
			if value[0] == '"' {
				value, err = strconv.Unquote(value)
				if err != nil {
					return nil, fmt.Errorf("environment %s: %w", key, err)
				}
			} else {
				value = value[1 : len(value)-1]
			}
		}
		value = strings.NewReplacer("${HOME}", account.HomeDir, "$HOME", account.HomeDir).Replace(value)
		env[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return env, nil
}

func validEnvironmentName(name string) bool {
	for i, char := range name {
		if (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_' || (i > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return name != ""
}

// runnerEnviron layers assignment-specific values over the image's declared
// toolchain environment and the runner account identity.
func runnerEnviron(account *user.User, imageEnv, assignmentEnv map[string]string) []string {
	env := map[string]string{
		"HOME":    account.HomeDir,
		"USER":    account.Username,
		"LOGNAME": account.Username,
		"PATH":    "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	for key, value := range imageEnv {
		env[key] = value
	}
	env["HOME"] = account.HomeDir
	env["USER"] = account.Username
	env["LOGNAME"] = account.Username
	for key, value := range assignmentEnv {
		env[key] = value
	}

	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environ := make([]string, 0, len(keys))
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
