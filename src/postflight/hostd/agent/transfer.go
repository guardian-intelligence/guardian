package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/guardian-intelligence/guardian/src/postflight/hostd/syncproto"
	"github.com/guardian-intelligence/guardian/src/postflight/hostd/transfer"
	"github.com/guardian-intelligence/guardian/src/postflight/hostd/zvol"
)

const (
	transferOutcomeUsed       = "used"
	transferOutcomeFailedCold = "failed-cold"
	transferOutcomeAbsent     = "absent"

	// transferBudgetCeiling bounds a fetch even when the binding deadline
	// leaves more room.
	transferBudgetCeiling = 90 * time.Second
	// transferBudgetHeadroom is reserved between the fetch bound and the
	// observed-state deadline so a timed-out transfer still materializes cold
	// and dispatches rendezvous before the assignment fails on time.
	transferBudgetHeadroom = 5 * time.Second
)

var errTransferAbsent = errors.New("source no longer holds the generation")

// transferState is one assignment's fetch attempt. The fetch goroutine owns
// the writes; convergence reads it under its own lock so a long pull never
// blocks the tick loop.
type transferState struct {
	mu          sync.Mutex
	done        bool
	outcome     string
	reason      string
	bytes       int64
	millis      int64
	incremental bool
}

func settledTransfer(outcome, reason string) *transferState {
	return &transferState{done: true, outcome: outcome, reason: reason}
}

func (t *transferState) settled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.done
}

func (t *transferState) report() *syncproto.TransferReport {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.outcome == "" {
		return nil
	}
	return &syncproto.TransferReport{
		Outcome: t.outcome, Bytes: t.bytes, Millis: t.millis, Incremental: t.incremental,
	}
}

// sanitizeTransfer drops a transfer routing the agent cannot act on. The
// spec stays valid without it — transfer is acceleration, never a
// precondition — so a malformed hint degrades to a cold build instead of
// quarantining the job.
func (a *Agent) sanitizeTransfer(spec *syncproto.DesiredAssignment) {
	hint := spec.Transfer
	if hint == nil {
		return
	}
	reason := ""
	switch {
	case hint.Generation == "" || hint.Generation != spec.Workspace.Generation:
		reason = "transfer generation differs from the workspace generation"
	case zvol.ValidateName("generation", hint.Generation) != nil:
		reason = "transfer generation is not a safe dataset name"
	case hint.Base != "" && zvol.ValidateName("generation", hint.Base) != nil:
		reason = "transfer base is not a safe dataset name"
	default:
		origin, err := url.Parse(hint.Origin)
		if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" {
			reason = "transfer origin is not an http(s) origin"
		}
	}
	if reason != "" {
		a.logger.Error("dropping unusable transfer routing",
			"assignment_id", spec.AssignmentID, "origin", hint.Origin,
			"generation", hint.Generation, "reason", reason)
		spec.Transfer = nil
	}
}

// ensureTransferred reports whether materialization may proceed. When the
// assignment routes at a remote generation that is neither resident nor
// cached, it starts one bounded background fetch and holds the assignment in
// observed until the fetch settles; every failure settles as a cold build.
func (a *Agent) ensureTransferred(ctx context.Context, record *assignment) bool {
	spec := record.spec
	if spec.Transfer == nil || spec.Workspace.Generation == "" {
		return true
	}
	if record.transfer != nil {
		return record.transfer.settled()
	}
	store, ok := a.zvols.(zvol.TransferStore)
	if !ok {
		record.transfer = settledTransfer(transferOutcomeFailedCold, "durable-volume driver has no transfer surface")
		return true
	}
	generation := zvol.GenerationID(spec.Workspace.Generation)
	resident, cached, err := store.GenerationState(ctx, generation)
	switch {
	case err != nil:
		record.transfer = settledTransfer(transferOutcomeFailedCold, "query generation state: "+err.Error())
		return true
	case resident:
		record.transfer = settledTransfer("", "")
		return true
	case cached:
		record.transfer = settledTransfer(transferOutcomeUsed, "")
		return true
	}
	budget := a.transferBudget(record)
	if budget <= 0 {
		record.transfer = settledTransfer(transferOutcomeFailedCold, "no transfer budget left before the binding deadline")
		return true
	}
	state := &transferState{}
	record.transfer = state
	a.appendTrace(record.trace, record, "generation_transfer_started", func(event *traceEvent) {
		event.Transfer = &traceTransfer{
			Origin: spec.Transfer.Origin, Generation: spec.Transfer.Generation, Base: spec.Transfer.Base,
		}
	})
	go a.runTransfer(ctx, store, *spec.Transfer, generation, budget, state)
	return false
}

