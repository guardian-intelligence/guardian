// guestd is the agent inside every runner VM: a vsock listener speaking
// guestproto, mount convergence, runner execution, quiesce. Configuration
// arrives over vsock, never over network or metadata; the binary takes no
// arguments.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/guardian-intelligence/guardian/src/services/postflight/guestd"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/guestproto"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vsock"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("guestd exiting", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	encryption, err := guestd.LoadEncryptionMode(guestd.EncryptionModePath)
	if err != nil {
		return err
	}
	server, err := guestd.New(guestd.Config{
		System:     guestd.RealSystem{},
		RunRunner:  guestd.ExecRunner(guestd.RunnerRoot, "runner", logger),
		Encryption: encryption,
		Logger:     logger,
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
	if err := server.Serve(ctx, listener); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}
