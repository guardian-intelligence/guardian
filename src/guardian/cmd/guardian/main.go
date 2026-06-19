package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/guardian-intelligence/guardian/src/guardian/internal/config"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/output"
	statusview "github.com/guardian-intelligence/guardian/src/guardian/internal/status"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/toolrunner"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/up"
)

var errUsage = errors.New("usage")

const usage = `usage:
  guardian up <cluster.cue> [--execute] [--genesis-age-recipient age1...] [--output text|json|yaml|toml] [--status auto|tui|plain|off]

guardian owns only host come-up: Talos via Talm, Kubernetes bootstrap,
Cozystack platform install, and a default hello-world handoff marker.`

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
	loaded, err := config.Load(parsed.ConfigPath)
	if err != nil {
		res := up.Result{Outcome: "NeedsConfig", Reason: err.Error()}
		_ = output.Write(os.Stdout, res, parsed.Format)
		return fmt.Errorf("up: %w", err)
	}
	tools, err := resolveTools()
	if err != nil {
		return err
	}
	statusReporter, closeStatus, err := newStatusReporter(parsed, loaded.Config.Cluster.Name)
	if err != nil {
		return err
	}
	defer closeStatus()
	res := up.Run(context.Background(), loaded, tools, toolrunner.RealRunner{}, up.Options{
		Execute:              parsed.Execute,
		GenesisAgeRecipients: parsed.GenesisAgeRecipients,
		Status:               statusReporter,
	})
	if err := output.Write(os.Stdout, res, parsed.Format); err != nil {
		return err
	}
	if res.Outcome == "Converged" || res.Outcome == "Planned" {
		return nil
	}
	return fmt.Errorf("up: %s: %s", res.Outcome, res.Reason)
}

var errHelp = errors.New("help")

type upArgs struct {
	ConfigPath           string
	Execute              bool
	Format               string
	Status               string
	GenesisAgeRecipients []string
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
		case arg == "--genesis-age-recipient":
			i++
			if i >= len(args) {
				return upArgs{}, fmt.Errorf("up: %w: --genesis-age-recipient requires a value", errUsage)
			}
			parsed.GenesisAgeRecipients = append(parsed.GenesisAgeRecipients, args[i])
		case strings.HasPrefix(arg, "--genesis-age-recipient="):
			parsed.GenesisAgeRecipients = append(parsed.GenesisAgeRecipients, strings.TrimPrefix(arg, "--genesis-age-recipient="))
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
			if parsed.ConfigPath != "" {
				return upArgs{}, fmt.Errorf("up: %w: expected one cluster CUE config path", errUsage)
			}
			parsed.ConfigPath = arg
		}
	}
	if len(parsed.GenesisAgeRecipients) == 0 {
		parsed.GenesisAgeRecipients = splitEnvList(os.Getenv("GUARDIAN_GENESIS_AGE_RECIPIENTS"))
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
	if parsed.ConfigPath == "" {
		return upArgs{}, fmt.Errorf("up: %w: expected one cluster CUE config path", errUsage)
	}
	return parsed, nil
}

func newStatusReporter(parsed upArgs, clusterName string) (up.StatusReporter, func() error, error) {
	if !parsed.Execute || parsed.Status == "off" {
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
	renderer := statusview.New(os.Stderr, statusview.Options{
		Mode:        statusview.Mode(mode),
		ClusterName: clusterName,
	})
	return renderer, renderer.Close, nil
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func splitEnvList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	var out []string
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			out = append(out, field)
		}
	}
	return out
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
	return up.Tools{Talm: talm, Talos: talos, Kubectl: kubectl, Helm: helm}, nil
}