func (a *Agent) transferBudget(record *assignment) time.Duration {
	deadline := assignmentDeadlines[syncproto.AssignmentObserved]
	remaining := deadline - a.now().Sub(record.since) - transferBudgetHeadroom
	if remaining > transferBudgetCeiling {
		return transferBudgetCeiling
	}
	return remaining
}

func (a *Agent) runTransfer(ctx context.Context, store zvol.TransferStore, spec syncproto.TransferSpec, generation zvol.GenerationID, budget time.Duration, state *transferState) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	outcome, reason := transferOutcomeUsed, ""
	var total int64
	incremental := false
	for _, tree := range zvol.TransferTrees {
		bytes, treeIncremental, err := a.fetchTransferTree(ctx, store, spec, generation, tree)
		total += bytes
		if tree == zvol.TreeWorkspace {
			incremental = treeIncremental
		}
		if err != nil {
			outcome, reason = transferOutcomeFailedCold, fmt.Sprintf("%s tree: %s", tree, err.Error())
			if errors.Is(err, errTransferAbsent) {
				outcome = transferOutcomeAbsent
			}
			break
		}
	}
	elapsed := time.Since(started)
	state.mu.Lock()
	state.done = true
	state.outcome = outcome
	state.reason = reason
	state.bytes = total
	state.millis = elapsed.Milliseconds()
	state.incremental = incremental
	state.mu.Unlock()
	a.logger.Info("postflight.hostd.transfer.settled",
		"generation", string(generation), "origin", spec.Origin, "outcome", outcome,
		"bytes", total, "incremental", incremental,
		"duration_ns", elapsed.Nanoseconds(), "reason", reason)
}

func (a *Agent) fetchTransferTree(ctx context.Context, store zvol.TransferStore, spec syncproto.TransferSpec, generation zvol.GenerationID, tree zvol.TransferTree) (int64, bool, error) {
	endpoint, err := url.JoinPath(spec.Origin, transfer.GenerationPathPrefix, string(generation))
	if err != nil {
		return 0, false, err
	}
	query := url.Values{transfer.TreeParam: []string{string(tree)}}
	if spec.Base != "" {
		query.Set(transfer.FromParam, spec.Base)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return 0, false, err
	}
	request.Header.Set("Authorization", "Bearer "+a.credential)
	response, err := a.transferHTTP.Do(request)
	if err != nil {
		return 0, false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return 0, false, errTransferAbsent
	}
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return 0, false, fmt.Errorf("source returned %s: %s", response.Status, detail)
	}
	incremental := response.Header.Get(transfer.IncrementalHeader) == "true"
	counter := &countingReader{r: response.Body}
	if err := store.ReceiveGeneration(ctx, generation, tree, counter); err != nil {
		return counter.n, incremental, err
	}
	return counter.n, incremental, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// finalizeTransferEvidence publishes the settled fetch into the assignment's
// report exactly once.
func (a *Agent) finalizeTransferEvidence(record *assignment) {
	if record.transfer == nil || record.transferReport != nil {
		return
	}
	report := record.transfer.report()
	if report == nil {
		return
	}
	record.transferReport = report
	record.transfer.mu.Lock()
	reason := record.transfer.reason
	record.transfer.mu.Unlock()
	event := "generation_transfer_completed"
	if report.Outcome == transferOutcomeUsed {
		a.metrics.WarmTransfers.Add(1)
	} else {
		event = "generation_transfer_failed"
		record.coldProcess = true
		a.metrics.FailedTransfers.Add(1)
	}
	spec := record.spec.Transfer
	a.appendTrace(record.trace, record, event, func(traced *traceEvent) {
		traced.FailureReason = reason
		traced.Transfer = &traceTransfer{
			Outcome: report.Outcome, Bytes: report.Bytes, Millis: report.Millis,
			Incremental: report.Incremental,
		}
		if spec != nil {
			traced.Transfer.Origin = spec.Origin
			traced.Transfer.Generation = spec.Generation
			traced.Transfer.Base = spec.Base
		}
	})
}
