package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

var version = "dev"

type commandRunner interface {
	Run(ctx context.Context, program string, args []string, stdout io.Writer, stderr io.Writer) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, program string, args []string, stdout io.Writer, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

type exitCodeError interface {
	ExitCode() int
}

type managementBootstrapConfig struct {
	Aspect         string
	Revision       string
	Root           string
	Talosconfig    string
	Endpoints      string
	Nodes          string
	Kubeconfig     string
	RequestTimeout string
	WaitTimeout    string
	TofuEndpoint   string
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, execRunner{}))
}

func run(ctx context.Context, argv []string, stdout io.Writer, stderr io.Writer, runner commandRunner) int {
	if len(argv) == 0 {
		writeUsage(stderr)
		return 2
	}

	switch argv[0] {
	case "up":
		return runUp(ctx, argv[1:], stdout, stderr, runner)
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "--help":
		writeUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "ERROR: unknown command %q\n\n", argv[0])
		writeUsage(stderr)
		return 2
	}
}

func runUp(ctx context.Context, argv []string, stdout io.Writer, stderr io.Writer, runner commandRunner) int {
	if len(argv) == 0 {
		writeUpUsage(stderr)
		return 2
	}

	switch argv[0] {
	case "management":
		return runUpManagement(ctx, argv[1:], stdout, stderr, runner)
	case "help", "-h", "--help":
		writeUpUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "ERROR: unknown up target %q\n\n", argv[0])
		writeUpUsage(stderr)
		return 2
	}
}

func runUpManagement(ctx context.Context, argv []string, stdout io.Writer, stderr io.Writer, runner commandRunner) int {
	if wantsHelp(argv) {
		writeUpManagementUsage(stdout)
		return 0
	}

	cfg := managementBootstrapConfig{
		Aspect:         envDefault("GUARDIAN_ASPECT", "aspect"),
		Root:           "src/infrastructure/talm",
		Talosconfig:    "src/infrastructure/talm/talosconfig",
		Endpoints:      "10.8.0.250",
		Nodes:          "10.8.0.11,10.8.0.12,10.8.0.13",
		RequestTimeout: "15s",
		WaitTimeout:    "5m",
	}

	fs := flag.NewFlagSet("guardian up management", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { writeUpManagementUsage(fs.Output()) }
	fs.StringVar(&cfg.Aspect, "aspect", cfg.Aspect, "path to the pinned Aspect CLI launcher")
	fs.StringVar(&cfg.Revision, "revision", "", "merged main commit SHA source-controller must have applied")
	fs.StringVar(&cfg.Root, "root", cfg.Root, "Talm root containing gitignored management-cluster operator state")
	fs.StringVar(&cfg.Talosconfig, "talosconfig", cfg.Talosconfig, "talosconfig path")
	fs.StringVar(&cfg.Endpoints, "endpoints", cfg.Endpoints, "Talos API endpoint used for kubeconfig refresh and L2 validation")
	fs.StringVar(&cfg.Nodes, "nodes", cfg.Nodes, "comma-separated guardian-mgmt Talos nodes")
	fs.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "optional kubeconfig for live validation; defaults to <root>/kubeconfig after refresh")
	fs.StringVar(&cfg.RequestTimeout, "request-timeout", cfg.RequestTimeout, "kubectl API request timeout")
	fs.StringVar(&cfg.WaitTimeout, "wait-timeout", cfg.WaitTimeout, "Flux and workload readiness wait timeout")
	fs.StringVar(&cfg.TofuEndpoint, "tofu-backend-endpoint", "", "optional S3-compatible OpenTofu backend endpoint override; defaults to AWS_ENDPOINT_URL_S3, then checked-in backend tfvars")

	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "ERROR: unexpected argument %q\n\n", fs.Arg(0))
		writeUpManagementUsage(stderr)
		return 2
	}

	aspectArgs, err := cfg.aspectArgs()
	if err != nil {
		fmt.Fprintln(stderr, "ERROR:", err)
		return 2
	}
	if err := runner.Run(ctx, cfg.Aspect, aspectArgs, stdout, stderr); err != nil {
		var exitErr exitCodeError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(stderr, "ERROR: run %s %s: %v\n", cfg.Aspect, strings.Join(aspectArgs, " "), err)
		return 1
	}
	return 0
}

func (cfg managementBootstrapConfig) aspectArgs() ([]string, error) {
	if strings.TrimSpace(cfg.Revision) == "" {
		return nil, errors.New("--revision is required")
	}
	if strings.TrimSpace(cfg.Aspect) == "" {
		return nil, errors.New("--aspect is required")
	}
	if strings.TrimSpace(cfg.Root) == "" {
		return nil, errors.New("--root is required")
	}
	if strings.TrimSpace(cfg.Endpoints) == "" {
		return nil, errors.New("--endpoints is required")
	}
	if strings.TrimSpace(cfg.Nodes) == "" {
		return nil, errors.New("--nodes is required")
	}

	args := []string{
		"infra",
		"bootstrap",
		"--revision",
		cfg.Revision,
		"--root",
		cfg.Root,
		"--endpoints",
		cfg.Endpoints,
		"--nodes",
		cfg.Nodes,
		"--request-timeout",
		cfg.RequestTimeout,
		"--wait-timeout",
		cfg.WaitTimeout,
	}
	if cfg.Talosconfig != "" {
		args = append(args, "--talosconfig", cfg.Talosconfig)
	}
	if cfg.Kubeconfig != "" {
		args = append(args, "--kubeconfig", cfg.Kubeconfig)
	}
	if cfg.TofuEndpoint != "" {
		args = append(args, "--tofu-backend-endpoint", cfg.TofuEndpoint)
	}
	return args, nil
}

func envDefault(name string, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func wantsHelp(argv []string) bool {
	for _, arg := range argv {
		if arg == "help" || arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func writeUsage(w io.Writer) {
	fmt.Fprint(w, `guardian manages Guardian host come-up.

Usage:
  guardian up management --revision <merged-main-commit-sha> [flags]
  guardian version

`)
}

func writeUpUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  guardian up management --revision <merged-main-commit-sha> [flags]

`)
}

func writeUpManagementUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  guardian up management --revision <merged-main-commit-sha> [flags]

Runs the repo Aspect bootstrap path:
  aspect infra bootstrap --revision <merged-main-commit-sha> ...

Flags:
`)
}
