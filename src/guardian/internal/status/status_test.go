package status

import (
	"bytes"
	"strings"
	"testing"
	"time"

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
				Code: "latitude.reinstall",
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
		"latitude.reinstall",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	for _, reject := range []string{
		"Next:",
		"Why:",
		"Press e",
		"Details:",
		"Detail:",
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

func TestModelDoesNotRenderFailureDetails(t *testing.T) {
	m := newModel("guardian-dev")
	updated, _ := m.Update(statusMsg{event: up.StatusEvent{
		ID:          "check-bootstrap-safety",
		ParentID:    "prepare",
		ParentTitle: "Prepare bootstrap",
		State:       up.StatusBlocked,
		Title:       "Check bootstrap safety",
		Failure: &up.StatusFailure{
			Code: "bootstrap.safety",
		},
	}})
	m = updated.(model)

	out := m.View()
	if !strings.Contains(out, "bootstrap.safety") {
		t.Fatalf("code missing:\n%s", out)
	}
	for _, reject := range []string{"Details:", "Detail:", "bootstrap.destructive"} {
		if strings.Contains(out, reject) {
			t.Fatalf("output contains failure detail %q:\n%s", reject, out)
		}
	}
}

func TestPlainRendererPrintsFailureCode(t *testing.T) {
	var buf bytes.Buffer
	renderer := New(&buf, Options{Mode: ModePlain, ClusterName: "guardian-dev"})
	renderer.Report(up.StatusEvent{
		ID:    "check-bootstrap-safety",
		State: up.StatusBlocked,
		Title: "Check bootstrap safety",
		Failure: &up.StatusFailure{
			Code: "bootstrap.safety",
		},
	})

	out := buf.String()
	for _, want := range []string{
		"guardian up guardian-dev",
		"! Check bootstrap safety",
		"bootstrap.safety",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plain output missing %q:\n%s", want, out)
		}
	}
	for _, reject := range []string{"Next:", "Detail:", "reason"} {
		if strings.Contains(out, reject) {
			t.Fatalf("plain output contains generated failure text %q:\n%s", reject, out)
		}
	}
}

func TestPlainRendererPrintsUnchangedDescription(t *testing.T) {
	var buf bytes.Buffer
	renderer := New(&buf, Options{Mode: ModePlain, ClusterName: "guardian-dev"})
	renderer.Report(up.StatusEvent{
		ID:          "kubernetes",
		State:       up.StatusUnchanged,
		Title:       "Bootstrap Kubernetes",
		Description: "Already bootstrapped",
	})

	out := buf.String()
	if !strings.Contains(out, "◆ Bootstrap Kubernetes - Already bootstrapped") {
		t.Fatalf("plain output missing unchanged status:\n%s", out)
	}
}

func TestModelOnlyMarksParentUnchangedWhenAllChildrenUnchanged(t *testing.T) {
	m := newModel("guardian-dev")
	for _, event := range []up.StatusEvent{
		{
			ID:          "talm-init",
			ParentID:    "ubuntu",
			ParentTitle: "Prepare Ubuntu host",
			State:       up.StatusUnchanged,
			Title:       "Initialize Talm",
			Description: "Already initialized",
		},
		{
			ID:          "talm-template",
			ParentID:    "ubuntu",
			ParentTitle: "Prepare Ubuntu host",
			State:       up.StatusDone,
			Title:       "Render Talos config",
		},
	} {
		updated, _ := m.Update(statusMsg{event: event})
		m = updated.(model)
	}

	out := m.View()
	if !strings.Contains(out, "✓ Prepare Ubuntu host") {
		t.Fatalf("parent should be done for mixed changed/unchanged children:\n%s", out)
	}
	if strings.Contains(out, "◆ Prepare Ubuntu host") {
		t.Fatalf("parent incorrectly marked unchanged for mixed children:\n%s", out)
	}
}
