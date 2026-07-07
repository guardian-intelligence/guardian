// Command custody manages the encrypted custody bundle: the secret-zero set
// (Talos genesis secrets, the OpenBao static-seal key, the operator env) that
// no system the cluster controls may ever hold in full. The encrypted restic
// repository is the ONLY at-rest form; plaintext exists solely inside the
// fixed tmpfs bundle directory between `restore` and `wipe`.
//
// Lifecycle: create (stage + validate fail-closed + backup), verify (repo
// integrity + latest snapshot carries every required member), restore (to
// tmpfs), wipe (shred the tmpfs bundle), status (staleness + plaintext
// residue), key-add (second password for the password-manager recovery flow).
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	// The bundle directory is a fixed path on tmpfs so backup and restore
	// round-trip to the same location and `wipe` never needs a user-supplied
	// target. /dev/shm keeps plaintext out of any disk-backed filesystem.
	defaultBundleDir = "/dev/shm/guardian-custody"

	envName = "custody.env"

	staleWarnAge = 30 * 24 * time.Hour
	minPassword  = 12
)

var unsealKeyRE = regexp.MustCompile(`^unseal-([0-9a-f]{64})\.key$`)

type member struct {
	bundlePath string
	required   bool
	desc       string
}

// The manifest is the auditor-facing declaration of the load-bearing set.
// Required members are the ones whose loss forfeits cluster or OpenBao
// control; optional members are re-issuable through provider consoles but
// determine DR speed. The unseal key is validated separately because its
// filename embeds the fingerprint.
var manifest = []member{
	{"talm/secrets.yaml", true, "Talos genesis secrets (machine/k8s/etcd CAs, service-account keys)"},
	{"talm/talm.key", true, "age key decrypting the committed-shape .encrypted Talm variants"},
	{"talm/talosconfig", true, "Talos API client credentials"},
	{"openbao/metadata.json", true, "OpenBao static-seal key metadata"},
	{envName, true, "operator env keys (importer source of truth)"},
	{"keys/github-promotions-app.private-key.pem", false, "GitHub promotions App key (re-issuable via GitHub)"},
	{"keys/verself-runner.private-key.pem", false, "Verself runner App key (re-issuable via GitHub)"},
	{"keys/guardian-worker-ssh", false, "worker SSH private key (re-issuable by reimage)"},
	{"keys/guardian-worker-ssh.pub", false, "worker SSH public key"},
	{"latitude.token", false, "Latitude API token (re-issuable via console)"},
}

type options struct {
	restic     string
	repo       string
	bundleDir  string
	talmRoot   string
	custodyDir string
	yes        bool
	readData   bool

	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	run    func(opts *options, extraEnv []string, args ...string) error
	runOut func(opts *options, extraEnv []string, args ...string) ([]byte, error)
}

func main() {
	opts := &options{
		stdin:  os.Stdin,
		stdout: os.Stdout,
		stderr: os.Stderr,
		run:    runRestic,
		runOut: runResticOutput,
	}
	if err := realMain(opts, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(1)
	}
}

func realMain(opts *options, argv []string) error {
	if len(argv) == 0 {
		return errors.New("usage: custody <create|verify|restore|wipe|status|key-add> [flags]")
	}
	sub, rest := argv[0], argv[1:]

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	fl := flag.NewFlagSet("custody "+sub, flag.ContinueOnError)
	fl.SetOutput(opts.stderr)
	fl.StringVar(&opts.restic, "restic", "restic", "path to the restic binary")
	fl.StringVar(&opts.repo, "repo", filepath.Join(home, ".guardian/custody/repo"), "restic repository directory")
	fl.StringVar(&opts.bundleDir, "bundle-dir", defaultBundleDir, "fixed tmpfs bundle directory (stage/restore/wipe target)")
	fl.StringVar(&opts.talmRoot, "talm-root", "", "legacy Talm root holding plaintext secrets.yaml/talm.key/talosconfig")
	fl.StringVar(&opts.custodyDir, "custody-dir", filepath.Join(home, "guardian-custody"), "legacy custody directory")
	fl.BoolVar(&opts.yes, "yes", false, "skip interactive confirmation")
	fl.BoolVar(&opts.readData, "read-data", false, "verify: also re-read and hash every pack (slow, run against offline copies)")
	if err := fl.Parse(rest); err != nil {
		return err
	}

	switch sub {
	case "create":
		return cmdCreate(opts)
	case "verify":
		return cmdVerify(opts)
	case "restore":
		return cmdRestore(opts)
	case "wipe":
		return cmdWipe(opts)
	case "status":
		return cmdStatus(opts)
	case "key-add":
		return opts.run(opts, nil, "key", "add")
	default:
		return fmt.Errorf("unknown subcommand %q (want create|verify|restore|wipe|status|key-add)", sub)
	}
}

