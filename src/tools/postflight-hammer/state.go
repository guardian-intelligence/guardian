package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// stateFile is the battery's single artifact, shared by the three
// subcommands: dispatch appends what it fired, watch folds in everything it
// observed (GitHub and database), report reads it and never touches the
// network. Timestamps recorded by watch are poll-resolution observations;
// timestamps copied from GitHub or the database are authoritative.
type stateFile struct {
	Repo         string    `json:"repo"`
	Workflow     string    `json:"workflow"`
	TwinWorkflow string    `json:"twin_workflow,omitempty"`
	Ref          string    `json:"ref"`
	StartedAt    time.Time `json:"started_at"`

	Dispatches []dispatchRecord `json:"dispatches"`
	Churn      []churnRecord    `json:"churn,omitempty"`
	RestartAt  *time.Time       `json:"restart_at,omitempty"`

	Baseline *dbSnapshot           `json:"baseline,omitempty"`
	Runs     map[string]*runRecord `json:"runs"`
	DB       *dbObservations       `json:"db,omitempty"`

	WatchDoneAt *time.Time `json:"watch_done_at,omitempty"`
}

type dispatchRecord struct {
	Pattern      string    `json:"pattern"`
	Workflow     string    `json:"workflow"`
	Twin         bool      `json:"twin,omitempty"`
	DispatchedAt time.Time `json:"dispatched_at"`
}

// churnRecord tracks one dispatch that was cancelled mid-flight and re-run.
type churnRecord struct {
	RunID           int64      `json:"run_id"`
	CancelledAt     time.Time  `json:"cancelled_at"`
	CancelAttempt   int64      `json:"cancel_attempt"`
	RerunAt         *time.Time `json:"rerun_at,omitempty"`
	CancelConfirmed bool       `json:"cancel_confirmed,omitempty"`
}

type runRecord struct {
	RunID         int64                     `json:"run_id"`
	Workflow      string                    `json:"workflow"`
	Twin          bool                      `json:"twin,omitempty"`
	CreatedAt     time.Time                 `json:"created_at"`
	LatestAttempt int64                     `json:"latest_attempt"`
	Status        string                    `json:"status"`
	Conclusion    string                    `json:"conclusion"`
	Attempts      map[string]*attemptRecord `json:"attempts"`
}

func (r *runRecord) attempt(n int64) *attemptRecord {
	if r.Attempts == nil {
		r.Attempts = map[string]*attemptRecord{}
	}
	key := fmt.Sprintf("%d", n)
	a := r.Attempts[key]
	if a == nil {
		a = &attemptRecord{Attempt: n}
		r.Attempts[key] = a
	}
	return a
}

type attemptRecord struct {
	Attempt     int64       `json:"attempt"`
	Status      string      `json:"status"`
	Conclusion  string      `json:"conclusion"`
	StartedAt   time.Time   `json:"started_at"`
	Jobs        []jobRecord `json:"jobs,omitempty"`
	LogsFetched bool        `json:"logs_fetched,omitempty"`
	// StepLogBytes maps "<job name>/<step number>" to the step's log size in
	// the attempt's log archive, recorded once the attempt is terminal.
	StepLogBytes map[string]int64 `json:"step_log_bytes,omitempty"`
}

func (a *attemptRecord) terminal() bool { return a.Status == "completed" }

type jobRecord struct {
	JobID       int64        `json:"job_id"`
	Name        string       `json:"name"`
	Status      string       `json:"status"`
	Conclusion  string       `json:"conclusion"`
	RunnerName  string       `json:"runner_name,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	StartedAt   time.Time    `json:"started_at"`
	CompletedAt time.Time    `json:"completed_at"`
	Steps       []stepRecord `json:"steps,omitempty"`
}

type stepRecord struct {
	Number      int64      `json:"number"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// dbSnapshot is a point-in-time capture of the control plane's accounting
// tables; dispatch records the baseline, watch records the final state.
type dbSnapshot struct {
	CapturedAt  time.Time       `json:"captured_at"`
	Slots       []slotRow       `json:"slots"`
	Generations []generationRow `json:"generations"`
}

type dbObservations struct {
	Demands     map[string]demandRow `json:"demands"`
	Leases      map[string]leaseRow  `json:"leases"`
	Deliveries  map[string]time.Time `json:"deliveries"`
	Transitions []transition         `json:"transitions"`
	Final       *dbSnapshot          `json:"final,omitempty"`
}

// transition is the first time watch observed an entity's field holding a
// value. Poll-resolution: the true transition happened at most one poll
// interval earlier.
type transition struct {
	Kind       string    `json:"kind"` // demand | lease | generation
	ID         string    `json:"id"`
	Field      string    `json:"field"`
	Value      string    `json:"value"`
	ObservedAt time.Time `json:"observed_at"`
}

func (o *dbObservations) observedAt(kind, id, field, value string) (time.Time, bool) {
	for _, t := range o.Transitions {
		if t.Kind == kind && t.ID == id && t.Field == field && t.Value == value {
			return t.ObservedAt, true
		}
	}
	return time.Time{}, false
}

func loadState(path string) (*stateFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st stateFile
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, fmt.Errorf("state file %s: %w", path, err)
	}
	if st.Runs == nil {
		st.Runs = map[string]*runRecord{}
	}
	return &st, nil
}

// loadOrInitState returns the existing state when it matches repo/workflow
// (patterns accumulate into one battery) and refuses a mismatched reuse
// instead of silently mixing two batteries in one file.
func loadOrInitState(path, repo, workflow string, now time.Time) (*stateFile, error) {
	st, err := loadState(path)
	if errors.Is(err, os.ErrNotExist) {
		return &stateFile{
			Repo:      repo,
			Workflow:  workflow,
			StartedAt: now,
			Runs:      map[string]*runRecord{},
		}, nil
	}
	if err != nil {
		return nil, err
	}
	if st.Repo != repo || st.Workflow != workflow {
		return nil, fmt.Errorf("state file %s belongs to %s %s; delete it or pass a fresh -state path",
			path, st.Repo, st.Workflow)
	}
	return st, nil
}

// lockState holds an exclusive advisory lock on the battery for the life of
// the subcommand. Every subcommand does whole-file load-modify-save, so two
// running at once (a dispatch fired while a watch still polls) would silently
// discard each other's records.
func lockState(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("state file %s is in use by another hammer subcommand", path)
	}
	return func() { f.Close() }, nil
}

func saveState(path string, st *stateFile) error {
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Clean(path))
}
