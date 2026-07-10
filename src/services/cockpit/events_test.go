package main

import (
	"encoding/json"
	"testing"
	"time"
)

const commitsFixture = `[
  {
    "sha": "78d08e0a24c9ef5152bba5bfcc9da6d70f45e5dd",
    "commit": {
      "message": "Cockpit warm tier: per-stage rollup writers (PR 3b) (#579)\n\nbody text",
      "committer": {"date": "2026-07-10T01:17:15Z"},
      "author": {"name": "Shovon Hasan"}
    },
    "author": {"login": "Anveio"},
    "html_url": "https://github.com/guardian-intelligence/guardian/commit/78d08e0a"
  },
  {
    "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "commit": {
      "message": "chore: promote company-site to prod",
      "committer": {"date": "2026-07-10T00:00:00Z"},
      "author": {"name": "kargo-bot"}
    },
    "author": {"login": "guardian-kargo[bot]"},
    "html_url": "https://github.com/guardian-intelligence/guardian/commit/aaaaaaaa"
  },
  {
    "sha": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    "commit": {
      "message": "direct push without a PR",
      "committer": {"date": "2026-07-09T23:00:00Z"},
      "author": {"name": "Someone Offline"}
    },
    "author": null,
    "html_url": "https://github.com/guardian-intelligence/guardian/commit/bbbbbbbb"
  }
]`

func TestCommitRowsMapsKindsActorsAndSubjects(t *testing.T) {
	var commits []ghCommit
	if err := json.Unmarshal([]byte(commitsFixture), &commits); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	rows := commitRows(commits)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}

	pr := rows[0]
	if pr.kind != "pr_merged" || pr.actor != "Anveio" {
		t.Fatalf("pr row = kind %q actor %q, want pr_merged/Anveio", pr.kind, pr.actor)
	}
	if pr.title != "Cockpit warm tier: per-stage rollup writers (PR 3b) (#579)" {
		t.Fatalf("pr title kept the body or lost the subject: %q", pr.title)
	}
	if !pr.ts.Equal(time.Date(2026, 7, 10, 1, 17, 15, 0, time.UTC)) {
		t.Fatalf("pr ts = %v", pr.ts)
	}

	if rows[1].kind != "promotion" || rows[1].actor != "guardian-kargo[bot]" {
		t.Fatalf("bot row = kind %q actor %q, want promotion", rows[1].kind, rows[1].actor)
	}

	// No GitHub account: falls back to the git author name, plain push.
	if rows[2].kind != "push" || rows[2].actor != "Someone Offline" {
		t.Fatalf("offline row = kind %q actor %q", rows[2].kind, rows[2].actor)
	}
}

func TestCommitRowsSkipsEmptySha(t *testing.T) {
	if rows := commitRows([]ghCommit{{}}); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none for an empty commit", rows)
	}
}
