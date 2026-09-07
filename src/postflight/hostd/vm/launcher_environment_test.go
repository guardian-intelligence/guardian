package vm

import (
	"os/exec"
	"strings"
	"testing"
)

func TestQEMUEnvironmentExcludesHostCredentials(t *testing.T) {
	t.Setenv("HOSTD_SYNC_SECRET", "test-control-plane-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test-cloud-secret")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/run/user/1000/bus")
	for _, userManager := range []bool{false, true} {
		command := exec.Command("/usr/bin/env")
		command.Env = qemuEnvironment(userManager)
		output, err := command.Output()
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"HOSTD_SYNC_SECRET", "test-control-plane-secret", "AWS_SECRET_ACCESS_KEY", "test-cloud-secret"} {
			if strings.Contains(string(output), secret) {
				t.Fatal("VMM inherited a host credential")
			}
		}
		if strings.Contains(string(output), "DBUS_SESSION_BUS_ADDRESS=") != userManager ||
			strings.Contains(string(output), "XDG_RUNTIME_DIR=") != userManager {
			t.Fatal("user bus identity was not restricted to user-manager mode")
		}
	}
}