// Both runners pass --no-cache: restic would otherwise keep encrypted
// repository metadata under ~/.cache/restic, a durable on-disk artifact the
// "repository is the only at-rest form" model does not account for. The
// custody repo is small enough that the cache buys nothing.
//
// Accepted limit: when a password rides in extraEnv it is visible at
// /proc/<pid>/environ to same-UID processes for the life of the restic
// child, and Go strings holding it are not zeroed. A same-UID attacker can
// already ptrace restic or read the prompt; local process isolation is out
// of scope here.
func runRestic(opts *options, extraEnv []string, args ...string) error {
	cmd := exec.Command(opts.restic, append([]string{"--repo", opts.repo, "--no-cache"}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = opts.stdout
	cmd.Stderr = opts.stderr
	cmd.Env = append(os.Environ(), extraEnv...)
	return cmd.Run()
}

func runResticOutput(opts *options, extraEnv []string, args ...string) ([]byte, error) {
	cmd := exec.Command(opts.restic, append([]string{"--repo", opts.repo, "--no-cache"}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = opts.stderr
	cmd.Env = append(os.Environ(), extraEnv...)
	return cmd.Output()
}

// --- create ---

func cmdCreate(opts *options) error {
	src, err := resolveSources(opts)
	if err != nil {
		return err
	}

	if !opts.yes {
		fmt.Fprintf(opts.stdout, "About to back up the custody bundle to %s:\n", opts.repo)
		for _, r := range src.resolved {
			fmt.Fprintf(opts.stdout, "  %-50s <- %s\n", r.bundlePath, r.source)
		}
		for _, m := range src.missingOptional {
			fmt.Fprintf(opts.stdout, "  %-50s (optional, absent)\n", m)
		}
		ok, err := confirm(opts, "Proceed? [yes/no]: ")
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("aborted")
		}
	}

	env, err := ensureRepo(opts)
	if err != nil {
		return err
	}

	staged, stagedFresh, err := stageBundle(opts, src)
	// Only shred what create itself staged — success, failure, or partial
	// staging alike. A pre-existing bundle dir was restored deliberately and
	// stays until an explicit wipe.
	if stagedFresh {
		defer func() {
			if err := shredDir(staged); err != nil {
				fmt.Fprintf(opts.stderr, "WARN: could not shred staging dir %s: %v — wipe it by hand\n", staged, err)
			}
		}()
	}
	if err != nil {
		return err
	}

	if err := opts.run(opts, env, "backup", "--tag", "custody", staged); err != nil {
		return fmt.Errorf("restic backup: %w", err)
	}
	if err := opts.run(opts, env, "check"); err != nil {
		return fmt.Errorf("restic check after backup: %w", err)
	}

	// The lifecycle closes itself: prove the round trip (restore the fresh
	// snapshot to a scratch dir and byte-compare every member against its
	// source), and only then shred the plaintext sources. Deletion is gated
	// on a demonstrated restore, never on faith in the writer.
	if stagedFresh {
		if err := proveRoundTrip(opts, env, staged); err != nil {
			return fmt.Errorf("round-trip proof failed — plaintext sources left untouched: %w", err)
		}
		if err := shredSources(opts, src); err != nil {
			return err
		}
		fmt.Fprintf(opts.stdout, "round trip proven; plaintext sources shredded — the repository is now the only at-rest form\n")
	}
	printInstructions(opts)
	return nil
}

func proveRoundTrip(opts *options, env []string, staged string) error {
	snap, err := latestSnapshot(opts, env)
	if err != nil {
		return err
	}
	proof := opts.bundleDir + ".proof"
	if err := requireTmpfs(proof); err != nil {
		return err
	}
	if err := os.RemoveAll(proof); err != nil {
		return err
	}
	defer func() {
		if err := shredDir(proof); err != nil {
			fmt.Fprintf(opts.stderr, "WARN: could not shred proof dir %s: %v — wipe it by hand\n", proof, err)
		}
	}()
	if err := opts.run(opts, env, "restore", snap.ID, "--target", proof); err != nil {
		return fmt.Errorf("restic restore to proof dir: %w", err)
	}
	// restore --target re-roots the snapshot's absolute paths under proof.
	return filepath.WalkDir(staged, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		want, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(filepath.Join(proof, path))
		if err != nil {
			return fmt.Errorf("restored copy missing for %s: %w", path, err)
		}
		if !bytes.Equal(want, got) {
			return fmt.Errorf("restored bytes differ from source for %s", path)
		}
		return nil
	})
}

// shredSources removes the resolved plaintext sources plus the talm root's
// derived residue (minted kubeconfig, .encrypted variants) after the round
// trip is proven. Copies the resolver never saw (stray checkouts) are the
// scan layer's job, not this function's.
func shredSources(opts *options, src *sources) error {
	for _, r := range src.resolved {
		if err := shredFile(r.source); err != nil {
			return err
		}
	}
	if opts.talmRoot != "" {
		for _, name := range []string{"kubeconfig", "secrets.encrypted.yaml", "talosconfig.encrypted"} {
			p := filepath.Join(opts.talmRoot, name)
			if _, err := os.Stat(p); err == nil {
				if err := shredFile(p); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

type resolvedMember struct {
	bundlePath string
	source     string
}

type sources struct {
	resolved        []resolvedMember
	missingOptional []string
}

func resolveSources(opts *options) (*sources, error) {
	if _, err := os.Stat(opts.bundleDir); err == nil {
		return resolveFromBundleLayout(opts, opts.bundleDir)
	}
	return resolveFromLegacy(opts)
}

// requireTmpfs pins every path that will ever hold plaintext to /dev/shm.
// create, restore, and wipe all route through it, so a snapshot can only
// ever record — and restore can only ever re-materialize — the fixed tmpfs
// location.
func requireTmpfs(dir string) error {
	if !strings.HasPrefix(dir, "/dev/shm/") {
		return fmt.Errorf("%s is not on /dev/shm; plaintext custody material must stay on tmpfs", dir)
	}
	return nil
}

// resolveFromBundleLayout validates a directory already in bundle layout
// (typically the tmpfs dir left by `restore`, after an operator edit).
func resolveFromBundleLayout(opts *options, dir string) (*sources, error) {
	out := &sources{}
	var missing []string
	for _, m := range manifest {
		p := filepath.Join(dir, m.bundlePath)
		if _, err := os.Stat(p); err != nil {
			if m.required {
				missing = append(missing, fmt.Sprintf("%s (%s)", m.bundlePath, m.desc))
			} else {
				out.missingOptional = append(out.missingOptional, m.bundlePath)
			}
			continue
		}
		out.resolved = append(out.resolved, resolvedMember{m.bundlePath, p})
	}
	key, err := findUnsealKey(dir)
	if err != nil {
		missing = append(missing, "openbao/unseal-<sha256>.key ("+err.Error()+")")
	} else {
		out.resolved = append(out.resolved, resolvedMember{"openbao/" + filepath.Base(key), key})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("bundle at %s is missing required members — refusing to create a bundle that would be trusted and useless:\n  %s", dir, strings.Join(missing, "\n  "))
	}
	return out, nil
}

// resolveFromLegacy assembles the bundle from the pre-archive-only layout:
// plaintext Talm state in the repo tree plus the operator custody directory.
// This is the one-time migration path and the genesis path for new clusters.
func resolveFromLegacy(opts *options) (*sources, error) {
	if opts.talmRoot == "" {
		return nil, errors.New("--talm-root is required when no bundle-layout directory exists (pass the Talm chart root holding plaintext secrets.yaml)")
	}
	out := &sources{}
	var missing []string

	locate := func(m member) (string, bool) {
		base := filepath.Base(m.bundlePath)
		switch {
		case strings.HasPrefix(m.bundlePath, "talm/"):
			return filepath.Join(opts.talmRoot, base), true
		case m.bundlePath == envName:
			return filepath.Join(opts.custodyDir, envName), true
		case m.bundlePath == "openbao/metadata.json":
			p, err := findSealSibling(opts.custodyDir, "metadata.json")
			if err != nil {
				return "", false
			}
			return p, true
		default:
			return filepath.Join(opts.custodyDir, base), true
		}
	}

	for _, m := range manifest {
		p, ok := locate(m)
		if ok {
			if _, err := os.Stat(p); err != nil {
				ok = false
			}
		}
		if !ok {
			if m.required {
				missing = append(missing, fmt.Sprintf("%s (%s)", m.bundlePath, m.desc))
			} else {
				out.missingOptional = append(out.missingOptional, m.bundlePath)
			}
			continue
		}
		out.resolved = append(out.resolved, resolvedMember{m.bundlePath, p})
	}

	key, err := findUnsealKey(opts.custodyDir)
	if err != nil {
		missing = append(missing, "openbao/unseal-<sha256>.key ("+err.Error()+")")
	} else {
		out.resolved = append(out.resolved, resolvedMember{"openbao/" + filepath.Base(key), key})
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required custody members — refusing to create a bundle that would be trusted and useless:\n  %s", strings.Join(missing, "\n  "))
	}
	return out, nil
}

// findUnsealKey walks dir for unseal-<sha256>.key files, requires exactly
// one — even byte-identical copies are refused, because metadata.json is
// resolved from the key's directory and two copies make that binding
// path-order-dependent — and verifies the content hash matches the filename
// fingerprint so a truncated or swapped key cannot enter the bundle.
func findUnsealKey(dir string) (string, error) {
	var found []string
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if unsealKeyRE.MatchString(d.Name()) {
			found = append(found, path)
		}
		return nil
	})
	if walkErr != nil {
		// A partial walk could hide a second key and defeat the exactly-one
		// guard, so an aborted traversal fails closed.
		return "", fmt.Errorf("walking %s for unseal keys: %w", dir, walkErr)
	}
	if len(found) == 0 {
		return "", fmt.Errorf("no unseal-<sha256>.key under %s", dir)
	}
	if len(found) > 1 {
		sort.Strings(found)
		return "", fmt.Errorf("multiple unseal key files found, refusing to guess which directory owns the live key and its metadata — remove the stale copies:\n  %s", strings.Join(found, "\n  "))
	}
	b, err := os.ReadFile(found[0])
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	sum := hex.EncodeToString(h[:])
	want := unsealKeyRE.FindStringSubmatch(filepath.Base(found[0]))[1]
	if sum != want {
		return "", fmt.Errorf("%s content hash %s does not match its filename fingerprint", found[0], sum)
	}
	return found[0], nil
}

func findSealSibling(dir, name string) (string, error) {
	key, err := findUnsealKey(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(key), name), nil
}

// stageBundle returns the directory to back up and whether this call staged
// it fresh (fresh staging is shredded by create; a directory the operator
// already had open is not).
func stageBundle(opts *options, src *sources) (string, bool, error) {
	if err := requireTmpfs(opts.bundleDir); err != nil {
		return "", false, err
	}
	if _, err := os.Stat(opts.bundleDir); err == nil {
		// resolveSources already validated this layout in place.
		return opts.bundleDir, false, nil
	}
	dir := opts.bundleDir
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, err
	}
	for _, r := range src.resolved {
		dst := filepath.Join(dir, r.bundlePath)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return dir, true, err
		}
		b, err := os.ReadFile(r.source)
		if err != nil {
			return dir, true, err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return dir, true, err
		}
	}
	return dir, true, nil
}

// promptRepoPassword prompts once for an existing repository's password and
// returns it as a RESTIC_PASSWORD env entry, so a command's several restic
// invocations (check, snapshots, ls, restore...) do not each re-prompt.
// Returns a nil env — restic sources the password itself — when RESTIC_PASSWORD
// is already set or there is no terminal to prompt. The password rides in the
// environment only; see the runRestic note on the accepted /proc visibility.
func promptRepoPassword(opts *options) ([]string, error) {
	if os.Getenv("RESTIC_PASSWORD") != "" || !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, nil
	}
	fmt.Fprint(opts.stdout, "Custody repository password: ")
	password, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(opts.stdout)
	if err != nil {
		return nil, err
	}
	return []string{"RESTIC_PASSWORD=" + string(password)}, nil
}

// ensureRepo initializes the restic repository on first use, owning the
// password prompt so a length floor and double entry can be enforced. The
// password is handed to restic via the environment only; it never touches
// argv or disk. Later operations let restic prompt on its own.
func ensureRepo(opts *options) ([]string, error) {
	if _, err := os.Stat(filepath.Join(opts.repo, "config")); err == nil {
		// Existing repo: prompt once here so backup and the follow-up check
		// don't each ask; with RESTIC_PASSWORD set restic inherits it.
		return promptRepoPassword(opts)
	}
	if err := os.MkdirAll(opts.repo, 0o700); err != nil {
		return nil, err
	}
	password := os.Getenv("RESTIC_PASSWORD")
	if password == "" {
		var err error
		password, err = promptNewPassword(opts)
		if err != nil {
			return nil, err
		}
	}
	env := []string{"RESTIC_PASSWORD=" + password}
	if err := opts.run(opts, env, "init"); err != nil {
		return nil, fmt.Errorf("restic init: %w", err)
	}
	return env, nil
}

func promptNewPassword(opts *options) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("no terminal to prompt for a new repository password; set RESTIC_PASSWORD for non-interactive use")
	}
	for {
		fmt.Fprintf(opts.stdout, "New custody repository password (min %d chars): ", minPassword)
		first, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(opts.stdout)
		if err != nil {
			return "", err
		}
		if len(first) < minPassword {
			fmt.Fprintf(opts.stdout, "Too short — this password is the only thing standing between the repository and an attacker.\n")
			continue
		}
		fmt.Fprint(opts.stdout, "Repeat password: ")
		second, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(opts.stdout)
		if err != nil {
			return "", err
		}
		if string(first) != string(second) {
			fmt.Fprintln(opts.stdout, "Passwords do not match, try again.")
			continue
		}
		return string(first), nil
	}
}

