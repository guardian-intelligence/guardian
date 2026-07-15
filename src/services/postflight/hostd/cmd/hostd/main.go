// hostd is the per-host runner daemon: the sync/converge agent over real
// substrate — zfs zvols, QEMU/KVM VMs in transient systemd scopes, the
// vsock guestd channel — plus the host-local checkout-bundle endpoint backed
// by the live lease table.
//
// Configuration is environment-only:
//
//	HOSTD_HOST_ID                 this host's identity with the control plane
//	HOSTD_SYNC_URL                control-plane origin, e.g. https://guardianintelligence.org
//	HOSTD_SYNC_SECRET             bearer credential for the sync exchange
//	HOSTD_HOST_SECRET_FILE        >=32 random bytes keying checkout tokens; per host, never shared
//	HOSTD_STATE_DIR               root for VM state dirs and the checkout store, e.g. /var/lib/postflight
//	HOSTD_POOL                    hostd-managed dataset subtree, e.g. tank/postflight
//	HOSTD_CLASS                   runner class this host serves, e.g. postflight-4cpu-ubuntu-2404
//	HOSTD_IMAGE_ID                golden image id; root disks clone <pool>/images/<id>@golden
//	HOSTD_SLOTS                   per-class slot count (default 4; warm VM = slot, no overcommit)
//	HOSTD_CPUS                    vCPUs per VM (default 4)
//	HOSTD_MEMORY_MIB              memory per VM (default 16384)
//	HOSTD_QEMU_PATH               QEMU binary (default /usr/bin/qemu-system-x86_64)
//	HOSTD_SYNC_INTERVAL           sync cadence when the control plane does not suggest one (default 2s)
//	HOSTD_GUEST_NETWORK           guest egress datapath: none (default) or user. user is the
//	                              tracer-only libslirp datapath: unrestricted outbound from the
//	                              host's network position, and the guest reaches every host
//	                              loopback service via the 10.0.2.2 gateway. The production lane
//	                              is a filtered bridge; do not expose untrusted guests under user.
//	HOSTD_CHECKOUT_LISTEN_ADDR    checkout endpoint bind (default 127.0.0.1:8480). It carries
//	                              tenant GitHub tokens over plaintext HTTP; under GUEST_NETWORK=user
//	                              the loopback bind is itself guest-reachable (via 10.0.2.2), which
//	                              is how the guest checkout action reaches it.
//	HOSTD_CHECKOUT_GUEST_ORIGIN   the same endpoint as guests reach it, e.g. http://10.0.2.2:8480
//	                              (user datapath) or the bridge address http://10.77.0.1:8480
//
// Install (one-time, per plain-Ubuntu runner host):
//
//	install -m 0755 hostd /usr/local/bin/hostd
//	install -d -m 0700 /etc/postflight
//	head -c 64 /dev/urandom > /etc/postflight/host-secret && chmod 0600 /etc/postflight/host-secret
//	install -m 0600 hostd.env /etc/postflight/hostd.env    # HOSTD_* lines, including the sync secret
//	install -m 0644 hostd.service /etc/systemd/system/hostd.service
//	systemctl daemon-reload && systemctl enable --now hostd
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/agent"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/checkoutbundle"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/vm"
	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("hostd exiting", "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	hostSecret, err := os.ReadFile(cfg.hostSecretFile)
	if err != nil {
		return fmt.Errorf("read host secret: %w", err)
	}

	class := vm.Class(cfg.class)
	image := cfg.pool + "/images/" + cfg.imageID + "@golden"
	vms, err := vm.NewQEMU(vm.Config{
		StateRoot:    filepath.Join(cfg.stateDir, "vm"),
		QEMUPath:     cfg.qemuPath,
		DatasetRoot:  cfg.pool,
		Classes:      map[vm.Class]vm.ClassConfig{class: {CPUs: cfg.cpus, MemoryMiB: cfg.memoryMiB, Image: image}},
		Launcher:     vm.NewSystemdLauncher(),
		Guest:        vm.NewVsockGuest(),
		GuestNetwork: cfg.guestNetwork,
		Logger:       logger,
	})
	if err != nil {
		return err
	}

	instance, err := agent.New(agent.Config{
		HostID:              cfg.hostID,
		ControlPlaneOrigin:  cfg.syncURL,
		Slots:               map[vm.Class]int{class: cfg.slots},
		SyncInterval:        cfg.syncInterval,
		CheckoutGuestOrigin: cfg.checkoutGuestOrigin,
	}, &zvol.Exec{Root: cfg.pool}, vms, cfg.syncSecret, hostSecret, agent.Options{Logger: logger})
	if err != nil {
		return err
	}

	checkout := checkoutbundle.New(checkoutbundle.Config{
		StoreDir:   filepath.Join(cfg.stateDir, "checkout"),
		HostSecret: hostSecret,
		Logger:     logger,
	}, instance)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	agentCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go checkout.RunReaper(agentCtx, 0)

	// No WriteTimeout: pack downloads legitimately run long. ReadTimeout is
	// safe — request bodies are small and read up front — and closes the
	// slow-drip hold-open vector from untrusted guests.
	server := &http.Server{
		Addr:              cfg.checkoutListenAddr,
		Handler:           checkout.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	failed := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			failed <- fmt.Errorf("checkout endpoint: %w", err)
		}
	}()

	agentDone := make(chan error, 1)
	go func() { agentDone <- instance.Run(agentCtx) }()

	logger.Info("hostd running",
		"host", cfg.hostID, "class", cfg.class, "slots", cfg.slots,
		"pool", cfg.pool, "image", image, "checkout_addr", cfg.checkoutListenAddr)

	var exitErr error
	select {
	case exitErr = <-failed:
		cancel()
		<-agentDone
	case err := <-agentDone:
		// Run only returns when its context ends; anything else is fatal.
		if agentCtx.Err() == nil {
			exitErr = err
		}
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	_ = server.Shutdown(shutdownCtx)
	return exitErr
}
