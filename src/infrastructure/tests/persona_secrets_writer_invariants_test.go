package tests

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The platform-agent secrets-writer token-mint lane (platform-admins.yaml) is
// only safe while two invariants hold. A reviewer confirmed both are true
// today; these tests pin them so the grant cannot outlive its safety
// preconditions.

// writerPolicyBlock captures each rendered guardian-writer-<ns> ACL policy in
// the OpenBao self-init block. The self-init config is an HCL document embedded
// in a YAML block scalar, so the policy body arrives as an escaped one-line
// string (\" for quotes, \n for newlines) — matching it as raw text is the
// same shape every other OpenBao conformance assertion uses.
var writerPolicyBlock = regexp.MustCompile(
	`path = "sys/policies/acl/(guardian-writer-[a-z0-9-]+)"\s+data = \{\s+policy = "((?:[^"\\]|\\.)*)"`,
)

// secretsWriterTokenGrantPresent reports whether platform-admins.yaml still
// carries the agent's secrets-writer token-mint grant — either the dedicated
// ClusterRole or the VAP disjunct that permits `serviceaccounts/token` CREATE
// for the name secrets-writer. Either one lets the platform-agent mint a
// secrets-writer token headlessly.
func secretsWriterTokenGrantPresent(t *testing.T, docs []map[string]interface{}) bool {
	t.Helper()

	for _, doc := range docs {
		metadata := mapValue(doc["metadata"])
		if stringValue(doc["kind"]) == "ClusterRole" &&
			stringValue(metadata["name"]) == "guardian-persona-secrets-writer-token" {
			return true
		}
		if stringValue(doc["kind"]) == "ValidatingAdmissionPolicy" &&
			stringValue(metadata["name"]) == "guardian-persona-write-basic" {
			for _, validation := range sliceValue(mapValue(doc["spec"])["validations"]) {
				expr := stringValue(mapValue(validation)["expression"])
				if strings.Contains(expr, `request.operation == "CREATE"`) &&
					strings.Contains(expr, `request.resource.resource == "serviceaccounts"`) &&
					strings.Contains(expr, `request.subResource == "token"`) &&
					strings.Contains(expr, `request.name == "secrets-writer"`) {
					return true
				}
			}
		}
	}
	return false
}

// TestSecretsWriterGrantImpliesWriteOnlyPolicies is the machine-checkable form
// of the merge-order safety gate. The agent-minted secrets-writer token is a
// standing write credential the agent can produce headlessly; it is only safe
// while the guardian-writer-<ns> OpenBao policies grant NO read on any
// kv/data or kv/metadata path (write-only), so a leaked or misused token can
// load and rotate values but can never exfiltrate an existing secret.
//
// The conjunction is the whole point: grant-present AND writer-read-present is
// the unsafe state, and this test fails exactly there. On a branch where the
// live writer policies still carry read (current main), this test is EXPECTED
// TO FAIL — that red is the gate holding the grant lane unmergeable until the
// write-only writer policies land. It goes green only once those policies drop
// read and this lane rebases onto them. Do not weaken it to pass early.
func TestSecretsWriterGrantImpliesWriteOnlyPolicies(t *testing.T) {
	grantPath := runfilePath("src/infrastructure/base/cozystack/platform-admins.yaml")
	if !secretsWriterTokenGrantPresent(t, yamlDocs(t, grantPath)) {
		// The invariant is conditional on the grant: with no token-mint lane
		// there is no standing write credential to constrain.
		t.Skip("secrets-writer token-mint grant absent; write-only writer-policy invariant not applicable")
	}

	helmPath := runfilePath("src/infrastructure/deployments/guardian/system/openbao-helmrelease.yaml")
	raw := readText(t, helmPath)

	matches := writerPolicyBlock.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		t.Fatalf("%s: found no guardian-writer-<ns> ACL policies; the writer-policy matcher or the self-init block changed shape", helmPath)
	}

	for _, match := range matches {
		policyName, policyBody := match[1], match[2]
		// The escaped HCL capability list renders "read" as \"read\" in the
		// raw file text.
		if strings.Contains(policyBody, `\"read\"`) {
			t.Fatalf(
				"%s: writer policy %s grants read (capabilities include \"read\") while the secrets-writer token-mint grant is present in platform-admins.yaml; "+
					"the agent-minted write token must never be able to read secrets — drop read from every guardian-writer-<ns> policy (see PR #773) before this grant lane can merge",
				helmPath, policyName)
		}
	}
}

// TestSecretsWriterServiceAccountHasNoRBAC pins the second precondition. The
// token the agent mints targets the per-namespace `secrets-writer`
// ServiceAccount, and that mint escapes the platform-agent VAP's
// matchCondition (the request is the agent creating a token, not the SA acting)
// — so the ONLY thing bounding what a secrets-writer token can do inside
// Kubernetes is that the SA carries zero RBAC. Its sole capability is the
// OpenBao write-only role it authenticates into. Any RoleBinding or
// ClusterRoleBinding that subjects a ServiceAccount named secrets-writer would
// silently widen that blast radius; this walks every manifest and fails naming
// the offending binding. It holds on the current branch.
func TestSecretsWriterServiceAccountHasNoRBAC(t *testing.T) {
	root := repoRootFromRunfiles(t)

	bindings := 0
	walkMapDocs(t, filepath.Join(root, "src/infrastructure"), func(path string, doc map[string]interface{}) {
		kind := stringValue(doc["kind"])
		if kind != "RoleBinding" && kind != "ClusterRoleBinding" {
			return
		}
		bindings++
		name := stringValue(mapValue(doc["metadata"])["name"])
		for _, subject := range sliceValue(doc["subjects"]) {
			s := mapValue(subject)
			if stringValue(s["kind"]) == "ServiceAccount" && stringValue(s["name"]) == "secrets-writer" {
				t.Fatalf(
					"%s: %s %q subjects ServiceAccount secrets-writer; that SA must carry zero Kubernetes RBAC — its only capability is the OpenBao write-only role, and the agent's token-mint grant escapes the platform-agent VAP, so any binding here silently widens the blast radius",
					path, kind, name)
			}
		}
	})

	if bindings == 0 {
		t.Fatalf("walked src/infrastructure and found no RoleBinding/ClusterRoleBinding; the walk root or manifest data deps are wrong")
	}
}

// walkMapDocs decodes every *.yaml under dir (recursively) and passes each
// mapping document to fn. Unlike the shared walkYAMLFiles helper it tolerates
// documents whose top level is a sequence or scalar (e.g. Prometheus file_sd
// target lists), skipping them instead of failing — the src/infrastructure
// tree that must be swept for RBAC bindings contains such files.
func walkMapDocs(t *testing.T, dir string, fn func(path string, doc map[string]interface{})) {
	t.Helper()

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// The talm chart is Go templates, not plain YAML (and carries no
		// Kubernetes RBAC); it cannot be decoded as manifests.
		if d.IsDir() && d.Name() == "talm" {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		payload, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(payload))
		for {
			var doc interface{}
			decErr := dec.Decode(&doc)
			if errors.Is(decErr, io.EOF) {
				break
			}
			if decErr != nil {
				t.Fatalf("decode %s: %v", path, decErr)
			}
			if mapping, ok := doc.(map[string]interface{}); ok && len(mapping) > 0 {
				fn(path, mapping)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
}