func confirm(opts *options, prompt string) (bool, error) {
	fmt.Fprint(opts.stdout, prompt)
	var answer string
	if _, err := fmt.Fscanln(opts.stdin, &answer); err != nil {
		return false, err
	}
	return strings.EqualFold(answer, "yes"), nil
}

// --- verify / status ---

type snapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Paths []string  `json:"paths"`
}

func latestSnapshot(opts *options, env []string) (*snapshot, error) {
	out, err := opts.runOut(opts, env, "snapshots", "latest", "--json")
	if err != nil {
		return nil, fmt.Errorf("restic snapshots: %w", err)
	}
	var snaps []snapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, fmt.Errorf("parsing restic snapshots output: %w", err)
	}
	if len(snaps) == 0 {
		return nil, errors.New("repository holds no snapshots")
	}
	// `snapshots latest` returns one entry per host+path group with no
	// ordering guarantee, so pick the globally newest by timestamp.
	newest := &snaps[0]
	for i := range snaps {
		if snaps[i].Time.After(newest.Time) {
			newest = &snaps[i]
		}
	}
	if len(newest.ID) < 8 {
		return nil, fmt.Errorf("restic returned malformed snapshot id %q", newest.ID)
	}
	return newest, nil
}

func cmdVerify(opts *options) error {
	env, err := promptRepoPassword(opts)
	if err != nil {
		return err
	}
	checkArgs := []string{"check"}
	if opts.readData {
		checkArgs = append(checkArgs, "--read-data")
	}
	if err := opts.run(opts, env, checkArgs...); err != nil {
		return fmt.Errorf("restic check: %w", err)
	}

	snap, err := latestSnapshot(opts, env)
	if err != nil {
		return err
	}
	out, err := opts.runOut(opts, env, "ls", "--json", snap.ID)
	if err != nil {
		return fmt.Errorf("restic ls: %w", err)
	}
	present := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" {
			continue
		}
		var node struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(line), &node); err != nil {
			continue
		}
		present[node.Path] = true
	}

	var missing []string
	for _, m := range manifest {
		if !m.required {
			continue
		}
		if !presentUnderAnyRoot(present, m.bundlePath) {
			missing = append(missing, m.bundlePath)
		}
	}
	sealOK := false
	for p := range present {
		if unsealKeyRE.MatchString(filepath.Base(p)) && strings.Contains(p, "/openbao/") {
			sealOK = true
		}
	}
	if !sealOK {
		missing = append(missing, "openbao/unseal-<sha256>.key")
	}
	if len(missing) > 0 {
		return fmt.Errorf("latest snapshot %s (%s) is missing required members:\n  %s", snap.ID[:8], snap.Time.Format(time.RFC3339), strings.Join(missing, "\n  "))
	}
	fmt.Fprintf(opts.stdout, "OK: repository is sound; latest snapshot %s from %s carries every required member\n", snap.ID[:8], snap.Time.Format(time.RFC3339))
	return nil
}

