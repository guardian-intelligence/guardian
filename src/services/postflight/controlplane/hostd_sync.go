package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
)

const maxSyncRequestBytes = 4 << 20

type syncServer struct {
	st          *pgStore
	secret      []byte
	sealTimeout time.Duration
	tracer      trace.Tracer
	jobPlans    *jobPlanBus
}

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
	started := time.Now()
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
	decodeStarted := time.Now()
	var request syncproto.SyncRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxSyncRequestBytes)).Decode(&request); err != nil {
		writeProblems(w, []problem{problemSyncPayloadInvalid("sync request does not parse: " + err.Error())})
		return
	}
	if request.HostID == "" || request.BootID == "" {
		writeProblems(w, []problem{problemSyncPayloadInvalid("host_id and boot_id are required")})
		return
	}
	decodeElapsed := time.Since(decodeStarted)
	span.SetAttributes(attribute.String("host_id", request.HostID),
		attribute.Int("members", len(request.Members)), attribute.Int("assignments", len(request.Assignments)))

	hostStarted := time.Now()
	if err := s.st.UpsertHostSync(ctx, request.HostID, request.BootID, request.Slots); err != nil {
		s.syncError(w, request.HostID, "host inventory", err)
		return
	}
	hostElapsed := time.Since(hostStarted)

	generationStarted := time.Now()
	if err := s.st.ObserveHostGenerations(ctx, request.HostID, request.Generations); err != nil {
		s.syncError(w, request.HostID, "generation inventory", err)
		return
	}
	generationElapsed := time.Since(generationStarted)

	intentStarted := time.Now()
	if _, err := s.st.EnsureJobIntents(ctx); err != nil {
		s.syncError(w, request.HostID, "job intents", err)
		return
	}
	intentElapsed := time.Since(intentStarted)

	membersStarted := time.Now()
	if err := s.st.ApplyHostMembers(ctx, request.HostID, request.Members); err != nil {
		s.syncError(w, request.HostID, "pool members", err)
		return
	}
	membersElapsed := time.Since(membersStarted)

	assignmentsStarted := time.Now()
	for _, report := range request.Assignments {
		if err := s.st.ApplyAssignmentReport(ctx, request.HostID, report, time.Now().Add(s.sealTimeout)); err != nil {
			s.syncError(w, request.HostID, "assignment report", err)
			return
		}
	}
	assignmentsElapsed := time.Since(assignmentsStarted)

	desiredStarted := time.Now()
	response, err := s.desiredState(ctx, request)
	if err != nil {
		s.syncError(w, request.HostID, "desired state", err)
		return
	}
	desiredElapsed := time.Since(desiredStarted)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	encodeStarted := time.Now()
	_ = json.NewEncoder(w).Encode(response)
	encodeElapsed := time.Since(encodeStarted)

	span.SetAttributes(
		attribute.Int64("postflight.decode_ns", decodeElapsed.Nanoseconds()),
		attribute.Int64("postflight.host_ingest_ns", hostElapsed.Nanoseconds()),
		attribute.Int64("postflight.generation_ingest_ns", generationElapsed.Nanoseconds()),
		attribute.Int64("postflight.intent_ingest_ns", intentElapsed.Nanoseconds()),
		attribute.Int64("postflight.member_reports_ns", membersElapsed.Nanoseconds()),
		attribute.Int64("postflight.assignment_reports_ns", assignmentsElapsed.Nanoseconds()),
		attribute.Int64("postflight.desired_state_ns", desiredElapsed.Nanoseconds()),
		attribute.Int64("postflight.encode_ns", encodeElapsed.Nanoseconds()),
	)
	slog.Info("postflight.controlplane.hostd_sync.completed",
		"host_id", request.HostID, "duration_ns", time.Since(started).Nanoseconds(),
		"decode_ns", decodeElapsed.Nanoseconds(), "host_ingest_ns", hostElapsed.Nanoseconds(),
		"generation_ingest_ns", generationElapsed.Nanoseconds(), "intent_ingest_ns", intentElapsed.Nanoseconds(),
		"member_reports_ns", membersElapsed.Nanoseconds(), "assignment_reports_ns", assignmentsElapsed.Nanoseconds(),
		"desired_state_ns", desiredElapsed.Nanoseconds(), "encode_ns", encodeElapsed.Nanoseconds(),
		"reported_members", len(request.Members), "reported_assignments", len(request.Assignments),
		"desired_members", len(response.Members), "desired_assignments", len(response.Assignments))
}

