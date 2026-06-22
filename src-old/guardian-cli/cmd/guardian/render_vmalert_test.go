package main

import (
	"strings"
	"testing"
)

func TestVMAlertRenderPinsAppErrorRule(t *testing.T) {
	kubectl, err := kubectlPath()
	if err != nil {
		t.Fatalf("locate kubectl: %v", err)
	}
	c := componentByName(t, "vmalert")
	rendered, err := buildComponentKustomization(kubectl, c, map[string]string{"vmalert": "registry.guardian.internal/vmalert@sha256:deadbeef"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	decodeKinds(t, rendered)

	out := string(rendered)
	for _, want := range []string{
		"AppErrorRate",
		"aisucks_http_requests_total",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("vmalert render missing %q", want)
		}
	}
}
