package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// baoLocalPort is the loopback port the OpenBao port-forward binds; chosen to
// not collide with the seed-registry forward (pushLocalPort).
const baoLocalPort = 53200

// Sentinels callers branch on (the drill tests, the up dispatch). Everything
// they mark exits 1; they exist for errors.Is, not for distinct exit codes.
var (
	errDigestMismatch  = errors.New("snapshot sha256 mismatch")
	errRestoreOverData = errors.New("refusing to restore over an initialized vault")
	errBaoUnreachable  = errors.New("openbao did not become reachable")
)

// baoState is OpenBao's seal/init status as probed over /sys/health — runtime
// truth, never recorded state, the same philosophy as the Talos mode probe.
type baoState int

const (
	baoUnreachable baoState = iota
	baoFresh                // initialized=false
	baoSealed               // initialized, sealed
	baoUnsealed             // initialized, unsealed
)

func (s baoState) String() string {
	switch s {
	case baoFresh:
		return "uninitialized"
	case baoSealed:
		return "initialized (sealed)"
	case baoUnsealed:
		return "initialized (unsealed)"
	default:
		return "unreachable"
	}
}

// baoHealthClient has a short timeout so down's best-effort probe degrades
// quickly; the dance's writes use http.DefaultClient (see baoAPI).
var baoHealthClient = &http.Client{Timeout: 5 * time.Second}

// baoHealth maps OpenBao's /sys/health status code to a baoState. The query
// pins the codes so the mapping is explicit instead of relying on defaults.
func baoHealth(addr string) (baoState, error) {
	resp, err := baoHealthClient.Get("http://" + addr + "/v1/sys/health?standbyok=true&uninitcode=501&sealedcode=503")
	if err != nil {
		return baoUnreachable, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusTooManyRequests: // 200 active, 429 unsealed standby
		return baoUnsealed, nil
	case http.StatusServiceUnavailable: // 503 sealed
		return baoSealed, nil
	case http.StatusNotImplemented: // 501 uninitialized
		return baoFresh, nil
	default:
		return baoUnreachable, fmt.Errorf("unexpected /sys/health status %d", resp.StatusCode)
	}
}

// probeBaoHealth polls until OpenBao answers with a known state: the pod is
// Ready (rollout finished) before its listener fully serves, and a just-
// restored node briefly refuses while it re-seals under the new keyring.
func probeBaoHealth(addr string) (baoState, error) {
	var st baoState
	err := poll("openbao health", 2*time.Minute, 3*time.Second, func() error {
		s, e := baoHealth(addr)
		if e != nil {
			return e
		}
		st = s
		return nil
	})
	return st, err
}

// baoAction is what `up` does after probing: report converged guidance, or run
// the restore dance.
type baoAction int

const (
	actReport baoAction = iota
	actRestore
)

// baoDecision crosses probed runtime truth with operator intent (--restore)
// and returns the refusal sentinel (or nil). Restore is only ever legal into a
// fresh vault; there is deliberately no force-overwrite flag — the wipe
// (guardian down --yes) is the override, so "restore over live data" can never
// happen as a flag typo. Returning the sentinel keeps this a pure, testable
// function; the caller decorates it with context.
func baoDecision(st baoState, restoring bool) (baoAction, error) {
	switch {
	case st == baoUnreachable:
		return actReport, errBaoUnreachable
	case restoring && (st == baoSealed || st == baoUnsealed):
		return actReport, errRestoreOverData
	case restoring && st == baoFresh:
		return actRestore, nil
	default:
		return actReport, nil
	}
}

