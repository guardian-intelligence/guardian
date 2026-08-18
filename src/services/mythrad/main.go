// The chunkies binaries: one source tree, two processes. The gateway owns
// the public transport and admission; the park owns one simulation
// authority. Which one a process is comes from its executable name, so the
// Bazel targets and images stay stable while the packages stay separate.
package main

import (
	"log"
	"os"
	"path/filepath"

	"github.com/guardian-intelligence/guardian/src/services/mythrad/gateway"
	"github.com/guardian-intelligence/guardian/src/services/mythrad/park"
)

func main() {
	switch filepath.Base(os.Args[0]) {
	case "chunkies-gateway":
		gateway.Run()
	case "chunkies-park":
		park.Run()
	default:
		log.Fatalf("unknown executable %q", filepath.Base(os.Args[0]))
	}
}