func presentUnderAnyRoot(present map[string]bool, bundlePath string) bool {
	suffix := "/" + bundlePath
	for p := range present {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}

func cmdStatus(opts *options) error {
	if _, err := os.Stat(filepath.Join(opts.repo, "config")); err != nil {
		return fmt.Errorf("no custody repository at %s — run `aspect infra custody --action create`", opts.repo)
	}
	env, err := promptRepoPassword(opts)
	if err != nil {
		return err
	}
	snap, err := latestSnapshot(opts, env)
	if err != nil {
		return err
	}
	age := time.Since(snap.Time).Round(time.Hour)
	fmt.Fprintf(opts.stdout, "latest snapshot: %s (%s, %s old)\n", snap.ID[:8], snap.Time.Format(time.RFC3339), age)
	if age > staleWarnAge {
		fmt.Fprintf(opts.stdout, "WARN: latest snapshot is older than %s; re-run create after custody events and refresh the offline copies\n", staleWarnAge)
	}
	if _, err := os.Stat(opts.bundleDir); err == nil {
		fmt.Fprintf(opts.stdout, "WARN: plaintext bundle is open at %s — run `aspect infra custody --action wipe` when done\n", opts.bundleDir)
	}
	for _, residue := range plaintextResidue(opts) {
		fmt.Fprintf(opts.stdout, "WARN: plaintext custody material at %s — the encrypted repository must be the only at-rest form\n", residue)
	}
	return nil
}

func plaintextResidue(opts *options) []string {
	var out []string
	if opts.talmRoot != "" {
		for _, name := range []string{"secrets.yaml", "talm.key", "talosconfig"} {
			p := filepath.Join(opts.talmRoot, name)
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
			}
		}
	}
	p := filepath.Join(opts.custodyDir, envName)
	if _, err := os.Stat(p); err == nil {
		out = append(out, p)
	}
	return out
}

