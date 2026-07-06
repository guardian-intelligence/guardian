package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"log/slog"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// maxWebhookBytes bounds the raw webhook body (1 MiB).
const maxWebhookBytes = 1 << 20

// webhookServer is the synchronous half of ingest: verify, ledger, ack.
// Nothing else runs inline — GitHub gives a 10-second response budget, so all
// GitHub API reads and demand work happen in the async worker.
type webhookServer struct {
	secret []byte
	inbox  inboxStore
	tracer trace.Tracer
	now    func() time.Time
}

// singleHeader returns the header value only when it is present EXACTLY once
// with a non-empty trimmed value; duplicates are malformed, not first-wins.
func singleHeader(h http.Header, name string) (string, bool) {
	vals := h.Values(name)
	if len(vals) != 1 {
		return "", false
	}
	v := strings.TrimSpace(vals[0])
	return v, v != ""
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// verifyGitHubSignature checks X-Hub-Signature-256: HMAC-SHA256 over the
// EXACT raw bytes, constant-time compare. Runs BEFORE any JSON parse —
// unauthenticated payloads are never parsed.
func verifyGitHubSignature(secret, body []byte, header string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	claimed, err := hex.DecodeString(header[len(prefix):])
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(claimed, mac.Sum(nil))
}

// webhookEnvelope is the minimal parse for the ledger row: identifiers only,
// the full payload is parsed later by the worker.
type webhookEnvelope struct {
	Action       string `json:"action"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
	WorkflowJob struct {
		ID         int64     `json:"id"`
		RunID      int64     `json:"run_id"`
		RunAttempt int64     `json:"run_attempt"`
		CreatedAt  time.Time `json:"created_at"`
		StartedAt  time.Time `json:"started_at"`
	} `json:"workflow_job"`
}

func (s *webhookServer) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx, span := s.tracer.Start(r.Context(), "github.webhook")
	defer span.End()
	receivedAt := s.now().UTC()

	// Best-effort delivery id up front: even rejected requests are ledgered
	// when GitHub identified them.
	deliveryID, _ := singleHeader(r.Header, "X-GitHub-Delivery")

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		s.reject(ctx, w, deliveryID, "", sha256Hex(nil), receivedAt, []problem{problemMethodNotAllowed()})
		return
	}

	var problems []problem
	eventName, ok := singleHeader(r.Header, "X-GitHub-Event")
	if !ok {
		problems = append(problems, problemHeaderInvalid("X-GitHub-Event"))
	}
	if _, ok := singleHeader(r.Header, "X-GitHub-Delivery"); !ok {
		problems = append(problems, problemHeaderInvalid("X-GitHub-Delivery"))
	}
	sigHeader, sigOK := singleHeader(r.Header, "X-Hub-Signature-256")
	if !sigOK {
		problems = append(problems, problemHeaderInvalid("X-Hub-Signature-256"))
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		problems = append(problems, problemBodyInvalid("request body unreadable or too large", status))
	}
	payloadSHA := sha256Hex(body) // always computed, even for rejected deliveries

	emitEvent(ctx, evWebhookReceived, eventAttrs{DeliveryID: deliveryID, Result: "received"})

	if len(problems) > 0 {
		s.reject(ctx, w, deliveryID, eventName, payloadSHA, receivedAt, problems)
		return
	}
	if !verifyGitHubSignature(s.secret, body, sigHeader) {
		s.reject(ctx, w, deliveryID, eventName, payloadSHA, receivedAt, []problem{problemSignatureInvalid()})
		return
	}
	var env webhookEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		s.reject(ctx, w, deliveryID, eventName, payloadSHA, receivedAt, []problem{problemPayloadInvalid(err.Error())})
		return
	}

	// GitHub-side lag, when the payload carries a creation hint.
	if hint := firstNonZeroTime(env.WorkflowJob.CreatedAt, env.WorkflowJob.StartedAt); !hint.IsZero() {
		if lag := receivedAt.Sub(hint); lag > 0 {
			span.SetAttributes(attribute.Int64("github.lag_ms", lag.Milliseconds()))
		}
	}

	attempt := env.WorkflowJob.RunAttempt
	if attempt == 0 && env.WorkflowJob.ID != 0 {
		attempt = 1 // run_attempt may be absent in payloads; exact-attempt everywhere
	}
	ack, err := s.inbox.RecordWebhookDelivery(ctx, deliveryEnvelope{
		DeliveryID:             deliveryID,
		EventName:              eventName,
		Action:                 env.Action,
		PayloadSHA256:          payloadSHA,
		PayloadJSON:            body,
		ProviderInstallationID: env.Installation.ID,
		ProviderRepositoryID:   env.Repository.ID,
		RepositoryFullName:     env.Repository.FullName,
		ProviderRunID:          env.WorkflowJob.RunID,
		ProviderRunAttempt:     attempt,
		ProviderJobID:          env.WorkflowJob.ID,
		ReceivedAt:             receivedAt,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Same delivery GUID, different payload hash: a replay/forgery
		// signal. Deliberately a 409 security event, never an overwrite and
		// never a retry invitation.
		emitEvent(ctx, evWebhookRejected, eventAttrs{DeliveryID: deliveryID, Result: "failed", Reason: "delivery_replay_conflict"})
		writeProblems(w, []problem{problemReplayConflict()})
		return
	case err != nil:
		// Transient inbox failure: 503 so GitHub redelivers.
		slog.Error("webhook: inbox unavailable", "delivery_id", deliveryID, "err", err)
		writeProblems(w, []problem{problemInboxUnavailable()})
		return
	}

	status := "accepted"
	if ack.State != stateAccepted && ack.State != stateRetryable {
		status = "duplicate"
	}
	emitEvent(ctx, evWebhookVerified, eventAttrs{
		DeliveryID: ack.DeliveryID,
		Repo:       env.Repository.FullName,
		RunID:      env.WorkflowJob.RunID,
		RunAttempt: attempt,
		JobID:      env.WorkflowJob.ID,
		Result:     status,
	})
	if status == "accepted" {
		emitEvent(ctx, evDeliveryEnqueued, eventAttrs{DeliveryID: ack.DeliveryID, Result: "succeeded"})
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"accepted": map[string]any{"status": status, "delivery_id": ack.DeliveryID},
	})
}

// reject records the rejected delivery (hash + problems, never the raw body)
// when a delivery id exists — so GitHub's manual redelivery can resurrect and
// self-heal it — then writes the RFC-7807 response.
func (s *webhookServer) reject(ctx context.Context, w http.ResponseWriter, deliveryID, eventName, payloadSHA string, receivedAt time.Time, problems []problem) {
	if deliveryID != "" {
		err := s.inbox.RecordRejectedDelivery(ctx, deliveryEnvelope{
			DeliveryID:    deliveryID,
			EventName:     eventName,
			PayloadSHA256: payloadSHA,
			ReceivedAt:    receivedAt,
		}, problems)
		if err != nil {
			// Best-effort by design: the response still tells GitHub to back off.
			slog.Error("webhook: record rejected delivery", "delivery_id", deliveryID, "err", err)
		}
	}
	emitEvent(ctx, evWebhookRejected, eventAttrs{DeliveryID: deliveryID, Result: "failed", Reason: problems[0].Code})
	writeProblems(w, problems)
}

func firstNonZeroTime(times ...time.Time) time.Time {
	for _, t := range times {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}
