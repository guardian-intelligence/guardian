package controllers

import (
	"errors"
	"testing"
)

func TestOpenBaoAuthFailureStatus(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantReason  string
		wantMessage string
	}{
		{
			name:        "missing Kubernetes auth role",
			err:         errors.New(`login to OpenBao with Kubernetes auth: invalid role name "guardian-openbao-ops-controller"`),
			wantReason:  reasonSelfInitIncomplete,
			wantMessage: messageSelfInitIncomplete,
		},
		{
			name:        "other authentication failure",
			err:         errors.New("login to OpenBao with Kubernetes auth: permission denied"),
			wantReason:  reasonAuthenticationFailed,
			wantMessage: messageAuthenticationFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openBaoAuthFailureStatus(tt.err)
			if got.reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", got.reason, tt.wantReason)
			}
			if got.message != tt.wantMessage {
				t.Fatalf("message = %q, want %q", got.message, tt.wantMessage)
			}
		})
	}
}