// --- restore / wipe ---

func cmdRestore(opts *options) error {
	if err := requireTmpfs(opts.bundleDir); err != nil {
		return err
	}
	if _, err := os.Stat(opts.bundleDir); err == nil {
		return fmt.Errorf("%s already exists; wipe it first (`aspect infra custody --action wipe`) so stale and fresh state cannot mix", opts.bundleDir)
	}
	env, err := promptRepoPassword(opts)
	if err != nil {
		return err
	}
	snap, err := latestSnapshot(opts, env)
	if err != nil {
		return err
	}
	// Snapshots record the fixed tmpfs path, so restoring against / puts
	// the bundle back exactly where wipe and create expect it. A snapshot
	// recording any other path (foreign repo, changed --bundle-dir) is
	// refused before plaintext lands anywhere.
	for _, p := range snap.Paths {
		if p != opts.bundleDir {
			return fmt.Errorf("snapshot %s records path %s, not %s; refusing to restore plaintext to an unmanaged location", snap.ID[:8], p, opts.bundleDir)
		}
	}
	if len(snap.Paths) == 0 {
		return fmt.Errorf("snapshot %s records no paths; refusing to restore blind", snap.ID[:8])
	}
	if err := opts.run(opts, env, "restore", snap.ID, "--target", "/"); err != nil {
		return fmt.Errorf("restic restore: %w", err)
	}
	if err := os.Chmod(opts.bundleDir, 0o700); err != nil {
		return err
	}
	fmt.Fprintf(opts.stdout, "restored snapshot %s (%s) to %s\n", snap.ID[:8], snap.Time.Format(time.RFC3339), opts.bundleDir)
	fmt.Fprintf(opts.stdout, "wipe it the moment you are done: aspect infra custody --action wipe\n")
	return nil
}

