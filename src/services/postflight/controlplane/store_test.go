package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/syncproto"
)

// The comment loop's dirty-clear must be conditional on the updated_at the
// sync started from, or a MarkPRCommentDirty landing mid-sync is silently
// wiped and the final job state never renders. There is no hermetic Postgres
// in CI, so pin the guard's shape in the SQL itself; the semantics are
// exercised in the live stage-(a) drill.
func TestPRCommentClearsAreConditional(t *testing.T) {
	const guard = "dirty = (updated_at <> $"
	if !strings.Contains(sqlMarkPRCommentPosted, guard) {
		t.Errorf("sqlMarkPRCommentPosted lost its conditional dirty-clear guard:\n%s", sqlMarkPRCommentPosted)
	}
	if !strings.Contains(sqlMarkPRCommentClean, guard) {
		t.Errorf("sqlMarkPRCommentClean lost its conditional dirty-clear guard:\n%s", sqlMarkPRCommentClean)
	}
	if !strings.Contains(sqlListDirtyPRComments, "updated_at") {
		t.Errorf("sqlListDirtyPRComments must select updated_at for the clear guard:\n%s", sqlListDirtyPRComments)
	}
}

func TestAssignmentReportRejectsProcessCheckpointPublication(t *testing.T) {
	err := (&pgStore{}).ApplyAssignmentReport(
		context.Background(),
		"host-a",
		time.Time{},
		syncproto.AssignmentReport{
			AssignmentID: "assignment-a",
			MemberID:     "member-a",
			RequestID:    "request-a",
			JobID:        "job-a",
			Checkpoint: &syncproto.CheckpointArtifact{
				Digest:  "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Version: "Version: 4.2",
			},
		},
		time.Time{},
	)
	if err == nil || !strings.Contains(err.Error(), "process checkpoint publication is disabled") {
		t.Fatalf("ApplyAssignmentReport error = %v", err)
	}
}
