// postflight-hammer is the load harness for the Postflight runner: it
// dispatches real workflow_dispatch CI jobs in configurable patterns, watches
// them and the control-plane database to conclusion, and asserts that the
// system's books balance afterward while measuring every latency the design
// promised.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const usage = `postflight-hammer — load harness and standing health proof for the Postflight runner.

Subcommands:
  dispatch   Fire workflow_dispatch load in a pattern (burst, serial, sustained, churn, restart).
  watch      Poll GitHub and the control-plane database until every run and row is terminal.
  report     Join both sources, print the scoreboard, and write the JSON report.
  validate-rendezvous
             Validate an assignment-first JSONL trace, including step 6 zvol rendezvous.

Credentials:
  GitHub     GITHUB_TOKEN, or the ambient gh login (` + "`gh auth token`" + `). Needs actions:write
             on the target repo (dispatch/cancel/rerun) and actions:read (runs, jobs, logs).
  Database   DATABASE_URL pointing at the control-plane PostgreSQL. Optional: without it the
             GitHub-side assertions still run and every database assertion reports SKIP.
  API base   GITHUB_API_BASE_URL overrides https://api.github.com.

Prerequisites for a real battery:
  - control plane deployed with SCHEDULER_ENABLED=true and the hostd sync secret set
  - hostd synced on the tracer host (its slots visible in host_slots)
  - golden image templated on the host, demo repo workflow using postflight-checkout
  - a GitHub-hosted twin workflow with identical steps for the exec comparison (-twin-workflow)

Runbook:
  Smoke (5 dispatches):
    postflight-hammer dispatch -repo <org/repo> -workflow <file.yml> -pattern burst -n 5
    postflight-hammer watch    -repo <org/repo>
    postflight-hammer report
  Cold-to-warm benchmark (inputs are repeatable):
    postflight-hammer dispatch -repo <org/repo> -workflow <file.yml> -pattern serial -n 5 \
      -ref bench/cohort-1 \
      -input lane=postflight -input profile=smoke -input cache_epoch=cohort-1 \
      -await-promotion
  Full battery (>=50 across all patterns, one state file):
    dispatch -pattern burst -n 20, then -pattern sustained -n 15 -rate 10,
    then -pattern churn -n 10, then -pattern restart -n 5 (with HAMMER_RESTART_CMD set,
    e.g. "systemctl restart hostd"; the restart is skipped when unset), then watch, then report.
  The battery is re-runnable at will; it is the standing answer to "is the system
  still healthy after this change".

Reading the report:
  - Assertions print PASS/CONCERN/INVALID/SKIP. INVALID means the run did not produce
    trustworthy benchmark evidence and exits non-zero. CONCERN preserves valid raw data
    while flagging a performance or operational expectation that needs investigation.
    SKIP means the battery could not exercise the assertion (no DATABASE_URL, no churn
    cycles, no second green run of a scope) — a full battery should have zero skips.
  - The churn bound is hostd's current 30m ready/exited deadline; the recorded
    cancellation-propagation follow-up will tighten it, which changes one constant.
  - "Warm runs clone a generation" attests the durable-volume mechanism. A warm checkout
    slower than cold is a CONCERN, not a claim that durable volumes are unsound.
  - Orphan workspaces and VM state dirs are asserted through their database-visible
    proxy: slot occupancy returning to baseline. Host-disk-level leak detection lives
    in hostd's own GC and simulation tests.
  - Pickup segments marked * are watch observations at poll resolution; unmarked
    boundaries are authoritative timestamps from the ledger, the lease rows, and GitHub.
  - validate-rendezvous consumes the host/guest/runner JSONL evidence stream. The first
    six logical events are pool_ready, assignment_observed, job_hook_blocked,
    job_identity_reported, generation_resolved, and rendezvous_bound. Step 6 is the
    atomic zvol-to-QEMU binding; no customer volume may be present on pool_ready.

State: every subcommand shares one state file (-state, default ./postflight-hammer.json).
Patterns accumulate into it; delete it (or point elsewhere) to start a fresh battery.
Subcommands hold an exclusive lock on it, so run them one at a time.
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "postflight-hammer:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("a subcommand is required")
	}
	switch os.Args[1] {
	case "dispatch":
		return cmdDispatch(ctx, os.Args[2:])
	case "watch":
		return cmdWatch(ctx, os.Args[2:])
	case "report":
		return cmdReport(os.Args[2:])
	case "validate-rendezvous":
		return cmdValidateRendezvous(os.Args[2:])
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown subcommand %q", os.Args[1])
	}
}