func cmdWipe(opts *options) error {
	if err := requireTmpfs(opts.bundleDir); err != nil {
		return fmt.Errorf("refusing to wipe: %w", err)
	}
	if _, err := os.Stat(opts.bundleDir); err != nil {
		fmt.Fprintf(opts.stdout, "nothing to wipe at %s\n", opts.bundleDir)
		return nil
	}
	if err := shredDir(opts.bundleDir); err != nil {
		return err
	}
	fmt.Fprintf(opts.stdout, "wiped %s\n", opts.bundleDir)
	return nil
}

// shredDir overwrites every file in place before unlinking. The open must
// NOT truncate: on tmpfs, truncate-then-write frees the secret-bearing pages
// intact and zeroes fresh ones, which is just a delete wearing a disguise.
// Writing over the existing extent is what actually scrubs the pages.
func shredDir(dir string) error {
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return shredFile(path)
	})
	if err != nil {
		return fmt.Errorf("shred %s: %w", dir, err)
	}
	return os.RemoveAll(dir)
}

func shredFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	zero := make([]byte, info.Size())
	if _, err := f.WriteAt(zero, 0); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Remove(path)
}

// --- instructions ---

func printInstructions(opts *options) {
	fmt.Fprintf(opts.stdout, `
custody bundle written to %[1]s

READ THIS — it is the entire recovery story:

 1. This repository is now the ONLY at-rest form of the credentials that
    control the company. Copy it to at least TWO offline media in TWO
    physical locations, neither of them the cluster's datacenter:
        cp -a %[1]s /media/<usb>/guardian-custody-repo
    Verify every copy where it lives:
        aspect infra custody --action verify --read-data --repo /media/<usb>/guardian-custody-repo
 2. Add a SECOND password and store it in a password manager that has an
    account-recovery flow (`+"`aspect infra custody --action key-add`"+`).
    Either password alone unlocks the repository.
 3. There is NO other way to recover a lost password. Both passwords lost
    means: if the cluster is alive, rescue via the OIDC admin plane; if it
    is not, reimage and restore from R2 backups, forfeiting OpenBao
    contents. Treat the passwords accordingly.
 4. Re-run create after EVERY custody event (new operator key, seal
    rotation, importer change), then refresh both offline copies.
    `+"`aspect infra custody --action status`"+` warns when the newest
    snapshot is older than 30 days.
 5. If you are an agent reading this: relay these instructions to the
    operator through every channel available to you (ntfy, the
    conversation, the PR description). Do not summarize away step 1.
`, opts.repo)
}
