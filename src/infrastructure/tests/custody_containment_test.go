package tests

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The custody bundle is a sealed disaster-recovery artifact: it holds the
// Talos genesis secrets, the LINSTOR master passphrase, and the OpenBao seal
// key, and every open widens the plaintext window on the material that
// rebuilds the whole estate.
//
// The failure mode this test exists to prevent is documentary, not technical.
// A restore ceremony written into a routine runbook reads to an operator — and
// far more readily to an agent — as an ordinary prerequisite step rather than
// an emergency, so custody drifts into daily circulation one copied snippet at
// a time. Reissuable third-party credentials belong in OpenBao, written
// through a namespace-scoped `secrets-writer` token; see docs/secrets.md.
//
// The ceremony therefore appears in bootstrap, disaster-recovery, and rotation
// runbooks only. Adding a file here means asserting it is one of those.
var custodyCeremonyDocs = map[string]bool{
	"src/infrastructure/runbooks/cold-boot-bootstrap.md":          true,
	"src/infrastructure/runbooks/custody.md":                      true,
	"src/infrastructure/runbooks/cert-rotation.md":                true,
	"src/infrastructure/runbooks/etcd-snapshot-restore.md":        true,
	"src/infrastructure/runbooks/openbao-static-seal-self-init.md": true,
	"src/infrastructure/runbooks/wiped-node-drill.md":             true,
}

// Invoking the restore, or reading the plaintext it lands in. The bare word
// "custody" stays legal everywhere: key custody and Transit custody are
// unrelated concepts that legitimately appear across the design docs.
var custodyCeremonyTokens = []string{
	"--action restore",
	"/dev/shm/guardian-custody",
}

func TestCustodyCeremonyConfinedToRecoveryRunbooks(t *testing.T) {
	roots := []string{"docs", "src/infrastructure/runbooks", "AGENTS.md"}

	scanned := 0
	for _, root := range roots {
		resolved := runfilePath(root)

		err := filepath.WalkDir(resolved, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".md") {
				return nil
			}

			rel := root
			if root != "AGENTS.md" {
				suffix, relErr := filepath.Rel(resolved, path)
				if relErr != nil {
					return relErr
				}
				rel = filepath.Join(root, suffix)
			}
			scanned++

			if custodyCeremonyDocs[rel] {
				return nil
			}

			body, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, token := range custodyCeremonyTokens {
				if strings.Contains(string(body), token) {
					t.Errorf("%s contains the custody restore ceremony (%q). "+
						"Custody opens for disaster recovery, cold boot, and CA/seal-key "+
						"rotation only. A reissuable secret is written straight to OpenBao "+
						"through a secrets-writer token — see docs/secrets.md, \"Adding a "+
						"secret for a third-party integration\". If this document really is "+
						"a recovery runbook, add it to custodyCeremonyDocs.", rel, token)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	// A silently empty walk would make this test vacuous.
	if scanned < 40 {
		t.Fatalf("scanned only %d markdown files; the doc data deps are incomplete", scanned)
	}
}

// The allowlist is a claim about which runbooks exist. A renamed or deleted
// runbook must not leave a stale entry behind that would silently re-permit
// the ceremony in a future file of the same name.
func TestCustodyCeremonyAllowlistIsLive(t *testing.T) {
	for doc := range custodyCeremonyDocs {
		if _, err := os.Stat(runfilePath(doc)); err != nil {
			t.Errorf("custodyCeremonyDocs lists %s, which does not exist: %v", doc, err)
		}
	}
}
