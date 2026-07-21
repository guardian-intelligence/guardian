package guestd

import "testing"

func TestProcessIsNamespaceInit(t *testing.T) {
	running := []byte("Name:\ttini\nState:\tS (sleeping)\nNSpid:\t1084\t1\n")
	if !processIsNamespaceInit(running, "tini") {
		t.Fatal("running namespace init was not recognized")
	}
	for name, status := range map[string][]byte{
		"wrong executable": []byte("Name:\tsleep\nState:\tS (sleeping)\nNSpid:\t1084\t1\n"),
		"not pid one":      []byte("Name:\ttini\nState:\tS (sleeping)\nNSpid:\t1084\t2\n"),
		"zombie":           []byte("Name:\ttini\nState:\tZ (zombie)\nNSpid:\t1084\t1\n"),
	} {
		t.Run(name, func(t *testing.T) {
			if processIsNamespaceInit(status, "tini") {
				t.Fatal("invalid namespace init was accepted")
			}
		})
	}
}

func TestRunnerCommandRecognitionUsesPathBoundaries(t *testing.T) {
	if !commandUsesPath([]byte("/bin/bash\x00/opt/actions-runner/run.sh\x00"), "/opt/actions-runner") {
		t.Fatal("runner script was not recognized")
	}
	if commandUsesPath([]byte("/opt/actions-runner-hostile/tool\x00"), "/opt/actions-runner") {
		t.Fatal("prefix collision was recognized as a runner")
	}
}