// fetchAndVerifySnapshot resolves ref to a local snapshot file and proves its
// sha256 before `up` mutates anything: a corrupt or wrong blob must fail while
// the cluster is still untouched. ref is a local path or an http(s) URL; R2
// objects are reached by presigning them to https first — fetch is glue, not
// a credential holder.
func fetchAndVerifySnapshot(ref, wantSHA, state string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(wantSHA))
	if len(want) != 64 || !isHex(want) {
		return "", fmt.Errorf("up: %w: --sha256 must be 64 hex chars, got %q", errUsage, wantSHA)
	}
	h := sha256.New()
	var snapPath string
	switch {
	case strings.HasPrefix(ref, "http://"), strings.HasPrefix(ref, "https://"):
		resp, err := http.Get(ref)
		if err != nil {
			return "", fmt.Errorf("up: fetch %s: %w", ref, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("up: fetch %s: status %d (R2 fetch is glue — presign the object or download it and pass a file path)", ref, resp.StatusCode)
		}
		f, err := os.CreateTemp(state, "restore-*.snap")
		if err != nil {
			return "", fmt.Errorf("up: stage snapshot: %w", err)
		}
		defer f.Close()
		snapPath = f.Name()
		if _, err := io.Copy(f, io.TeeReader(resp.Body, h)); err != nil {
			return "", fmt.Errorf("up: download %s: %w", ref, err)
		}
	case strings.Contains(ref, "://"):
		return "", fmt.Errorf("up: %w: unsupported snapshot scheme in %q — presign the R2 object to an https URL or download it and pass a file path", errUsage, ref)
	default:
		snapPath = resolvePath(ref)
		f, err := os.Open(snapPath)
		if err != nil {
			return "", fmt.Errorf("up: open snapshot %s: %w", snapPath, err)
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return "", fmt.Errorf("up: read snapshot %s: %w", snapPath, err)
		}
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return "", fmt.Errorf("up: %w: ref %s computed %s, expected %s (corrupt blob, or the wrong snapshot)", errDigestMismatch, ref, got, want)
	}
	return snapPath, nil
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// baoAPI calls the OpenBao HTTP API and decodes a 2xx JSON body into out (nil
// to ignore). It uses http.DefaultClient with no timeout: snapshot-force
// streams the whole snapshot as the request body and may be large.
func baoAPI(addr, method, path, token string, body io.Reader, out any) error {
	req, err := http.NewRequest(method, "http://"+addr+path, body)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s %s: decode: %w", method, path, err)
		}
	}
	return nil
}

// restoreSnapshot runs the throwaway-init dance against a fresh vault: init and
// unseal with a single ephemeral Shamir key purely to get a barrier that can
// accept the snapshot, force-restore the snapshot (whose keyring differs from
// the throwaway one — hence -force), and confirm the vault re-sealed under the
// snapshot's own keyring. The throwaway key and root token never leave this
// function; after the restore they are garbage and go out of scope. The
// operator then unseals with the site's original shares.
func restoreSnapshot(addr, snapPath string) error {
	recoverHint := "; vault now holds a throwaway init — recover with guardian down --yes and re-run --restore"

	var initResp struct {
		KeysB64   []string `json:"keys_base64"`
		RootToken string   `json:"root_token"`
	}
	if err := baoAPI(addr, "PUT", "/v1/sys/init", "", strings.NewReader(`{"secret_shares":1,"secret_threshold":1}`), &initResp); err != nil {
		return fmt.Errorf("restore: init%s: %w", recoverHint, err)
	}
	if len(initResp.KeysB64) != 1 || initResp.RootToken == "" {
		return fmt.Errorf("restore: init returned no throwaway key or token%s", recoverHint)
	}

	var unsealResp struct {
		Sealed bool `json:"sealed"`
	}
	if err := baoAPI(addr, "PUT", "/v1/sys/unseal", "", strings.NewReader(fmt.Sprintf(`{"key":%q}`, initResp.KeysB64[0])), &unsealResp); err != nil {
		return fmt.Errorf("restore: unseal%s: %w", recoverHint, err)
	}
	if unsealResp.Sealed {
		return fmt.Errorf("restore: vault still sealed after throwaway unseal%s", recoverHint)
	}

	f, err := os.Open(snapPath)
	if err != nil {
		return fmt.Errorf("restore: open snapshot %s: %w", snapPath, err)
	}
	defer f.Close()
	if err := baoAPI(addr, "POST", "/v1/sys/storage/raft/snapshot-force", initResp.RootToken, f, nil); err != nil {
		return fmt.Errorf("restore: snapshot-force%s: %w", recoverHint, err)
	}

	// snapshot-force returns before the node finishes reapplying the restored
	// FSM and reseals under the snapshot's keyring (measured <100ms, but it is
	// a race): poll until it is actually sealed rather than reading the first
	// state, which can catch the pre-reseal unsealed window. The throwaway keys
	// are now garbage; the operator unseals with the site's original shares.
	if err := poll("openbao to reseal after restore", 2*time.Minute, time.Second, func() error {
		st, herr := baoHealth(addr)
		if herr != nil {
			return herr
		}
		if st != baoSealed {
			return fmt.Errorf("openbao is %s, awaiting reseal", st)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("restore: openbao did not reseal after snapshot-force: %w", err)
	}
	return nil
}
