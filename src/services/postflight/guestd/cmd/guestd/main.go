// guestd is the agent inside every runner VM: a vsock listener speaking
// guestproto, mount convergence, runner execution, quiesce. Configuration
// arrives over vsock, never over network or metadata; the binary takes no
// arguments.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/guardian-intelligence/guardian/src/services/postflight/guestd"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vsock"
)

func main() {
	binary, err := os.Executable()
	if os.Geteuid() != os.Getuid() && (err != nil || filepath.Clean(binary) != guestd.RunnerWorkerWrapper) {
		_, _ = fmt.Fprintln(os.Stderr, "guestd: privileged execution is restricted to the Runner.Worker trampoline")
		os.Exit(1)
	}
	if err == nil && filepath.Clean(binary) == guestd.RunnerWorkerWrapper {
		if err := guestd.RunRunnerWorkerExec(os.Args); err != nil {
			guestd.ReportRunnerWorkerFailure(err)
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if guestd.IsRunnerAssigned(os.Args) {
		if err := guestd.RunRunnerAssigned(os.Args); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if guestd.IsValidateAssignment(os.Args) {
		if err := guestd.RunValidateAssignment(os.Args); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if guestd.IsCapsuleEnter(os.Args) {
		if err := guestd.RunCapsuleEnter(os.Args); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("guestd exiting", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	encryption, err := guestd.LoadEncryptionMode(guestd.EncryptionModePath)
	if err != nil {
		return err
	}
	capsules := &guestd.CapsuleManager{
		BinaryPath: binary,
		InitPath:   "/usr/bin/tini",
		SleepPath:  "/usr/bin/sleep",
		RunnerRoot: guestd.RunnerRoot,
		CgroupPath: "/sys/fs/cgroup/postflight/capsule",
	}
	checkpoints := &guestd.ProcessCheckpoints{
		Capsules:   capsules,
		ImagesRoot: guestd.ProcessMountpoint,
		CRIU: guestd.CRIU{
			Path: "/usr/sbin/criu", ImagesRoot: guestd.ProcessMountpoint,
			WorkRoot:   guestd.ProcessMountpoint + "/work",
			RestoreRun: guestd.RunRestorePrivateInCgroup(capsules.CgroupPath),
		},
	}
	runner, err := user.Lookup("runner")
	if err != nil {
		return fmt.Errorf("lookup runner account: %w", err)
	}
	runnerGID, err := strconv.Atoi(runner.Gid)
	if err != nil {
		return fmt.Errorf("parse runner gid: %w", err)
	}
	server, err := guestd.New(guestd.Config{
		System:               guestd.RealSystem{},
		RunRunner:            guestd.ExecRunner(guestd.RunnerRoot, "runner", logger),
		Checkpoints:          checkpoints,
		Encryption:           encryption,
		AssignmentSocketMode: 0o660,
		AssignmentSocketGID:  runnerGID,
		Logger:               logger,
	})
	if err != nil {
		return err
	}
	logger.Info("workspace encryption", "mode", string(encryption))
	listener, err := vsock.Listen(vsock.Any, guestproto.VsockPort)
	if err != nil {
		return err
	}
	logger.Info("guestd listening", "addr", listener.Addr().String())

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- server.Serve(runCtx, listener) }()
	go func() { errs <- server.ServeAssignments(runCtx, guestd.AssignmentSocketPath) }()
	err = <-errs
	cancel()
	if err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
