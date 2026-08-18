package zvol

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
)

// TransferTree names one of the three dataset trees a generation set spans.
type TransferTree string

const (
	TreeWorkspace TransferTree = "workspace"
	TreeTool      TransferTree = "tool"
	TreeProcess   TransferTree = "process"
)

// TransferTrees is the complete set, in materialization order.
var TransferTrees = []TransferTree{TreeWorkspace, TreeTool, TreeProcess}

// ValidTransferTree reports whether tree names one of the generation trees.
func ValidTransferTree(tree TransferTree) bool {
	return tree == TreeWorkspace || tree == TreeTool || tree == TreeProcess
}

// SendPlan is a resolved, validated send of one tree of a sealed generation.
type SendPlan struct {
	Generation GenerationID
	Tree       TransferTree
	// Snapshot is the full sealed-snapshot name being sent.
	Snapshot string
	// BaseSnapshot is the origin snapshot an incremental stream is taken
	// against; empty for a full stream.
	BaseSnapshot string
	Incremental  bool
}

// TransferStore is the optional generation send/receive surface. It is
// separate from Driver, like CapacitySource, so substrate fakes and alternate
// durable-volume implementations can opt in independently. Received
// generations live in a transfer-cache namespace (<root>/xfer/<generation>
// per tree) that Inventory never reports as residency; materialization clones
// from the cache exactly as it clones from a resident generation, and the
// cache is derived state the host may discard whenever no assignment
// references it.
type TransferStore interface {
	// ResolveSend validates that the generation's sealed snapshot exists for
	// the tree (ErrNotFound otherwise) and decides whether from can serve as
	// an incremental base: only when from's sealed snapshot is the dataset's
	// direct ZFS origin, which send/recv GUID propagation makes globally
	// unambiguous. Anything else degrades to a full stream.
	ResolveSend(ctx context.Context, generation GenerationID, tree TransferTree, from GenerationID) (SendPlan, error)

	// Send streams the plan's compressed replication stream into w and
	// returns the bytes written.
	Send(ctx context.Context, plan SendPlan, w io.Writer) (int64, error)

	// ReceiveGeneration lands one tree's stream in the transfer cache. It is
	// idempotent: a tree already cached returns nil without consuming r.
	ReceiveGeneration(ctx context.Context, generation GenerationID, tree TransferTree, r io.Reader) error

	// GenerationState reports whether the generation is resident (sealed in
	// the generation namespace) and whether the complete set is cached in the
	// transfer namespace.
	GenerationState(ctx context.Context, generation GenerationID) (resident, cached bool, err error)

	// ListTransfers names every generation with at least one cached tree.
	ListTransfers(ctx context.Context) ([]GenerationID, error)

	// DestroyTransfer removes a generation's cached trees. ErrBusy if a
	// dependent (a live workspace clone, or a generation sealed from the
	// cached snapshot) still needs it.
	DestroyTransfer(ctx context.Context, generation GenerationID) error
}

func (e *Exec) transferDataset(generation GenerationID) string {
	return e.Root + "/xfer/" + string(generation)
}

func (e *Exec) treeDriver(tree TransferTree) *Exec {
	switch tree {
	case TreeTool:
		return e.toolDriver()
	case TreeProcess:
		return e.processDriver()
	default:
		return e
	}
}

