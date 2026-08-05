package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestExitStatus(t *testing.T) {
	if got := exitStatus(nil); got != 0 {
		t.Fatalf("success status = %d, want 0", got)
	}
	command := exec.Command("sh", "-c", "exit 7")
	if got := exitStatus(command.Run()); got != 7 {
		t.Fatalf("failed command status = %d, want 7", got)
	}
}

type fixtureClient struct {
	address      string
	hostKey      ssh.PublicKey
	clientSigner ssh.Signer
	serverResult <-chan error
	stdinDigest  string
}

func startFixture(t *testing.T, expectedCommand string) fixtureClient {
	t.Helper()
	directory := t.TempDir()
	hostPublic, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = hostPublic
	clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientPrivate)
	if err != nil {
		t.Fatal(err)
	}
	hostBlock, err := ssh.MarshalPrivateKey(hostPrivate, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	hostKeyPath := filepath.Join(directory, "host")
	if err := os.WriteFile(hostKeyPath, pem.EncodeToMemory(hostBlock), 0o600); err != nil {
		t.Fatal(err)
	}
	clientKey, err := ssh.NewPublicKey(clientPublic)
	if err != nil {
		t.Fatal(err)
	}
	authorizedPath := filepath.Join(directory, "authorized_keys")
	if err := os.WriteFile(authorizedPath, ssh.MarshalAuthorizedKey(clientKey), 0o600); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(directory, "ready")
	result := make(chan error, 1)
	go func() {
		result <- serveOnce(options{
			hostKeyPath:       hostKeyPath,
			authorizedKeyPath: authorizedPath,
			readyPath:         readyPath,
			expectedCommand:   expectedCommand,
			stdinSHA256Path:   filepath.Join(directory, "stdin.sha256"),
		})
	}()

	var address []byte
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		address, err = os.ReadFile(readyPath)
		if err == nil && len(address) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(address) == 0 {
		t.Fatal("fixture did not publish a listener address")
	}
	return fixtureClient{
		address:      strings.TrimSpace(string(address)),
		hostKey:      hostSigner.PublicKey(),
		clientSigner: clientSigner,
		serverResult: result,
		stdinDigest:  filepath.Join(directory, "stdin.sha256"),
	}
}

func (fixture fixtureClient) dial(t *testing.T, signer ssh.Signer) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", fixture.address, &ssh.ClientConfig{
		User:            "canary",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(fixture.hostKey),
		Timeout:         2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestFixtureAcceptsOnlyTheExactCommandAndRelaysExitStatus(t *testing.T) {
	const command = "sh -s -- '/'"
	const stdin = "printf 'relayed\\n'; exit 7\n"
	fixture := startFixture(t, command)
	host, _, err := net.SplitHostPort(fixture.address)
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("fixture address = %q, want an IPv4 loopback listener: %v", fixture.address, err)
	}
	client := fixture.dial(t, fixture.clientSigner)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	session.Stdin = strings.NewReader(stdin)
	output, err := session.CombinedOutput(command)
	if !bytes.Equal(output, []byte("relayed\n")) {
		t.Fatalf("relayed output = %q", output)
	}
	var exitError *ssh.ExitError
	if !errors.As(err, &exitError) || exitError.ExitStatus() != 7 {
		t.Fatalf("session error = %v, want SSH exit 7", err)
	}
	client.Close()
	if err := <-fixture.serverResult; err != nil {
		t.Fatal(err)
	}
	wantDigest := fmt.Sprintf("%x\n", sha256.Sum256([]byte(stdin)))
	gotDigest, err := os.ReadFile(fixture.stdinDigest)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotDigest) != wantDigest {
		t.Fatalf("integrated stdin digest = %q, want %q", gotDigest, wantDigest)
	}
}

func TestFixtureRejectsAnUnauthorizedPublicKey(t *testing.T) {
	fixture := startFixture(t, "sh -s -- '/allowed'")
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongSigner, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	client, err := ssh.Dial("tcp", fixture.address, &ssh.ClientConfig{
		User:            "canary",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(wrongSigner)},
		HostKeyCallback: ssh.FixedHostKey(fixture.hostKey),
		Timeout:         2 * time.Second,
	})
	if err == nil {
		client.Close()
		t.Fatal("fixture accepted an unauthorized public key")
	}
	if serverErr := <-fixture.serverResult; serverErr == nil {
		t.Fatal("fixture server reported success after rejecting authentication")
	}
}

func TestFixtureRejectsAnUnexpectedCommand(t *testing.T) {
	fixture := startFixture(t, "sh -s -- '/allowed'")
	client := fixture.dial(t, fixture.clientSigner)
	session, err := client.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Run("sh -s -- '/different'"); err == nil {
		t.Fatal("fixture accepted an unexpected command")
	}
	client.Close()
	if err := <-fixture.serverResult; err != nil {
		t.Fatal(err)
	}
}

func TestWritesTheExactStdinDigest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stdin.sha256")
	digest := sha256.Sum256([]byte("fixed probe\n"))
	if err := writeStdinSHA256(path, digest[:]); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "7431f90674c2c19563e53054c8f621c4344a81b13dc48b47a46a8549a88d709b\n" {
		t.Fatalf("digest file = %q", got)
	}
}

func TestOptionsRequireAnExactCommandAndEphemeralKeys(t *testing.T) {
	if err := serveOnce(options{}); err == nil {
		t.Fatal("empty fixture options unexpectedly started an SSH listener")
	}
}
