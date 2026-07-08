package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// Every non-success outcome is an RFC-7807 problem, both in the HTTP response
// and in the delivery/demand problem ledgers (append-only history; the first
// problem is denormalized onto the parent row as primary).

const (
	phaseRequest    = "request"
	phaseProcessing = "processing"
)

type problem struct {
	Code      string
	Title     string
	Detail    string
	Status    int
	Retryable bool
	Pointer   string
}

func (p problem) typeURI() string {
	return "urn:guardian:postflight-runner:problem:" + p.Code
}

func problemMethodNotAllowed() problem {
	return problem{
		Code:   "provider_webhook.method_not_allowed",
		Title:  "method not allowed",
		Detail: "webhook deliveries must be POSTed",
		Status: http.StatusMethodNotAllowed,
	}
}

func problemHeaderInvalid(name string) problem {
	return problem{
		Code:    "provider_webhook.header_invalid",
		Title:   "invalid webhook header",
		Detail:  fmt.Sprintf("header %s must be present exactly once with a non-empty value", name),
		Status:  http.StatusBadRequest,
		Pointer: "header:" + name,
	}
}

func problemBodyInvalid(detail string, status int) problem {
	return problem{
		Code:    "provider_webhook.body_invalid",
		Title:   "invalid webhook body",
		Detail:  detail,
		Status:  status,
		Pointer: "body",
	}
}

func problemSignatureInvalid() problem {
	return problem{
		Code:    "provider_webhook.signature_invalid",
		Title:   "webhook signature verification failed",
		Detail:  "X-Hub-Signature-256 did not match the payload",
		Status:  http.StatusUnauthorized,
		Pointer: "header:X-Hub-Signature-256",
	}
}

func problemPayloadInvalid(detail string) problem {
	return problem{
		Code:    "provider_webhook.payload_invalid",
		Title:   "invalid webhook payload",
		Detail:  detail,
		Status:  http.StatusBadRequest,
		Pointer: "body",
	}
}

func problemReplayConflict() problem {
	return problem{
		Code:   "provider_webhook.delivery_replay_conflict",
		Title:  "delivery replay conflict",
		Detail: "delivery id was already recorded with a different payload hash",
		Status: http.StatusConflict,
	}
}

func problemInboxUnavailable() problem {
	return problem{
		Code:      "provider_webhook.inbox_unavailable",
		Title:     "delivery inbox unavailable",
		Detail:    "the delivery could not be persisted; GitHub will redeliver",
		Status:    http.StatusServiceUnavailable,
		Retryable: true,
	}
}

func problemUnsupportedEvent(event string) problem {
	return problem{
		Code:   "provider_webhook.unsupported_event",
		Title:  "unsupported webhook event",
		Detail: fmt.Sprintf("event %q is recorded but not processed", event),
	}
}

func problemInstallationMismatch(got int64) problem {
	return problem{
		Code:   "provider_webhook.installation_mismatch",
		Title:  "installation not configured",
		Detail: fmt.Sprintf("delivery is for installation %d, not the configured one", got),
	}
}

func problemProcessingStale() problem {
	return problem{
		Code:      "provider_webhook.processing_stale",
		Title:     "delivery processing went stale",
		Detail:    "the delivery was stuck in processing and has been reclaimed",
		Retryable: true,
	}
}

func problemAttemptsExhausted(attempts int32) problem {
	return problem{
		Code:   "provider_webhook.processing_attempts_exhausted",
		Title:  "delivery attempts exhausted",
		Detail: fmt.Sprintf("delivery failed after %d attempts", attempts),
	}
}

func problemProcessingFailed(err error) problem {
	return problem{
		Code:      "provider_webhook.processing_failed",
		Title:     "delivery processing failed",
		Detail:    err.Error(),
		Retryable: true,
	}
}

type problemDoc struct {
	Type    string       `json:"type"`
	Title   string       `json:"title"`
	Status  int          `json:"status,omitempty"`
	Detail  string       `json:"detail,omitempty"`
	Pointer string       `json:"pointer,omitempty"`
	Errors  []problemDoc `json:"errors,omitempty"`
}

func docFrom(p problem) problemDoc {
	return problemDoc{
		Type:    p.typeURI(),
		Title:   p.Title,
		Status:  p.Status,
		Detail:  p.Detail,
		Pointer: p.Pointer,
	}
}

// writeProblems renders an RFC-7807 document; the first problem is primary
// and drives the response status, all problems ride in errors[].
func writeProblems(w http.ResponseWriter, problems []problem) {
	primary := problems[0]
	doc := docFrom(primary)
	for _, p := range problems {
		doc.Errors = append(doc.Errors, docFrom(p))
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(primary.Status)
	_ = json.NewEncoder(w).Encode(doc)
}
