package guestd

import (
	"fmt"
	"os"
	"path/filepath"
)

// PurgeRunnerEphemeral keeps runner task state and standard authentication
// files out of the next tool generation. Workflow publishers additionally
// keep their live credential files in /tmp, outside the durable home.
func PurgeRunnerEphemeral(home string) error {
	for _, relative := range []string{
		"_work/_temp", ".docker/config.json", ".npmrc", ".git-credentials", ".netrc", ".config/gh/hosts.yml",
	} {
		if err := os.RemoveAll(filepath.Join(home, relative)); err != nil {
			return fmt.Errorf("remove ephemeral %s: %w", relative, err)
		}
	}
	return nil
}
