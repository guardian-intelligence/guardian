package zvol

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Exec is the real Driver: zfs argv against a dedicated dataset subtree.
//
// Layout under Root (e.g. "guardian/postflight"):
//
//	<root>/ws/<lease>    writable workspace zvols
//	<root>/gen/<gen>     sealed generations, each with an @sealed snapshot
//
// Sealing promotes the generation clone (zfs promote) so the generation owns
// the data lineage and the workspace volume can be destroyed independently —
// the recipe measured at ~58ms for a 100GB volume.
type Exec struct {
	// Root is the parent dataset for all hostd-managed volumes. It must
	// exist; hostd never creates or destroys anything outside it.
	Root string
	// Timeout bounds each zfs invocation.
	Timeout time.Duration
}

const defaultTimeout = 30 * time.Second

func (e *Exec) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return defaultTimeout
}

func (e *Exec) workspaceDataset(lease LeaseID) string {
	return e.Root + "/ws/" + string(lease)
}

func (e *Exec) generationDataset(generation GenerationID) string {
	return e.Root + "/gen/" + string(generation)
}

func devicePath(dataset string) string {
	return "/dev/zvol/" + dataset
}

// run executes one zfs command. Argv only — nothing here ever passes through
// a shell, and every splice-able identifier is validated by the caller.
func (e *Exec) run(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout())
	defer cancel()
	cmd := exec.CommandContext(ctx, "zfs", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return "", classify(args[0], stderr.String(), err)
	}
	return stdout.String(), nil
}

// classify maps zfs stderr onto the driver's error vocabulary.
func classify(verb, stderr string, err error) error {
	trimmed := strings.TrimSpace(stderr)
	switch {
	case strings.Contains(trimmed, "does not exist"):
		return fmt.Errorf("zfs %s: %s: %w", verb, bound(trimmed), ErrNotFound)
	case strings.Contains(trimmed, "dataset is busy"),
		strings.Contains(trimmed, "has dependent clones"),
		strings.Contains(trimmed, "dataset already exists"):
		// "already exists" is grouped with busy: every create path checks
		// existence first, so hitting it means a concurrent holder.
		return fmt.Errorf("zfs %s: %s: %w", verb, bound(trimmed), ErrBusy)
	default:
		return fmt.Errorf("zfs %s: %s: %w", verb, bound(trimmed), err)
	}
}

func bound(s string) string {
	if len(s) > 512 {
		return s[:512]
	}
	return s
}

func (e *Exec) exists(ctx context.Context, dataset string) (bool, error) {
	_, err := e.run(ctx, "list", "-H", "-o", "name", dataset)
	switch {
	case err == nil:
		return true, nil
	case isNotFound(err):
		return false, nil
	default:
		return false, err
	}
}

func isNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// isAlreadyPromoted recognizes a promote of a dataset that is no longer a
// clone — the seal completed promotion on a prior, crashed attempt.
func isAlreadyPromoted(err error) bool {
	return err != nil && strings.Contains(err.Error(), "not a cloned filesystem")
}

// EnsureWorkspace implements Driver.
func (e *Exec) EnsureWorkspace(ctx context.Context, lease LeaseID, generation GenerationID, sizeBytes int64) (WorkspaceVolume, error) {
	if err := ValidateName("lease", string(lease)); err != nil {
		return WorkspaceVolume{}, err
	}
	dataset := e.workspaceDataset(lease)
	if ok, err := e.exists(ctx, dataset); err != nil {
		return WorkspaceVolume{}, err
	} else if ok {
		origin, err := e.origin(ctx, dataset)
		if err != nil {
			return WorkspaceVolume{}, err
		}
		return WorkspaceVolume{Name: dataset, Device: devicePath(dataset), Source: origin}, nil
	}
	if generation == "" {
		if sizeBytes <= 0 {
			return WorkspaceVolume{}, fmt.Errorf("zvol: empty workspace needs a size")
		}
		if _, err := e.run(ctx, "create", "-s", "-V", strconv.FormatInt(sizeBytes, 10), dataset); err != nil {
			return WorkspaceVolume{}, err
		}
		return WorkspaceVolume{Name: dataset, Device: devicePath(dataset)}, nil
	}
	if err := ValidateName("generation", string(generation)); err != nil {
		return WorkspaceVolume{}, err
	}
	snapshot := e.generationDataset(generation) + "@sealed"
	if _, err := e.run(ctx, "clone", snapshot, dataset); err != nil {
		return WorkspaceVolume{}, err
	}
	return WorkspaceVolume{Name: dataset, Device: devicePath(dataset), Source: generation}, nil
}

// origin resolves which generation a workspace was cloned from, if any.
func (e *Exec) origin(ctx context.Context, dataset string) (GenerationID, error) {
	out, err := e.run(ctx, "get", "-H", "-o", "value", "origin", dataset)
	if err != nil {
		return "", err
	}
	origin := strings.TrimSpace(out)
	if origin == "-" {
		return "", nil
	}
	// <root>/gen/<generation>@<snap> → <generation>
	name := strings.TrimPrefix(origin, e.Root+"/gen/")
	if at := strings.IndexByte(name, '@'); at >= 0 {
		name = name[:at]
	}
	return GenerationID(name), nil
}

