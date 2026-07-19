package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
)

// maxSyncRequestBytes bounds one host's full-state report (4 MiB).
const maxSyncRequestBytes = 4 << 20

// syncServer serves the hostd sync exchange: ingest the host's full
// observed state, project the full desired state back. Everything is
// level-triggered — the response is recomputed from the database on every
// exchange, and a terminal lease is acknowledged by simply no longer being
// part of it.
type syncServer struct {
	st          *pgStore
	secret      []byte
	sealTimeout time.Duration
	tracer      trace.Tracer
}

// authorized does a constant-time bearer comparison via SHA-256 digests so
// neither content nor length differences leak through timing.
func (s *syncServer) authorized(r *http.Request) bool {
	header, ok := singleHeader(r.Header, "Authorization")
	if !ok || !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	expected := sha256.Sum256(s.secret)
	return subtle.ConstantTimeCompare(presented[:], expected[:]) == 1
}

func (s *syncServer) handleSync(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.tracer.Start(r.Context(), "hostd.sync")
	defer span.End()

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeProblems(w, []problem{problemMethodNotAllowed()})
		return
	}
	if !s.authorized(r) {
		writeProblems(w, []problem{problemSyncUnauthorized()})
		return
	}

	var request syncproto.SyncRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSyncRequestBytes)).Decode(&request); err != nil {
		writeProblems(w, []problem{problemSyncPayloadInvalid("sync request does not parse: " + err.Error())})
		return
	}
	if request.HostID == "" || request.BootID == "" {
		writeProblems(w, []problem{problemSyncPayloadInvalid("host_id and boot_id are required")})
		return
	}
	span.SetAttributes(
		attribute.String("host_id", request.HostID),
		attribute.Int("leases", len(request.Leases)),
	)

	if err := s.st.UpsertHostSync(ctx, request.HostID, request.BootID, request.Slots); err != nil {
		slog.Error("hostd sync: host ingest", "host_id", request.HostID, "err", err)
		writeProblems(w, []problem{problemSyncUnavailable()})
		return
	}
	if err := s.st.ObserveHostGenerations(ctx, request.HostID, request.Generations); err != nil {
		slog.Error("hostd sync: generation ingest", "host_id", request.HostID, "err", err)
		writeProblems(w, []problem{problemSyncUnavailable()})
		return
	}
	for _, report := range request.Leases {
		if err := s.applyLeaseReport(ctx, request.HostID, report); err != nil {
			slog.Error("hostd sync: lease report", "host_id", request.HostID, "lease_id", report.LeaseID, "err", err)
			writeProblems(w, []problem{problemSyncUnavailable()})
			return
		}
	}

	response, err := s.desiredState(ctx, request)
	if err != nil {
		slog.Error("hostd sync: desired state", "host_id", request.HostID, "err", err)
		writeProblems(w, []problem{problemSyncUnavailable()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// applyLeaseReport advances the control plane's lease record on one host
// report. Guarded transitions make replays no-ops; reports for leases the
// control plane does not know (or no longer wants) are ignored — omission
// from the desired set is what tells the host to collect them.
func (s *syncServer) applyLeaseReport(ctx context.Context, hostID string, report syncproto.LeaseReport) error {
	if report.LeaseID == "" {
		return nil
	}
	executionLeaseID := report.ExecutionLeaseID
	if executionLeaseID == "" {
		executionLeaseID = report.LeaseID
	}
	attrs := eventAttrs{LeaseID: executionLeaseID, HostID: hostID}
	switch report.State {
	case syncproto.StateListening, syncproto.StateReady:
		ready, err := s.st.MarkLeaseReady(ctx, hostID, report.LeaseID)
		if err != nil {
			return err
		}
		if ready {
			attrs.Result = "succeeded"
			emitEvent(ctx, evLeaseReady, attrs)
		}
		return nil
	case syncproto.StateHookBlocked:
		if report.Identity != nil {
			slog.Info("postflight.rendezvous.hook_blocked",
				"lease_id", report.LeaseID, "host_id", hostID,
				"run_id", report.Identity.RunID, "run_attempt", report.Identity.RunAttempt,
				"runner_name", report.Identity.RunnerName, "repo", report.Identity.Repository)
		}
		return nil
	case syncproto.StateExited:
		jobID, sealGeneration, completed, err := s.st.CompleteRoutedLease(
			ctx, hostID, report.LeaseID, executionLeaseID,
			report.ExitCode, time.Now().Add(s.sealTimeout))
		if err != nil {
			return err
		}
		if completed {
			attrs.JobID = jobID
			attrs.Result, attrs.Reason = "succeeded", fmt.Sprintf("exit_code:%d", report.ExitCode)
			emitEvent(ctx, evLeaseCompleted, attrs)
			if sealGeneration != "" {
				attrs.Generation = sealGeneration
				emitEvent(ctx, evGenerationSealRequested, attrs)
			}
		}
		return nil
	case syncproto.StateSealed:
		if report.SealedGeneration == "" {
			return nil
		}
		jobID, sealed, err := s.st.RecordRoutedLeaseSealed(
			ctx, hostID, report.LeaseID, executionLeaseID,
			report.SealedGeneration)
		if err != nil {
			return err
		}
		if sealed {
			attrs.JobID = jobID
			attrs.Generation = report.SealedGeneration
			attrs.Result = "succeeded"
			emitEvent(ctx, evLeaseSealed, attrs)
		}
		return nil
	case syncproto.StateFailed, syncproto.StateCancelled:
		reason := report.Reason
		if reason == "" {
			reason = string(report.State) + " on host"
		}
		jobID, failed, err := s.st.FailRoutedLeaseFromHost(
			ctx, hostID, report.LeaseID, executionLeaseID,
			string(report.State), reason,
			[]problem{problemSandboxFailed(reason)})
		if err != nil {
			return err
		}
		if failed {
			attrs.JobID = jobID
			attrs.Result, attrs.Reason = "failed", reason
			emitEvent(ctx, evLeaseFailed, attrs)
			return nil
		}
		// A failure after the runner already exited green is a lost seal,
		// not a job failure: discard the candidate and keep the demand's
		// completed verdict.
		sealFailed, err := s.st.FailRoutedSealingLease(
			ctx, hostID, report.LeaseID, executionLeaseID,
			string(report.State), reason)
		if err != nil {
			return err
		}
		if sealFailed {
			attrs.Result, attrs.Reason = "failed", reason
			emitEvent(ctx, evLeaseSealFailed, attrs)
		}
		return nil
	default:
		return s.st.RecordLeaseReportedState(ctx, hostID, report.LeaseID, string(report.State))
	}
}

// desiredState projects the host's full desired set. The BootID echo binds
// the response to the exact request it was computed for; hostd drops
// anything else.
func (s *syncServer) desiredState(ctx context.Context, request syncproto.SyncRequest) (syncproto.SyncResponse, error) {
	response := syncproto.SyncResponse{BootID: request.BootID}
	leases, err := s.st.ListDesiredLeases(ctx, request.HostID)
	if err != nil {
		return response, err
	}
	for _, row := range leases {
		desired := syncproto.DesiredLease{
			LeaseID:              row.LeaseID,
			ExecutionLeaseID:     row.ExecutionLeaseID,
			State:                syncproto.DesiredRun,
			ExecutionID:          row.ExecutionID,
			AttemptID:            row.AttemptID,
			OrgID:                row.OrgID,
			InstallationID:       row.InstallationID,
			RepositoryID:         row.RepositoryID,
			RepositoryFullName:   row.RepositoryFullName,
			RunnerClass:          row.RunnerClass,
			JITConfig:            row.JITConfig,
			ProviderRunID:        row.ProviderRunID,
			ProviderJobID:        row.ProviderJobID,
			ProviderRunAttempt:   int(row.ProviderRunAttempt),
			AssignedRunnerName:   row.AssignedRunnerName,
			RendezvousAuthorized: row.RendezvousAuthorized,
			Workspace: syncproto.WorkspaceSpec{
				Generation: row.Generation,
				SizeBytes:  row.SizeBytes,
			},
		}
		if row.State == leaseSealing {
			desired.State = syncproto.DesiredSeal
			desired.SealGeneration = row.SealGeneration
		}
		response.Leases = append(response.Leases, desired)
	}
	if response.Reap, err = s.st.ListReapGenerations(ctx, request.HostID); err != nil {
		return response, err
	}
	if response.PoolTargets, err = s.st.ListHostPoolTargets(ctx, request.HostID); err != nil {
		return response, err
	}
	return response, nil
}
