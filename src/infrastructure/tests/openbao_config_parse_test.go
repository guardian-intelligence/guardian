package tests

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl"
)

// bao parses its rendered config only at process start and the StatefulSet
// updates OnDelete, so invalid HCL in this block ships green and first bites
// at the next pod restart. Parse it with the HCL dialect OpenBao uses.
func TestOpenBaoServerConfigParsesAsHCL(t *testing.T) {
	path := runfilePath("src/infrastructure/deployments/guardian/system/openbao-helmrelease.yaml")
	doc := singleYAMLDoc(t, path)
	server := nestedMap(t, doc, "spec", "values", "openbao", "server")
	raw := nestedValue(t, server, "ha", "raft", "config")
	cfg, ok := raw.(string)
	if !ok {
		t.Fatalf("openbao.server.ha.raft.config: want string, got %T", raw)
	}
	if _, err := hcl.Parse(cfg); err != nil {
		t.Fatalf("openbao.server.ha.raft.config is not parseable HCL: %v", err)
	}
	if !strings.Contains(cfg, `initialize "guardian_self_init"`) {
		t.Fatalf("openbao.server.ha.raft.config lost the guardian_self_init block")
	}
}