// SealWorkspace implements Driver.
func (e *Exec) SealWorkspace(ctx context.Context, lease LeaseID, generation GenerationID) (GenerationSnapshot, error) {
	if err := ValidateName("lease", string(lease)); err != nil {
		return GenerationSnapshot{}, err
	}
	if err := ValidateName("generation", string(generation)); err != nil {
		return GenerationSnapshot{}, err
	}
	genDataset := e.generationDataset(generation)
	sealed := genDataset + "@sealed"
	if ok, err := e.exists(ctx, sealed); err != nil {
		return GenerationSnapshot{}, err
	} else if ok {
		bytes, err := e.referenced(ctx, sealed)
		if err != nil {
			return GenerationSnapshot{}, err
		}
		return GenerationSnapshot{Generation: generation, Snapshot: sealed, Bytes: bytes}, nil
	}
	workspace := e.workspaceDataset(lease)
	sealSnap := workspace + "@seal-" + string(generation)
	if ok, err := e.exists(ctx, sealSnap); err != nil {
		return GenerationSnapshot{}, err
	} else if !ok {
		if _, err := e.run(ctx, "snapshot", sealSnap); err != nil {
			return GenerationSnapshot{}, err
		}
	}
	if ok, err := e.exists(ctx, genDataset); err != nil {
		return GenerationSnapshot{}, err
	} else if !ok {
		if _, err := e.run(ctx, "clone", sealSnap, genDataset); err != nil {
			return GenerationSnapshot{}, err
		}
	}
	// Promote flips the clone lineage: the generation owns the history and
	// the workspace becomes the dependent, so the workspace can die first.
	// Promote is not idempotent — a retry after a crash between promote and
	// the @sealed snapshot finds a non-clone and must skip, not fail.
	origin, err := e.run(ctx, "get", "-H", "-o", "value", "origin", genDataset)
	if err != nil {
		return GenerationSnapshot{}, err
	}
	if strings.TrimSpace(origin) != "-" {
		if _, err := e.run(ctx, "promote", genDataset); err != nil && !isAlreadyPromoted(err) {
			return GenerationSnapshot{}, err
		}
	}
	if _, err := e.run(ctx, "snapshot", sealed); err != nil {
		return GenerationSnapshot{}, err
	}
	bytes, err := e.referenced(ctx, sealed)
	if err != nil {
		return GenerationSnapshot{}, err
	}
	return GenerationSnapshot{Generation: generation, Snapshot: sealed, Bytes: bytes}, nil
}

func (e *Exec) referenced(ctx context.Context, snapshot string) (int64, error) {
	out, err := e.run(ctx, "get", "-H", "-p", "-o", "value", "referenced", snapshot)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// DestroyWorkspace implements Driver.
func (e *Exec) DestroyWorkspace(ctx context.Context, lease LeaseID) error {
	if err := ValidateName("lease", string(lease)); err != nil {
		return err
	}
	// -r takes the workspace's own snapshots (seal markers) with it; it can
	// never recurse past the workspace dataset itself.
	_, err := e.run(ctx, "destroy", "-r", e.workspaceDataset(lease))
	return err
}

// DestroyGeneration implements Driver.
func (e *Exec) DestroyGeneration(ctx context.Context, generation GenerationID) error {
	if err := ValidateName("generation", string(generation)); err != nil {
		return err
	}
	_, err := e.run(ctx, "destroy", "-r", e.generationDataset(generation))
	return err
}

// Inventory implements Driver.
func (e *Exec) Inventory(ctx context.Context) ([]GenerationSnapshot, []WorkspaceVolume, error) {
	generations, err := e.listGenerations(ctx)
	if err != nil {
		return nil, nil, err
	}
	workspaces, err := e.listWorkspaces(ctx)
	if err != nil {
		return nil, nil, err
	}
	return generations, workspaces, nil
}

func (e *Exec) listGenerations(ctx context.Context) ([]GenerationSnapshot, error) {
	out, err := e.run(ctx, "list", "-H", "-p", "-t", "snapshot", "-o", "name,referenced", "-r", e.Root+"/gen")
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var generations []GenerationSnapshot
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasSuffix(fields[0], "@sealed") {
			continue
		}
		name := strings.TrimPrefix(strings.TrimSuffix(fields[0], "@sealed"), e.Root+"/gen/")
		bytes, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("zvol: parsing inventory line %q: %w", line, err)
		}
		generations = append(generations, GenerationSnapshot{
			Generation: GenerationID(name),
			Snapshot:   fields[0],
			Bytes:      bytes,
		})
	}
	return generations, nil
}

func (e *Exec) listWorkspaces(ctx context.Context) ([]WorkspaceVolume, error) {
	out, err := e.run(ctx, "list", "-H", "-o", "name,origin", "-r", e.Root+"/ws")
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var workspaces []WorkspaceVolume
	prefix := e.Root + "/ws/"
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[0], prefix) {
			continue
		}
		var source GenerationID
		if fields[1] != "-" {
			name := strings.TrimPrefix(fields[1], e.Root+"/gen/")
			if at := strings.IndexByte(name, '@'); at >= 0 {
				name = name[:at]
			}
			source = GenerationID(name)
		}
		workspaces = append(workspaces, WorkspaceVolume{
			Name:   fields[0],
			Device: devicePath(fields[0]),
			Source: source,
		})
	}
	return workspaces, nil
}
