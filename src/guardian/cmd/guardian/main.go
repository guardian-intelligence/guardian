package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/guardian-intelligence/guardian/src/guardian/internal/config"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/output"
	statusview "github.com/guardian-intelligence/guardian/src/guardian/internal/status"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/toolrunner"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/up"
)

var errUsage = errors.New("usage")
var errSilent = errors.New("silent")

const usage = `usage:
  guardian up -f <host.json> [--execute] [--output text|json|yaml|toml] [--status auto|tui|plain|off]

guardian owns only host come-up: Talos via Talm, Kubernetes bootstrap,
Cozystack installer handoff/status, and genesis recovery material.`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "up":
		err = runUp(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "guardian: unknown command %q\n%s\n", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		if errors.Is(err, errSilent) {
			os.Exit(1)
		}
		if errors.Is(err, errUsage) {
			fmt.Fprintf(os.Stderr, "guardian: %v\n%s\n", err, usage)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "guardian: %v\n", err)
		os.Exit(1)
	}
}

func runUp(args []string) error {
	parsed, err := parseUpArgs(args)
	if err != nil {
		if errors.Is(err, errHelp) {
			fmt.Println(usage)
			return nil
		}
		return err
	}
	loaded, err := config.Load(parsed.HostPath)
	if err != nil {
		res := up.Result{Outcome: "NeedsConfig", Code: configLoadCode(err), SourcePath: parsed.HostPath}
		_ = output.Write(os.Stdout, res, parsed.Format)
		return errSilent
	}
	tools, err := resolveTools()
	if err != nil {
		return err
	}
	statusReporter, closeStatus, err := newStatusReporter(parsed, loaded.Config.Cluster.Name)
	if err != nil {
		return err
	}
	statusClosed := false
	defer func() {
		if !statusClosed {
			_ = closeStatus()
		}
	}()
	runner := toolrunner.RealRunner{}
	if statusReporter != nil || parsed.Format != "text" {
		runner.Stdout = io.Discard
		runner.Stderr = io.Discard
	}
	res := up.Run(context.Background(), loaded, tools, runner, up.Options{
		Execute: parsed.Execute,
		Status:  statusReporter,
	})
	if err := closeStatus(); err != nil {
		statusClosed = true
		return err
	}
	statusClosed = true
	humanStatus := statusReporter != nil && parsed.Format == "text"
	if !humanStatus {
		if err := output.Write(os.Stdout, res, parsed.Format); err != nil {
			return err
		}
	}
	if res.Outcome == "Converged" || res.Outcome == "Planned" {
		return nil
	}
	return errSilent
}

func configLoadCode(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "config.path.notFound"
	}
	return "config.load"
}

var errHelp = errors.New("help")

type upArgs struct {
	HostPath string
	Execute  bool
	Format   string
	Status   string
}

func parseUpArgs(args []string) (upArgs, error) {
	parsed := upArgs{Format: "text", Status: "auto"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return upArgs{}, errHelp
		case arg == "--execute":
			parsed.Execute = true
		case arg == "-f" || arg == "--file":
			i++
			if i >= len(args) {
				return upArgs{}, fmt.Errorf("up: %w: %s requires a value", errUsage, arg)
			}
			if err := setHostPath(&parsed, args[i]); err != nil {
				return upArgs{}, err
			}
		case strings.HasPrefix(arg, "--file="):
			if err := setHostPath(&parsed, strings.TrimPrefix(arg, "--file=")); err != nil {
				return upArgs{}, err
			}
		case arg == "--output":
			i++
			if i >= len(args) {
				return upArgs{}, fmt.Errorf("up: %w: --output requires a value", errUsage)
			}
			parsed.Format = args[i]
		case strings.HasPrefix(arg, "--output="):
			parsed.Format = strings.TrimPrefix(arg, "--output=")
		case arg == "--status":
			i++
			if i >= len(args) {
				return upArgs{}, fmt.Errorf("up: %w: --status requires a value", errUsage)
			}
			parsed.Status = args[i]
		case strings.HasPrefix(arg, "--status="):
			parsed.Status = strings.TrimPrefix(arg, "--status=")
		case strings.HasPrefix(arg, "-"):
			return upArgs{}, fmt.Errorf("up: %w: unknown flag %q", errUsage, arg)
		default:
			return upArgs{}, fmt.Errorf("up: %w: host path must be passed with -f or --file", errUsage)
		}
	}
	switch parsed.Format {
	case "text", "json", "yaml", "toml":
	default:
		return upArgs{}, fmt.Errorf("up: %w: unsupported --output %q", errUsage, parsed.Format)
	}
	switch parsed.Status {
	case "auto", "tui", "plain", "off":
	default:
		return upArgs{}, fmt.Errorf("up: %w: unsupported --status %q", errUsage, parsed.Status)
	}
	if parsed.HostPath == "" {
		return upArgs{}, fmt.Errorf("up: %w: expected -f <host JSON config path>", errUsage)
	}
	return parsed, nil
}

func setHostPath(parsed *upArgs, path string) error {
	if path == "" {
		return fmt.Errorf("up: %w: host path must not be empty", errUsage)
	}
	if parsed.HostPath != "" {
		return fmt.Errorf("up: %w: expected one host JSON config path", errUsage)
	}
	parsed.HostPath = path
	return nil
}

func newStatusReporter(parsed upArgs, clusterName string) (up.StatusReporter, func() error, error) {
	if !parsed.Execute || parsed.Status == "off" || (parsed.Status == "auto" && parsed.Format != "text") {
		return nil, func() error { return nil }, nil
	}
	mode := parsed.Status
	if mode == "auto" {
		if isTerminal(os.Stderr) {
			mode = "tui"
		} else {
			mode = "plain"
		}
	}
	var input io.Reader
	if mode == "tui" && isTerminal(os.Stdin) {
		input = os.Stdin
	}
	renderer := statusview.New(os.Stderr, statusview.Options{
		Mode:        statusview.Mode(mode),
		ClusterName: clusterName,
		Input:       input,
	})
	return renderer, renderer.Close, nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func resolveTools() (up.Tools, error) {
	resolve := func(name, rlocation string) (string, error) {
		path, err := toolrunner.ToolPath(rlocation)
		if err != nil {
			return "", fmt.Errorf("%s: %w", name, err)
		}
		return path, nil
	}
	talm, err := resolve("talm", "talm_linux_amd64/talm")
	if err != nil {
		return up.Tools{}, err
	}
	talos, err := resolve("talosctl", "talosctl_linux_amd64/file/talosctl")
	if err != nil {
		return up.Tools{}, err
	}
	kubectl, err := resolve("kubectl", "kubectl_linux_amd64/file/kubectl")
	if err != nil {
		return up.Tools{}, err
	}
	helm, err := resolve("helm", "helm_linux_amd64/helm")
	if err != nil {
		return up.Tools{}, err
	}
	bootToTalos, err := resolve("boot-to-talos", "boot_to_talos_linux_amd64/boot-to-talos")
	if err != nil {
		return up.Tools{}, err
	}
	return up.Tools{Talm: talm, Talos: talos, Kubectl: kubectl, Helm: helm, BootToTalos: bootToTalos}, nil
}
