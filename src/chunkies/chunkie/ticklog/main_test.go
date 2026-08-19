package ticklog

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("TICKLOG_CRASH_CHILD") == "1" {
		crashChild()
		return
	}
	os.Exit(m.Run())
}