// ResolveSend implements TransferStore.
func (e *Exec) ResolveSend(ctx context.Context, generation GenerationID, tree TransferTree, from GenerationID) (SendPlan, error) {
	if err := ValidateName("generation", string(generation)); err != nil {
		return SendPlan{}, err
	}
	if !ValidTransferTree(tree) {
		return SendPlan{}, fmt.Errorf("zvol: unknown transfer tree %q", tree)
	}
	if from != "" {
		if err := ValidateName("generation", string(from)); err != nil {
			return SendPlan{}, err
		}
	}
	driver := e.treeDriver(tree)
	snapshot := driver.generationDataset(generation) + "@sealed"
	if ok, err := driver.exists(ctx, snapshot); err != nil {
		return SendPlan{}, err
	} else if !ok {
		return SendPlan{}, fmt.Errorf("zvol: generation %s (%s): %w", generation, tree, ErrNotFound)
	}
	plan := SendPlan{Generation: generation, Tree: tree, Snapshot: snapshot}
	if from == "" || from == generation {
		return plan, nil
	}
	origin, err := driver.run(ctx, "get", "-H", "-o", "value", "origin", driver.generationDataset(generation))
	if err != nil {
		return SendPlan{}, err
	}
	for _, candidate := range []string{
		driver.generationDataset(from) + "@sealed",
		driver.transferDataset(from) + "@sealed",
	} {
		if strings.TrimSpace(origin) == candidate {
			plan.BaseSnapshot = candidate
			plan.Incremental = true
			break
		}
	}
	return plan, nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// Send implements TransferStore. The stream runs under the request context
// only: replication of a large generation legitimately outlives the per-verb
// zfs timeout.
func (e *Exec) Send(ctx context.Context, plan SendPlan, w io.Writer) (int64, error) {
	args := []string{"send", "-c"}
	if plan.Incremental {
		args = append(args, "-i", plan.BaseSnapshot)
	}
	args = append(args, plan.Snapshot)
	counter := &countingWriter{w: w}
	cmd := exec.CommandContext(ctx, "zfs", args...)
	cmd.Stdout = counter
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return counter.n, classify("send", stderr.String(), err)
	}
	return counter.n, nil
}

// ReceiveGeneration implements TransferStore.
func (e *Exec) ReceiveGeneration(ctx context.Context, generation GenerationID, tree TransferTree, r io.Reader) error {
	if err := ValidateName("generation", string(generation)); err != nil {
		return err
	}
	if !ValidTransferTree(tree) {
		return fmt.Errorf("zvol: unknown transfer tree %q", tree)
	}
	driver := e.treeDriver(tree)
	target := driver.transferDataset(generation)
	if ok, err := driver.exists(ctx, target+"@sealed"); err != nil {
		return err
	} else if ok {
		return nil
	}
	cmd := exec.CommandContext(ctx, "zfs", "recv", target)
	cmd.Stdin = r
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_, _ = driver.run(ctx, "destroy", "-r", target)
		return classify("recv", stderr.String(), err)
	}
	return nil
}

// GenerationState implements TransferStore.
func (e *Exec) GenerationState(ctx context.Context, generation GenerationID) (bool, bool, error) {
	if err := ValidateName("generation", string(generation)); err != nil {
		return false, false, err
	}
	resident, err := e.exists(ctx, e.generationDataset(generation)+"@sealed")
	if err != nil {
		return false, false, err
	}
	cached := true
	for _, tree := range TransferTrees {
		driver := e.treeDriver(tree)
		ok, err := driver.exists(ctx, driver.transferDataset(generation)+"@sealed")
		if err != nil {
			return false, false, err
		}
		if !ok {
			cached = false
			break
		}
	}
	return resident, cached, nil
}

// ListTransfers implements TransferStore.
func (e *Exec) ListTransfers(ctx context.Context) ([]GenerationID, error) {
	seen := map[GenerationID]bool{}
	for _, tree := range TransferTrees {
		driver := e.treeDriver(tree)
		out, err := driver.run(ctx, "list", "-H", "-o", "name", "-d", "1", driver.Root+"/xfer")
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		prefix := driver.Root + "/xfer/"
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if name, ok := strings.CutPrefix(line, prefix); ok && name != "" {
				seen[GenerationID(name)] = true
			}
		}
	}
	generations := make([]GenerationID, 0, len(seen))
	for generation := range seen {
		generations = append(generations, generation)
	}
	sort.Slice(generations, func(i, j int) bool { return generations[i] < generations[j] })
	return generations, nil
}

// DestroyTransfer implements TransferStore.
func (e *Exec) DestroyTransfer(ctx context.Context, generation GenerationID) error {
	if err := ValidateName("generation", string(generation)); err != nil {
		return err
	}
	found := false
	for _, tree := range TransferTrees {
		driver := e.treeDriver(tree)
		_, err := driver.run(ctx, "destroy", "-r", driver.transferDataset(generation))
		switch {
		case err == nil:
			found = true
		case isNotFound(err):
		default:
			return err
		}
	}
	if !found {
		return ErrNotFound
	}
	return nil
}

var _ TransferStore = (*Exec)(nil)
var _ TransferStore = (*Fake)(nil)
