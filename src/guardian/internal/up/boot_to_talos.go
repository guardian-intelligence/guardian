package up

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/guardian-intelligence/guardian/src/guardian/internal/config"
	"golang.org/x/crypto/ssh"
)

const remoteBootToTalosPath = "/dev/shm/guardian-boot-to-talos"

func runBootToTalosOnTarget(ctx context.Context, cfg config.Config, localBootToTalos string) error {
	client, err := dialBootstrapSSH(ctx, cfg.Node.Address)
	if err != nil {
		return err
	}
	defer client.Close()

	binary, err := os.ReadFile(localBootToTalos)
	if err != nil {
		return fmt.Errorf("read boot-to-talos: %w", err)
	}
	if _, err := runSSH(ctx, client, "umask 077 && cat > "+remoteBootToTalosPath+" && chmod 0755 "+remoteBootToTalosPath, binary); err != nil {
		return fmt.Errorf("upload boot-to-talos: %w", err)
	}
	cmd := shellJoin(append([]string{
		"sudo",
		"-n",
		"env",
		"TMPDIR=/dev/shm",
		remoteBootToTalosPath,
	}, bootToTalosArgs(cfg)...)...)
	output, err := runSSH(ctx, client, cmd, nil)
	if err != nil && !looksLikeRebootDuringBootToTalos(output) {
		return fmt.Errorf("run boot-to-talos: %w", err)
	}
	return nil
}

func resolveRemoteDiskBySerial(ctx context.Context, client *ssh.Client, serial string) (string, error) {
	cmd := shellJoin("sh", "-c", `serial="$1"; lsblk -dn -o NAME,SERIAL | awk -v serial="$serial" '$2 == serial { print "/dev/" $1; found=1; exit } END { if (!found) exit 1 }'`, "guardian-resolve-disk", serial)
	output, err := runSSH(ctx, client, cmd, nil)
	if err != nil {
		return "", fmt.Errorf("resolve install disk serial %s: %w", serial, err)
	}
	disk := strings.TrimSpace(string(output))
	if disk == "" || !strings.HasPrefix(disk, "/dev/") {
		return "", fmt.Errorf("resolve install disk serial %s: got %q", serial, disk)
	}
	return disk, nil
}

func dialBootstrapSSH(ctx context.Context, address string) (*ssh.Client, error) {
	keyPath, err := expandHome(envDefault("GUARDIAN_BOOTSTRAP_SSH_KEY", "~/.ssh/id_ed25519"))
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("read ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key: %w", err)
	}
	sshConfig := &ssh.ClientConfig{
		User:            envDefault("GUARDIAN_BOOTSTRAP_SSH_USER", "ubuntu"),
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	networkAddress := net.JoinHostPort(address, envDefault("GUARDIAN_BOOTSTRAP_SSH_PORT", "22"))
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", networkAddress)
	if err != nil {
		return nil, fmt.Errorf("dial ssh: %w", err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, networkAddress, sshConfig)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ssh handshake: %w", err)
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

func runSSH(ctx context.Context, client *ssh.Client, command string, stdin []byte) ([]byte, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("new ssh session: %w", err)
	}
	defer session.Close()
	if stdin != nil {
		session.Stdin = bytes.NewReader(stdin)
	}
	done := make(chan struct {
		output []byte
		err    error
	}, 1)
	go func() {
		output, err := session.CombinedOutput(command)
		done <- struct {
			output []byte
			err    error
		}{output: output, err: err}
	}()
	select {
	case result := <-done:
		return result.output, result.err
	case <-ctx.Done():
		_ = session.Close()
		return nil, ctx.Err()
	}
}

func looksLikeRebootDuringBootToTalos(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "reboot") ||
		strings.Contains(text, "kexec") ||
		strings.Contains(text, "installation image copied")
}

func envDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func expandHome(path string) (string, error) {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return home, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func shellJoin(args ...string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !(r == '/' || r == ':' || r == '.' || r == '-' || r == '_' || r == '=' ||
			(r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z'))
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
