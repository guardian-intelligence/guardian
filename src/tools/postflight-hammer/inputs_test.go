package main

import (
	"strings"
	"testing"
)

func TestWorkflowInputs(t *testing.T) {
	inputs := workflowInputs{}
	if err := inputs.Set("lane=postflight"); err != nil {
		t.Fatalf("set lane: %v", err)
	}
	if err := inputs.Set("description=value=with=equals"); err != nil {
		t.Fatalf("set value: %v", err)
	}
	if got := inputs["description"]; got != "value=with=equals" {
		t.Fatalf("description = %q", got)
	}
	if err := inputs.Set("lane=github"); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("duplicate key accepted: %v", err)
	}
	if err := inputs.Set("missing-separator"); err == nil {
		t.Fatal("malformed input accepted")
	}
	if got := inputs.String(); got != "description=value=with=equals,lane=postflight" {
		t.Fatalf("String() = %q", got)
	}
}
