package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func runCreateCmd(args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	yes := fs.Bool("yes", false, "allow server allocation and destructive replacement of non-converged stock provider OS")
	output := fs.String("output", "text", "output format: text or json")
	tokenEnv := fs.String("latitude-token-env", "LATITUDE_API_KEY", "environment variable containing the Latitude API token")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			fmt.Println(usage)
			return nil
		}
		return fmt.Errorf("create: %w: %v", errUsage, err)
	}
	if *output != "text" && *output != "json" {
		return fmt.Errorf("create: %w: --output must be text or json, got %q", errUsage, *output)
	}
	if fs.NArg() != 1 {
		result := createResult{Outcome: createOutcomeNeedsConfig, Reason: "expected one create config path"}
		printCreateResult(os.Stdout, result, *output)
		return fmt.Errorf("create: %w: expected one create config path", errUsage)
	}
	cfg, err := loadCreateConfig(fs.Arg(0))
	if err != nil {
		result := createResult{Outcome: createOutcomeNeedsConfig, Reason: err.Error()}
		printCreateResult(os.Stdout, result, *output)
		return fmt.Errorf("create: %w", err)
	}
	token := strings.TrimSpace(os.Getenv(*tokenEnv))
	if token == "" {
		result := createResult{Outcome: createOutcomeNeedsConfig, Reason: fmt.Sprintf("%s is not set", *tokenEnv)}
		printCreateResult(os.Stdout, result, *output)
		return fmt.Errorf("create: %w: %s is not set", errUsage, *tokenEnv)
	}
	deps, err := createDepsForCLI(newSecretString(token))
	if err != nil {
		return err
	}
	result := runCreate(context.Background(), createRunInput{
		Config: *cfg,
		Options: createOptions{
			Approved: *yes,
			Output:   *output,
		},
	}, deps)
	printCreateResult(os.Stdout, result, *output)
	switch result.Outcome {
	case createOutcomeConverged:
		return nil
	case createOutcomeNeedsConfig:
		return fmt.Errorf("create: %w: %s", errUsage, result.Reason)
	default:
		return fmt.Errorf("create: %s: %s", result.Outcome, result.Reason)
	}
}

func printCreateResult(w io.Writer, result createResult, format string) {
	if format == "json" {
		raw, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Fprintf(w, `{"outcome":"%s","reason":"json encode failed: %s"}`+"\n", createOutcomeRetryable, err)
			return
		}
		fmt.Fprintln(w, string(raw))
		return
	}
	fmt.Fprintf(w, "outcome\t%s\n", result.Outcome)
	if result.Stage != "" {
		fmt.Fprintf(w, "stage\t%s\n", result.Stage)
	}
	if result.Reason != "" {
		fmt.Fprintf(w, "reason\t%s\n", result.Reason)
	}
	if result.Operation != nil {
		fmt.Fprintf(w, "operation\t%s\n", result.Operation.OperationID)
		if result.Operation.ServerID != "" {
			fmt.Fprintf(w, "server\t%s\n", result.Operation.ServerID)
		}
	}
	if result.Kubeconfig != "" {
		fmt.Fprintf(w, "kubeconfig\t%s\n", result.Kubeconfig)
	}
	if result.Plan != nil {
		fmt.Fprintf(w, "plan.cluster\t%s\n", result.Plan.ClusterName)
		fmt.Fprintf(w, "plan.provider\t%s\n", result.Plan.Provider)
		fmt.Fprintf(w, "plan.specDigest\t%s\n", result.Plan.SpecDigest)
		if result.Plan.ServerID != "" {
			fmt.Fprintf(w, "plan.server\t%s\n", result.Plan.ServerID)
		}
		if result.Plan.Create != nil {
			fmt.Fprintf(w, "plan.create.project\t%s\n", result.Plan.Create.Project)
			fmt.Fprintf(w, "plan.create.hostname\t%s\n", result.Plan.Create.Hostname)
			fmt.Fprintf(w, "plan.create.metro\t%s\n", result.Plan.Create.Metro)
			fmt.Fprintf(w, "plan.create.plan\t%s\n", result.Plan.Create.Plan)
		}
	}
	if result.Plan != nil && len(result.Plan.Mutations) > 0 {
		fmt.Fprintln(w, "plan.mutations")
		for _, mutation := range result.Plan.Mutations {
			fmt.Fprintf(w, "- %s\n", mutation)
		}
		if result.Plan.ApprovalRerunHint != "" {
			fmt.Fprintf(w, "approval\t%s\n", result.Plan.ApprovalRerunHint)
		}
	}
	for _, diag := range result.Diagnostics {
		fmt.Fprintf(w, "diagnostic\t%s\t%s\n", diag.Subsystem, diag.Message)
	}
}
