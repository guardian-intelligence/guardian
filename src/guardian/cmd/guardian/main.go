package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/guardian-intelligence/guardian/src/guardian/internal/config"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/output"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/toolrunner"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/up"
)

var errUsage = errors.New("usage")

const usage = `usage:
  guardian up <cluster.cue> [--execute] [--output text|json|yaml|toml]

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
	configPath, execute, format, err := parseUpArgs(args)
	if err != nil {
		if errors.Is(err, errHelp) {
			fmt.Println(usage)
			return nil
		}
		return err
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		res := up.Result{Outcome: "NeedsConfig", Reason: err.Error()}
		_ = output.Write(os.Stdout, res, format)
		return fmt.Errorf("up: %w", err)
	}
	tools, err := resolveTools()
	if err != nil {
		return err
	}
	res := up.Run(context.Background(), loaded, tools, toolrunner.RealRunner{}, up.Options{Execute: execute})
	if err := output.Write(os.Stdout, res, format); err != nil {
		return err
	}
	if res.Outcome == "Converged" || res.Outcome == "Planned" {
		return nil
	}
	return fmt.Errorf("up: %s: %s", res.Outcome, res.Reason)
}

var errHelp = errors.New("help")

func parseUpArgs(args []string) (configPath string, execute bool, format string, err error) {
	format = "text"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return "", false, "", errHelp
		case arg == "--execute":
			execute = true
		case arg == "--output":
			i++
			if i >= len(args) {
				return "", false, "", fmt.Errorf("up: %w: --output requires a value", errUsage)
			}
			format = args[i]
		case strings.HasPrefix(arg, "--output="):
			format = strings.TrimPrefix(arg, "--output=")
		case strings.HasPrefix(arg, "-"):
			return "", false, "", fmt.Errorf("up: %w: unknown flag %q", errUsage, arg)
		default:
			if configPath != "" {
				return "", false, "", fmt.Errorf("up: %w: expected one cluster CUE config path", errUsage)
			}
			configPath = arg
		}
	}
	switch format {
	case "text", "json", "yaml", "toml":
	default:
		return "", false, "", fmt.Errorf("up: %w: unsupported --output %q", errUsage, format)
	}
	if configPath == "" {
		return "", false, "", fmt.Errorf("up: %w: expected one cluster CUE config path", errUsage)
	}
	return configPath, execute, format, nil
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
