package vm

import "testing"

func TestWarmTemplateRejectsEveryCredentialBearingDonor(t *testing.T) {
	warm := Status{Phase: PhaseWarm}
	if !templateDonorEligible(meta{}, warm) {
		t.Fatal("fresh warm guest rejected")
	}
	for name, record := range map[string]meta{
		"registered":        {MemberID: "member"},
		"assignment":        {Assignment: &Assignment{RequestID: "job"}},
		"assignment ID":     {AssignmentID: "assignment"},
		"workspace":         {WorkspaceMountpoint: "/work"},
		"tools":             {ToolMountpoint: "/home/runner"},
		"process":           {ProcessMountpoint: "/process"},
		"restored template": {Restored: true},
	} {
		t.Run(name, func(t *testing.T) {
			if templateDonorEligible(record, warm) {
				t.Fatal("unsafe donor accepted")
			}
		})
	}
	for _, phase := range []Phase{PhaseBooting, PhaseAssigned, PhaseListening, PhaseJobAssigned, PhaseReady, PhaseExited} {
		if templateDonorEligible(meta{}, Status{Phase: phase}) {
			t.Fatalf("accepted %s donor", phase)
		}
	}
}

func TestTurboVMGenerationIDsDivergeAndIncomingIsExplicit(t *testing.T) {
	left := LaunchSpec{Flavor: FlavorTurbo, ID: "left"}
	right := LaunchSpec{Flavor: FlavorTurbo, ID: "right", Incoming: true}
	if argvDigest(left.Argv()) == argvDigest(right.Argv()) {
		t.Fatal("restored VM identity reused")
	}
	args := right.Argv()
	if args[len(args)-2] != "-incoming" || args[len(args)-1] != "defer" {
		t.Fatal("incoming migration must stay paused")
	}
}
