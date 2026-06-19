package status

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/guardian-intelligence/guardian/src/guardian/internal/up"
)

func TestPlainRendererUsesDynoStyleStatusLines(t *testing.T) {
	var buf bytes.Buffer
	renderer := New(&buf, Options{Mode: ModePlain, ClusterName: "guardian-dev"})

	renderer.Report(up.StatusEvent{
		Name:        "helm-install-cozystack",
		State:       up.StatusRunning,
		Title:       "Install Cozystack operator",
		Description: "installing the pinned Cozystack operator and package source",
	})
	renderer.Report(up.StatusEvent{
		Name:  "helm-install-cozystack",
		State: up.StatusDone,
		Title: "Install Cozystack operator",
	})

	out := buf.String()
	for _, want := range []string{
		"guardian up guardian-dev",
		"-----> Install Cozystack operator",
		"       installing the pinned Cozystack operator and package source",
		"       done Install Cozystack operator",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestDashboardModelRendersSinglePaneStatus(t *testing.T) {
	model := newDashboardModel("guardian-dev")
	updated, _ := model.Update(statusMsg{event: up.StatusEvent{
		Name:        "preflight",
		State:       up.StatusDone,
		Title:       "Check bootstrap safety",
		Description: "verifying destructive gates and genesis recipients before mutating the host",
	}})
	model = updated.(dashboardModel)
	updated, _ = model.Update(statusMsg{event: up.StatusEvent{
		Name:        "latitude-reinstall-ipxe",
		Index:       2,
		Total:       21,
		State:       up.StatusFailed,
		Title:       "Reimage host",
		Description: "asking Latitude to boot the host from the pinned Talos iPXE URL",
		Detail:      "LATITUDE_API_KEY must contain a Latitude API token before provider reinstall",
	}})
	model = updated.(dashboardModel)

	out := model.View()
	for _, want := range []string{
		"Guardian up",
		"guardian-dev",
		"ACTIVE",
		"Reimage host",
		"LATITUDE_API_KEY must contain a Latitude API token",
		"STEPS",
		"Check bootstrap safety",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dashboard missing %q:\n%s", want, out)
		}
	}
}

func TestTUIRendererLifecycle(t *testing.T) {
	var buf bytes.Buffer
	renderer := New(&buf, Options{Mode: ModeTUI, ClusterName: "guardian-dev"})
	done := make(chan error, 1)
	go func() {
		renderer.Report(up.StatusEvent{
			Name:        "helm-install-cozystack",
			State:       up.StatusRunning,
			Title:       "Install Cozystack operator",
			Description: "installing the pinned Cozystack operator and package source",
		})
		done <- renderer.Close()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("renderer close failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("renderer did not close")
	}

	out := buf.String()
	for _, want := range []string{
		"Guardian up",
		"guardian-dev",
		"Install Cozystack operator",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("renderer output missing %q:\n%s", want, out)
		}
	}
}
