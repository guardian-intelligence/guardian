package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/trace/noop"
)

const testSecret = "webhook-test-secret"

type fakeInbox struct {
	ackState         string
	recordErr        error
	recorded         []deliveryEnvelope
	rejected         []deliveryEnvelope
	rejectedProblems [][]problem
}

func (f *fakeInbox) RecordWebhookDelivery(_ context.Context, env deliveryEnvelope) (deliveryAck, error) {
	if f.recordErr != nil {
		return deliveryAck{}, f.recordErr
	}
	f.recorded = append(f.recorded, env)
	state := f.ackState
	if state == "" {
		state = stateAccepted
	}
	return deliveryAck{DeliveryID: env.DeliveryID, State: state, PayloadSHA256: env.PayloadSHA256}, nil
}

func (f *fakeInbox) RecordRejectedDelivery(_ context.Context, env deliveryEnvelope, problems []problem) error {
	f.rejected = append(f.rejected, env)
	f.rejectedProblems = append(f.rejectedProblems, problems)
	return nil
}

func newTestWebhookServer(f *fakeInbox) *webhookServer {
	return &webhookServer{
		secret: []byte(testSecret),
		inbox:  f,
		tracer: noop.NewTracerProvider().Tracer("test"),
		now:    time.Now,
	}
}

func signBody(body []byte) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

var testPayload = []byte(`{"action":"queued","installation":{"id":42},"repository":{"id":7,"full_name":"guardian-intelligence/guardian"},"workflow_job":{"id":100,"run_id":200,"run_attempt":0,"labels":["verself-4cpu-16gb"]}}`)

type reqOpt func(*http.Request)

func withoutHeader(name string) reqOpt {
	return func(r *http.Request) { r.Header.Del(name) }
}

func withExtraHeader(name, value string) reqOpt {
	return func(r *http.Request) { r.Header.Add(name, value) }
}

func withSignature(sig string) reqOpt {
	return func(r *http.Request) { r.Header.Set("X-Hub-Signature-256", sig) }
}

func doWebhook(t *testing.T, s *webhookServer, method string, body []byte, opts ...reqOpt) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "/api/v1/github/webhooks", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "workflow_job")
	req.Header.Set("X-GitHub-Delivery", "guid-1")
	req.Header.Set("X-Hub-Signature-256", signBody(body))
	for _, opt := range opts {
		opt(req)
	}
	rec := httptest.NewRecorder()
	s.handleWebhook(rec, req)
	return rec
}