func (s *syncServer) syncError(w http.ResponseWriter, hostID, operation string, err error) {
	slog.Error("hostd sync", "host_id", hostID, "operation", operation, "err", err)
	writeProblems(w, []problem{problemSyncUnavailable()})
}

func (s *syncServer) desiredState(ctx context.Context, request syncproto.SyncRequest) (syncproto.SyncResponse, error) {
	response := syncproto.SyncResponse{BootID: request.BootID}
	reportedMembers := make([]string, 0, len(request.Members))
	for _, member := range request.Members {
		if member.MemberID != "" {
			reportedMembers = append(reportedMembers, member.MemberID)
		}
	}
	members, err := s.st.ListDesiredMembers(ctx, request.HostID, reportedMembers)
	if err != nil {
		return response, err
	}
	for _, row := range members {
		state := syncproto.DesiredMemberListen
		if row.State == "recycle" {
			state = syncproto.DesiredMemberRecycle
		}
		response.Members = append(response.Members, syncproto.DesiredPoolMember{
			MemberID: row.MemberID, VMID: row.VMID, State: state,
			RunnerName: row.RunnerName, RunnerClass: row.RunnerClass, JITConfig: row.JITConfig,
		})
	}
	assignments, err := s.st.ListDesiredAssignments(ctx, request.HostID)
	if err != nil {
		return response, err
	}
	for _, row := range assignments {
		org, _, _ := strings.Cut(row.Repository, "/")
		desiredState := syncproto.DesiredAssignmentRun
		if row.State == "sealing" {
			desiredState = syncproto.DesiredAssignmentSeal
		}
		desired := syncproto.DesiredAssignment{
			AssignmentID: row.AssignmentID, MemberID: row.MemberID,
			RequestID: row.RequestID, JobID: row.ProtocolJobID, CheckRunID: row.CheckRunID, State: desiredState,
			ExecutionID: strconv.FormatInt(row.ProviderJobID, 10), AttemptID: strconv.Itoa(row.RunAttempt),
			OrgID: org, InstallationID: row.InstallationID, RepositoryID: row.RepositoryID,
			RepositoryFullName: row.Repository, RunnerClass: row.RunnerClass,
			Identity: syncproto.JobIdentity{
				RunID: row.RunID, RunAttempt: row.RunAttempt, RunnerName: row.RunnerName,
				Repository: row.Repository, WorkflowJob: row.WorkflowJob,
			},
			Workspace: syncproto.WorkspaceSpec{Generation: row.ScopeGeneration, SizeBytes: row.WorkspaceBytes},
			Tool:      syncproto.WorkspaceSpec{Generation: row.ScopeGeneration, SizeBytes: row.ToolBytes},
			Process:   syncproto.ProcessSpec{SizeBytes: row.ProcessBytes},
		}
		if row.ScopeGeneration != "" && row.ProcessDigest != "" && row.ProcessVersion != "" {
			desired.Process.Generation = row.ScopeGeneration
			desired.Process.ExpectedDigest = row.ProcessDigest
			desired.Process.ExpectedVersion = row.ProcessVersion
		}
		if desiredState == syncproto.DesiredAssignmentSeal {
			desired.SealGeneration = row.SealGeneration
			desired.SealCheckpoint = &syncproto.CheckpointArtifact{
				Digest: row.CheckpointDigest, Version: row.CheckpointVersion,
			}
		}
		response.Assignments = append(response.Assignments, desired)
	}
	if response.Reap, err = s.st.ListReapGenerations(ctx, request.HostID); err != nil {
		return response, err
	}
	if response.PoolTargets, err = s.st.ListHostPoolTargets(ctx, request.HostID); err != nil {
		return response, err
	}
	return response, nil
}