func apiBase() string {
	if v := os.Getenv("GITHUB_API_BASE_URL"); v != "" {
		return v
	}
	return "https://api.github.com"
}

func cmdDispatch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dispatch", flag.ContinueOnError)
	cfg := dispatchConfig{
		restartCmd: os.Getenv("HAMMER_RESTART_CMD"),
		dbDSN:      os.Getenv("DATABASE_URL"),
		inputs:     workflowInputs{},
		twinInputs: workflowInputs{},
	}
	fs.StringVar(&cfg.repo, "repo", "guardian-intelligence/postflight-nextjs-demo", "target owner/repo")
	fs.StringVar(&cfg.workflow, "workflow", "", "workflow file name to dispatch (required)")
	fs.StringVar(&cfg.ref, "ref", "main", "git ref to dispatch against")
	fs.StringVar(&cfg.pattern, "pattern", "burst", "dispatch pattern: burst, serial, sustained, churn, restart")
	fs.IntVar(&cfg.n, "n", 5, "number of dispatches")
	fs.Float64Var(&cfg.ratePerMin, "rate", 10, "sustained pattern: dispatches per minute")
	fs.Var(cfg.inputs, "input", "workflow_dispatch input as key=value (repeatable)")
	fs.StringVar(&cfg.twinWorkflow, "twin-workflow", "", "GitHub-hosted twin workflow for the exec comparison")
	fs.IntVar(&cfg.twinN, "twin-n", 5, "twin dispatches (when -twin-workflow is set)")
	fs.Var(cfg.twinInputs, "twin-input", "twin workflow_dispatch input as key=value (repeatable)")
	fs.BoolVar(&cfg.awaitPromotion, "await-promotion", false, "serial pattern: wait for each Postflight seal to become the scope's current generation (requires DATABASE_URL)")
	fs.DurationVar(&cfg.churnMaxWait, "churn-max-wait", time.Minute, "churn pattern: cancel at a random point within this window")
	fs.StringVar(&cfg.statePath, "state", "postflight-hammer.json", "battery state file shared by all subcommands")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.workflow == "" {
		return fmt.Errorf("-workflow is required")
	}
	if cfg.n <= 0 {
		return fmt.Errorf("-n must be positive")
	}
	if cfg.awaitPromotion && cfg.pattern != "serial" {
		return fmt.Errorf("-await-promotion requires -pattern serial")
	}
	if cfg.awaitPromotion && cfg.dbDSN == "" {
		return fmt.Errorf("-await-promotion requires DATABASE_URL")
	}
	gh, err := newGHClient(apiBase(), os.Getenv("GITHUB_TOKEN"))
	if err != nil {
		return err
	}
	return runDispatch(ctx, gh, cfg)
}

func cmdWatch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	cfg := watchConfig{dbDSN: os.Getenv("DATABASE_URL")}
	fs.StringVar(&cfg.repo, "repo", "guardian-intelligence/postflight-nextjs-demo", "target owner/repo")
	fs.StringVar(&cfg.statePath, "state", "postflight-hammer.json", "battery state file shared by all subcommands")
	fs.DurationVar(&cfg.poll, "poll", 10*time.Second, "poll interval")
	fs.DurationVar(&cfg.timeout, "timeout", 45*time.Minute, "give up after this long")
	fs.DurationVar(&cfg.settle, "settle", time.Minute, "wait after the last run turns terminal before the final snapshot")
	if err := fs.Parse(args); err != nil {
		return err
	}
	gh, err := newGHClient(apiBase(), os.Getenv("GITHUB_TOKEN"))
	if err != nil {
		return err
	}
	return runWatch(ctx, gh, cfg)
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	var statePath, jsonPath string
	fs.StringVar(&statePath, "state", "postflight-hammer.json", "battery state file shared by all subcommands")
	fs.StringVar(&jsonPath, "json", "", "JSON report output path (default <state>.report.json)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if jsonPath == "" {
		jsonPath = statePath + ".report.json"
	}
	return runReport(statePath, jsonPath)
}
