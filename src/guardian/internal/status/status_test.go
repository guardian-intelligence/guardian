package status

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/guardian-intelligence/guardian/src/guardian/internal/up"
)

func TestModelRendersMinimalTreeAndFailure(t *testing.T) {
	m := newModel("guardian-dev")
	start := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	for _, event := range []up.StatusEvent{
		{
			ID:          "open-state",
			ParentID:    "prepare",
			ParentTitle: "Prepare bootstrap",
			State:       up.StatusDone,
			Title:       "Open state",
			StartedAt:   start,
			EndedAt:     start.Add(time.Second),
		},
		{
			ID:          "latitude-reinstall-ipxe",
			ParentID:    "reimage",
			ParentTitle: "Reimage host",
			State:       up.StatusFailed,
			Title:       "Ask Latitude to boot Talos",
			Failure: &up.StatusFailure{
				Summary:   "Latitude API token is missing",
				Detail:    "LATITUDE_API_KEY must contain a Latitude API token before provider reinstall",
				NextSteps: []string{"Export LATITUDE_API_KEY.", "Run guardian up again."},
			},
			StartedAt: start,
			EndedAt:   start.Add(2 * time.Second),
		},
	} {
		updated, _ := m.Update(statusMsg{event: event})
		m = updated.(model)
	}

	out := m.View()
	for _, want := range []string{
		"guardian up",
		"guardian-dev",
		"✓ Prepare bootstrap",
		"✕ Reimage host",
		"✕ Ask Latitude to boot Talos",
		"Latitude API token is missing",
		"Next:",
		"Export LATITUDE_API_KEY.",
		"Press e for details.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	for _, reject := range []string{
		"ACTIVE",
		"done",
		"skipped",
		"failed",
		"seen",
		"┌",
		"└",
	} {
		if strings.Contains(out, reject) {
			t.Fatalf("output contains dashboard artifact %q:\n%s", reject, out)
		}
	}
}

func TestModelTogglesFailureDetails(t *testing.T) {
	m := newModel("guardian-dev")
	updated, _ := m.Update(statusMsg{event: up.StatusEvent{
		ID:          "check-bootstrap-safety",
		ParentID:    "prepare",
		ParentTitle: "Prepare bootstrap",
		State:       up.StatusBlocked,
		Title:       "Check bootstrap safety",
		Failure: &up.StatusFailure{
			Summary: "Bootstrap safety gate is closed",
			Detail:  "bootstrap.destructive and bootstrap.requireMaintenance must both be true before reimage",
		},
	}})
	m = updated.(model)

	if strings.Contains(m.View(), "bootstrap.destructive and bootstrap.requireMaintenance") {
		t.Fatalf("details visible before toggle:\n%s", m.View())
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(model)
	if !strings.Contains(m.View(), "bootstrap.destructive and bootstrap.requireMaintenance") {
		t.Fatalf("details missing after toggle:\n%s", m.View())
	}
}

func TestPlainRendererPrintsFailureNextSteps(t *testing.T) {
	var buf bytes.Buffer
	renderer := New(&buf, Options{Mode: ModePlain, ClusterName: "guardian-dev"})
	renderer.Report(up.StatusEvent{
		ID:    "check-bootstrap-safety",
		State: up.StatusBlocked,
		Title: "Check bootstrap safety",
		Failure: &up.StatusFailure{
			Summary:   "Bootstrap safety gate is closed",
			NextSteps: []string{"Confirm the target host is safe to wipe."},
		},
	})

	out := buf.String()
	for _, want := range []string{
		"guardian up guardian-dev",
		"! Check bootstrap safety",
		"Bootstrap safety gate is closed",
		"Next:",
		"Confirm the target host is safe to wipe.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plain output missing %q:\n%s", want, out)
		}
	}
}
