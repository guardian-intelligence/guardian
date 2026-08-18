package tests

import (
	"regexp"
	"strings"
	"testing"

	"github.com/guardian-intelligence/guardian/src/infrastructure/imageset"
)

// imageset.ManifestTrees is the one list of trees whose rendered artifact
// references enter the signed union, and two path filters must track it.
// The images-lock-sign workflow re-signs the union when a tree changes —
// imageops pushes pin bumps straight to main, so a tree missing from its
// filter leaves the published signature stale with nothing red. The imageops
// ImageUpdateAutomation only rewrites pin markers under its update.path, so
// a tree outside it stops receiving pin bumps entirely. Both failures are
// silent; this test is what moves the three lists together.
func TestManifestTreesCoveredBySignWorkflowAndImageAutomation(t *testing.T) {
	workflowPath := runfilePath(".github/workflows/images-lock-sign.yml")
	workflow := readText(t, workflowPath)

	autoPath := runfilePath("src/infrastructure/deployments/guardian/imageops/imageupdateautomation.yaml")
	auto := readText(t, autoPath)
	m := regexp.MustCompile(`path:\s*\./(\S+)`).FindStringSubmatch(auto)
	if m == nil {
		t.Fatalf("%s does not declare a ./-relative update path", autoPath)
	}
	updatePath := m[1]

	for _, tree := range imageset.ManifestTrees {
		if want := `"` + tree + `/**"`; !strings.Contains(workflow, want) {
			t.Errorf("%s push paths do not include %s; pin bumps in that tree would not re-sign the union", workflowPath, want)
		}
		if tree != updatePath && !strings.HasPrefix(tree, updatePath+"/") {
			t.Errorf("manifest tree %s is outside the image automation update path %s; its pin markers would never move", tree, updatePath)
		}
	}
}
