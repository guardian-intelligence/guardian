// Package pgtest runs a throwaway PostgreSQL server for one test: initdb
// into the test tmpdir, listen on a private unix socket (no TCP), and tear
// the whole thing down with the test. The server binaries are the
// Bazel-pinned archive behind //src/tools/postgres; PGTEST_INITDB points at
// its initdb and the rest of the tree is resolved relative to it.
package pgtest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Start launches a fresh single-tenant PostgreSQL and returns a DSN for its
// `postgres` database. The server dies with the test.
func Start(t testing.TB) string {
	t.Helper()
	initdb := os.Getenv("PGTEST_INITDB")
	if initdb == "" {
		t.Fatalf("pgtest: PGTEST_INITDB must point at the pinned initdb (set via the test target's env)")
	}
	if !filepath.IsAbs(initdb) {
		// $(rootpath) values are runfiles-root-relative; the test itself runs
		// chdir'd into its package directory, so resolve against the runfiles
		// tree rather than the cwd.
		initdb = filepath.Join(os.Getenv("TEST_SRCDIR"), os.Getenv("TEST_WORKSPACE"), initdb)
	}
	binDir := filepath.Dir(initdb)

	dataDir := filepath.Join(t.TempDir(), "data")
	// The socket directory must stay under sysconf's ~100-byte sun_path
	// limit, which Bazel's deep TEST_TMPDIR can exceed; the sandbox's
	// private /tmp keeps it short.
	sockDir, err := os.MkdirTemp("/tmp", "pgtest")
	if err != nil {
		t.Fatalf("pgtest: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })

	var initLog bytes.Buffer
	init := exec.Command(initdb, "-D", dataDir, "-U", "postgres", "-A", "trust",
		"--no-sync", "-E", "UTF8", "--locale=C")
	init.Stdout, init.Stderr = &initLog, &initLog
	if err := init.Run(); err != nil {
		t.Fatalf("pgtest: initdb: %v\n%s", err, initLog.String())
	}

	var serverLog bytes.Buffer
	server := exec.Command(filepath.Join(binDir, "postgres"),
		"-D", dataDir,
		"-k", sockDir,
		"-c", "listen_addresses=",
		// Durability is irrelevant for a process that dies with the test.
		"-F")
	server.Stdout, server.Stderr = &serverLog, &serverLog
	if err := server.Start(); err != nil {
		t.Fatalf("pgtest: starting postgres: %v", err)
	}
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
	})

	dsn := fmt.Sprintf("host=%s user=postgres dbname=postgres sslmode=disable", sockDir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			_ = conn.Close(ctx)
			return dsn
		}
		select {
		case <-ctx.Done():
			t.Fatalf("pgtest: postgres never became ready: %v\n%s", err, serverLog.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
