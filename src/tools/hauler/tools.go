//go:build tools

// Package tools pins Hauler's CLI dependency graph into go.mod. The hauler
// binary Bazel builds is the external module's own main
// (@dev_hauler_go_hauler_v2//cmd/hauler); nothing in this repo links against
// it, but `go mod tidy` prunes modules no package imports, and without this
// file the external repo's BUILD files reference dependency repos (helm,
// containerd, cosign, ...) that go_deps never generates. The cli package is
// the outermost importable package of the binary; its transitive imports are
// exactly the binary's.
package tools

import (
	_ "hauler.dev/go/hauler/v2/cmd/hauler/cli"
)
