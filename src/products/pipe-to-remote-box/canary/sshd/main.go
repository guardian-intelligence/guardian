// Command sshd is a credentialless, one-connection SSH fixture for the Pipe to
// Remote Box release canary. It is test infrastructure, not part of the
// published product. The caller supplies ephemeral keys and the one exact
// command the shipped CLI is allowed to request.
package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"sync"

	"golang.org/x/crypto/ssh"
)

type options struct {
	hostKeyPath       string
	authorizedKeyPath string
	readyPath         string
	expectedCommand   string
	stdinSHA256Path   string
	hang              bool
}

func main() {
	var opts options
	flag.StringVar(&opts.hostKeyPath, "host-key", "", "ephemeral SSH host private key")
	flag.StringVar(&opts.authorizedKeyPath, "authorized-key", "", "single ephemeral authorized public key")
	flag.StringVar(&opts.readyPath, "ready-file", "", "file in which to write the loopback listener address")
	flag.StringVar(&opts.expectedCommand, "expected-command", "", "only SSH exec command the fixture accepts")
	flag.StringVar(&opts.stdinSHA256Path, "stdin-sha256-file", "", "optional file receiving the exact request-stdin SHA-256")
	flag.BoolVar(&opts.hang, "hang", false, "consume the request without completing it")
	flag.Parse()

	if err := serveOnce(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func serveOnce(opts options) error {
	if opts.hostKeyPath == "" || opts.authorizedKeyPath == "" || opts.readyPath == "" || opts.expectedCommand == "" {
		return errors.New("--host-key, --authorized-key, --ready-file and --expected-command are required")
	}
	hostKey, err := os.ReadFile(opts.hostKeyPath)
	if err != nil {
		return fmt.Errorf("read host key: %w", err)
	}
	hostSigner, err := ssh.ParsePrivateKey(hostKey)
	if err != nil {
		return fmt.Errorf("parse host key: %w", err)
	}
	authorizedLine, err := os.ReadFile(opts.authorizedKeyPath)
	if err != nil {
		return fmt.Errorf("read authorized key: %w", err)
	}
	authorizedKey, _, _, _, err := ssh.ParseAuthorizedKey(authorizedLine)
	if err != nil {
		return fmt.Errorf("parse authorized key: %w", err)
	}

	config := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if subtle.ConstantTimeCompare(key.Marshal(), authorizedKey.Marshal()) != 1 {
				return nil, errors.New("public key is not authorized")
			}
			return nil, nil
		},
	}
	config.AddHostKey(hostSigner)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen on loopback: %w", err)
	}
	defer listener.Close()
	if err := os.WriteFile(opts.readyPath, []byte(listener.Addr().String()+"\n"), 0o600); err != nil {
		return fmt.Errorf("write ready file: %w", err)
	}

	connection, err := listener.Accept()
	if err != nil {
		return fmt.Errorf("accept connection: %w", err)
	}
	defer connection.Close()
	server, channels, requests, err := ssh.NewServerConn(connection, config)
	if err != nil {
		return fmt.Errorf("perform SSH handshake: %w", err)
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)

	var workers sync.WaitGroup
	for channel := range channels {
		workers.Add(1)
		go func() {
			defer workers.Done()
			handleChannel(channel, opts)
		}()
	}
	workers.Wait()
	return nil
}

func handleChannel(newChannel ssh.NewChannel, opts options) {
	if newChannel.ChannelType() != "session" {
		_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels are supported")
		return
	}
	channel, requests, err := newChannel.Accept()
	if err != nil {
		return
	}
	defer channel.Close()

	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil || payload.Command != opts.expectedCommand {
			_ = request.Reply(false, nil)
			return
		}
		if err := request.Reply(true, nil); err != nil {
			return
		}
		if opts.hang {
			hash := sha256.New()
			_, _ = io.Copy(hash, channel)
			_ = writeStdinSHA256(opts.stdinSHA256Path, hash.Sum(nil))
			select {}
		}

		hash := sha256.New()
		command := exec.Command("sh", "-c", payload.Command)
		command.Stdin = io.TeeReader(channel, hash)
		command.Stdout = channel
		command.Stderr = channel.Stderr()
		err := command.Run()
		if hashErr := writeStdinSHA256(opts.stdinSHA256Path, hash.Sum(nil)); hashErr != nil && err == nil {
			err = hashErr
		}
		status := exitStatus(err)
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
		return
	}
}

func writeStdinSHA256(path string, digest []byte) error {
	if path == "" {
		return nil
	}
	return os.WriteFile(path, []byte(hex.EncodeToString(digest)+"\n"), 0o600)
}

func exitStatus(err error) uint32 {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() >= 0 {
		return uint32(exitError.ExitCode())
	}
	return 255
}
