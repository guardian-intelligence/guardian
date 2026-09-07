//go:build linux

package guestd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/guardian-intelligence/guardian/src/postflight/hostd/guestproto"
	"golang.org/x/sys/unix"
)

// InitializeRestoredTurbo runs before Runner.Listener exists. Templates
// contain neither registration nor tenant state, and each clone receives a
// new VM generation ID, entropy, machine identity, DHCP identity, and clock.
func InitializeRestoredTurbo(ctx context.Context, prepare guestproto.Prepare, networkIdentityReady func()) error {
	seed, err := hex.DecodeString(prepare.Entropy)
	if err != nil || len(seed) != 32 || prepare.HostUnixNS <= 0 || prepare.MemberID == "" {
		return errors.New("invalid restored Turbo initialization")
	}
	mac, err := net.ParseMAC(prepare.MACAddress)
	if err != nil || len(mac) != 6 || mac[0]&1 != 0 {
		return errors.New("invalid restored Turbo MAC address")
	}
	defer clear(seed)
	random, err := os.OpenFile("/dev/urandom", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer random.Close()
	if _, err := random.Write(seed); err != nil {
		return err
	}
	// vmgenid already notifies the kernel on restore. Explicitly reseeding
	// after mixing host entropy also makes the credential boundary checkable.
	if err := unix.IoctlSetInt(int(random.Fd()), unix.RNDRESEEDCRNG, 0); err != nil {
		return fmt.Errorf("reseed restored guest: %w", err)
	}
	timeval := syscall.NsecToTimeval(prepare.HostUnixNS)
	if err := syscall.Settimeofday(&timeval); err != nil {
		return fmt.Errorf("resynchronize restored guest: %w", err)
	}
	digest := sha256.Sum256(seed)
	machineID := hex.EncodeToString(digest[:16])
	if err := os.WriteFile("/etc/machine-id", []byte(machineID+"\n"), 0o444); err != nil {
		return err
	}
	if err := syscall.Sethostname([]byte("pf-" + machineID[:20])); err != nil {
		return err
	}
	// QEMU migration restores virtio-net's MAC as well as the guest kernel's
	// network state. Destination argv alone does not change either. Stop the
	// DHCP client, change the actual guest NIC, and discard its old lease.
	interfaces, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return err
	}
	var nic string
	for _, entry := range interfaces {
		driver, err := os.Readlink(filepath.Join("/sys/class/net", entry.Name(), "device/driver"))
		if err != nil || filepath.Base(driver) != "virtio_net" {
			continue
		}
		if nic != "" {
			return errors.New("ambiguous restored Turbo network interface")
		}
		nic = entry.Name()
	}
	if nic == "" {
		return errors.New("restored Turbo virtio network interface is absent")
	}
	if err := exec.CommandContext(ctx, "systemctl", "stop", "systemd-networkd").Run(); err != nil {
		return err
	}
	for _, arguments := range [][]string{
		{"link", "set", "dev", nic, "down"},
		{"address", "flush", "dev", nic},
		{"link", "set", "dev", nic, "address", mac.String()},
		{"link", "set", "dev", nic, "up"},
	} {
		if err := exec.CommandContext(ctx, "/usr/sbin/ip", arguments...).Run(); err != nil {
			return fmt.Errorf("refresh restored interface: %w", err)
		}
	}
	if err := os.RemoveAll("/run/systemd/netif/leases"); err != nil {
		return err
	}
	networkIdentityReady()
	// Restarting re-derives the DUID from this VM's fresh machine identity.
	if err := exec.CommandContext(ctx, "systemctl", "restart", "systemd-networkd", "systemd-resolved").Run(); err != nil {
		return fmt.Errorf("refresh restored networking: %w", err)
	}
	if err := exec.CommandContext(ctx, "/usr/lib/systemd/systemd-networkd-wait-online", "--any", "--timeout=30").Run(); err != nil {
		return fmt.Errorf("restored network readiness: %w", err)
	}
	return nil
}
