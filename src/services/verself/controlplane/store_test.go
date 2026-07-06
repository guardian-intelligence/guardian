package main

import (
	"strings"
	"testing"
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
