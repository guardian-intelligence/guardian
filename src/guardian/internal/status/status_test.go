package status

import (
	"bytes"
	"strings"
	"testing"

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
