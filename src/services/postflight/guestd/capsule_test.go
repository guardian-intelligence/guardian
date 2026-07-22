package guestd

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestCapsuleCgroupValidationAndObservation(t *testing.T) {
	for _, path := range []string{"", "/sys/fs/cgroup", "/sys/fs/cgroup/../escape", "relative"} {
		if validCapsuleCgroup(path) {
			t.Fatalf("unsafe cgroup %q accepted", path)
		}
	}
	if !validCapsuleCgroup("/sys/fs/cgroup/postflight/capsule") {
		t.Fatal("capsule cgroup rejected")
	}

	root := t.TempDir()
	events := filepath.Join(root, "cgroup.events")
	for body, want := range map[string]bool{"populated 0\nfrozen 0\n": true, "populated 1\nfrozen 0\n": false} {
		if err := os.WriteFile(events, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := capsuleCgroupEmpty(root)
		if err != nil || got != want {
			t.Fatalf("events %q = %v, %v; want %v", body, got, err, want)
		}
	}
}

func TestRunnerProcessRecognitionPreservesWorkloadDaemons(t *testing.T) {
	root := "/opt/actions-runner"
	for name, process := range map[string]struct {
		executable string
		cmdline    []byte
	}{
		"worker": {
			executable: "/opt/actions-runner/bin/Runner.Worker.real",
		},
		"worker trampoline": {
			executable: "/usr/bin/nsenter",
			cmdline:    []byte("/usr/bin/nsenter\x00--\x00/opt/actions-runner/bin/Runner.Worker.real\x00"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			if !isRunnerProcess(process.executable, process.cmdline, root) {
				t.Fatal("runner process was not recognized")
			}
		})
	}

	for name, process := range map[string]struct {
		executable string
		cmdline    []byte
	}{
		"Gradle daemon using installed toolcache": {
			executable: "/opt/actions-runner/_work/_tool/Java_Temurin-Hotspot_jdk/25/bin/java",
			cmdline:    []byte("java\x00-javaagent:/opt/actions-runner/_work/gradle/gradle/gradle-agent.jar\x00org.gradle.launcher.daemon.bootstrap.GradleDaemon\x00"),
		},
		"workspace script": {
			executable: "/bin/bash",
			cmdline:    []byte("/bin/bash\x00/opt/actions-runner/_work/repo/repo/server.sh\x00"),
		},
		"action runtime": {
			executable: "/opt/actions-runner/externals/node24/bin/node",
		},
		"prefix collision": {
			executable: "/opt/actions-runner-hostile/bin/Runner.Worker.real",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if isRunnerProcess(process.executable, process.cmdline, root) {
				t.Fatal("workload process was classified as a runner")
			}
		})
	}
}
