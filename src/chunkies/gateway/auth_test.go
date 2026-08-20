package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTicketMintSharedKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("0123456789abcdef0123456789abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	left, err := newTicketMint(path)
	if err != nil {
		t.Fatal(err)
	}
	right, err := newTicketMint(path)
	if err != nil {
		t.Fatal(err)
	}
	want := ticket{Sub: "alice", Chunk: "chunk-main", Role: "player", Exp: time.Now().Add(time.Minute).Unix()}
	got, err := right.check(left.mint(want))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ticket = %+v, want %+v", got, want)
	}
}

func TestTicketMintRejectsShortKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("short"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := newTicketMint(path); err == nil {
		t.Fatal("short key accepted")
	}
}