func decodeAck(t *testing.T, rec *httptest.ResponseRecorder) (status, deliveryID string) {
	t.Helper()
	var out struct {
		Accepted struct {
			Status     string `json:"status"`
			DeliveryID string `json:"delivery_id"`
		} `json:"accepted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode ack: %v (body %q)", err, rec.Body.String())
	}
	return out.Accepted.Status, out.Accepted.DeliveryID
}

func TestWebhookValidSignatureAccepted(t *testing.T) {
	f := &fakeInbox{}
	rec := doWebhook(t, newTestWebhookServer(f), http.MethodPost, testPayload)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %q)", rec.Code, rec.Body.String())
	}
	status, deliveryID := decodeAck(t, rec)
	if status != "accepted" || deliveryID != "guid-1" {
		t.Fatalf("ack = (%q, %q), want (accepted, guid-1)", status, deliveryID)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if len(f.recorded) != 1 {
		t.Fatalf("recorded %d deliveries, want 1", len(f.recorded))
	}
	env := f.recorded[0]
	if env.ProviderInstallationID != 42 || env.ProviderRepositoryID != 7 || env.ProviderJobID != 100 {
		t.Fatalf("envelope ids = (%d, %d, %d), want (42, 7, 100)",
			env.ProviderInstallationID, env.ProviderRepositoryID, env.ProviderJobID)
	}
	if env.ProviderRunAttempt != 1 {
		t.Fatalf("run_attempt = %d, want 1 (0 coerced)", env.ProviderRunAttempt)
	}
	if want := sha256Hex(testPayload); env.PayloadSHA256 != want {
		t.Fatalf("payload sha = %q, want %q", env.PayloadSHA256, want)
	}
	if len(f.rejected) != 0 {
		t.Fatalf("rejected %d deliveries, want 0", len(f.rejected))
	}
}

func TestWebhookDuplicateSameHash(t *testing.T) {
	// The store returning a non-accepted/retryable state models a redelivery
	// of an already-settled GUID with a matching hash.
	f := &fakeInbox{ackState: stateProcessed}
	rec := doWebhook(t, newTestWebhookServer(f), http.MethodPost, testPayload)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	if status, _ := decodeAck(t, rec); status != "duplicate" {
		t.Fatalf("ack status = %q, want duplicate", status)
	}
}

func TestWebhookBadSignature(t *testing.T) {
	f := &fakeInbox{}
	rec := doWebhook(t, newTestWebhookServer(f), http.MethodPost, testPayload,
		withSignature("sha256="+hex.EncodeToString(bytes.Repeat([]byte{0xab}, 32))))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if len(f.recorded) != 0 {
		t.Fatalf("recorded %d deliveries, want 0", len(f.recorded))
	}
	if len(f.rejected) != 1 {
		t.Fatalf("rejected %d deliveries, want 1", len(f.rejected))
	}
	if f.rejected[0].PayloadJSON != nil {
		t.Fatal("rejected delivery must not carry the raw body")
	}
	if got := f.rejectedProblems[0][0].Code; got != "provider_webhook.signature_invalid" {
		t.Fatalf("problem code = %q, want provider_webhook.signature_invalid", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json", ct)
	}
}

func TestWebhookMissingHeader(t *testing.T) {
	f := &fakeInbox{}
	rec := doWebhook(t, newTestWebhookServer(f), http.MethodPost, testPayload,
		withoutHeader("X-GitHub-Event"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(f.recorded) != 0 {
		t.Fatal("delivery must not be recorded on header failure")
	}
	if len(f.rejected) != 1 {
		t.Fatalf("rejected %d deliveries, want 1", len(f.rejected))
	}
	if got := f.rejectedProblems[0][0].Pointer; got != "header:X-GitHub-Event" {
		t.Fatalf("problem pointer = %q, want header:X-GitHub-Event", got)
	}
}

func TestWebhookDuplicatedHeader(t *testing.T) {
	f := &fakeInbox{}
	rec := doWebhook(t, newTestWebhookServer(f), http.MethodPost, testPayload,
		withExtraHeader("X-GitHub-Event", "workflow_job"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(f.recorded) != 0 {
		t.Fatal("delivery must not be recorded on duplicated header")
	}
}

func TestWebhookMethodNotAllowed(t *testing.T) {
	f := &fakeInbox{}
	rec := doWebhook(t, newTestWebhookServer(f), http.MethodGet, testPayload)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if got := rec.Header().Get("Allow"); got != http.MethodPost {
		t.Fatalf("Allow = %q, want POST", got)
	}
	if len(f.recorded) != 0 {
		t.Fatal("delivery must not be recorded on non-POST")
	}
}

func TestWebhookBodyTooLarge(t *testing.T) {
	f := &fakeInbox{}
	big := bytes.Repeat([]byte("a"), maxWebhookBytes+1)
	rec := doWebhook(t, newTestWebhookServer(f), http.MethodPost, big)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", rec.Code)
	}
	if len(f.recorded) != 0 {
		t.Fatal("oversized delivery must not be recorded")
	}
	if len(f.rejected) != 1 {
		t.Fatalf("rejected %d deliveries, want 1", len(f.rejected))
	}
	if got := f.rejectedProblems[0][0].Code; got != "provider_webhook.body_invalid" {
		t.Fatalf("problem code = %q, want provider_webhook.body_invalid", got)
	}
}

func TestWebhookReplayConflict(t *testing.T) {
	// pgx.ErrNoRows from the insert = same GUID, different payload hash: the
	// 409 security event. The original delivery row is never touched.
	f := &fakeInbox{recordErr: pgx.ErrNoRows}
	rec := doWebhook(t, newTestWebhookServer(f), http.MethodPost, testPayload)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if len(f.rejected) != 0 {
		t.Fatal("replay conflict must not write a rejected row")
	}
	var doc problemDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode problem doc: %v", err)
	}
	if doc.Type != "urn:guardian:verself-runner:problem:provider_webhook.delivery_replay_conflict" {
		t.Fatalf("problem type = %q", doc.Type)
	}
}

func TestWebhookInboxUnavailable(t *testing.T) {
	f := &fakeInbox{recordErr: errors.New("connection refused")}
	rec := doWebhook(t, newTestWebhookServer(f), http.MethodPost, testPayload)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestWebhookInvalidJSONBody(t *testing.T) {
	f := &fakeInbox{}
	body := []byte(`{"action":`)
	rec := doWebhook(t, newTestWebhookServer(f), http.MethodPost, body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if len(f.rejected) != 1 {
		t.Fatalf("rejected %d deliveries, want 1", len(f.rejected))
	}
	if got := f.rejectedProblems[0][0].Code; got != "provider_webhook.payload_invalid" {
		t.Fatalf("problem code = %q, want provider_webhook.payload_invalid", got)
	}
}
