package tests

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Cozystack sets the cluster DNS domain to cozy.local, not Kubernetes'
// conventional cluster.local, so any manifest that addresses a service as
// <svc>.<ns>.svc.cluster.local resolves to NXDOMAIN. The failure is silent:
// pods stay Running and probes stay green; only app-level gauges or logs show
// the dead lookups. Search-relative names (<svc>.<ns>.svc:<port>) work under
// any cluster domain and are the house form.
//
// cert-manager Certificate dnsNames are exempt: SANs are names a certificate
// offers, not names anything dials — the OpenBao listener cert deliberately
// carries both cozy.local and cluster.local SANs for chart compatibility.
// Everywhere else, cluster.local in a manifest value is a latent black hole.
func TestClusterDomainConformance(t *testing.T) {
	root := repoRootFromRunfiles(t)

	docs := 0
	walk := func(path string, yamlDocs []map[string]interface{}) {
		for _, doc := range yamlDocs {
			docs++
			if isCertManagerCertificate(doc) {
				if spec := mapValue(doc["spec"]); spec != nil {
					delete(spec, "dnsNames")
				}
			}
			metadata := mapValue(doc["metadata"])
			kind := stringValue(doc["kind"])
			name := stringValue(metadata["name"])
			forEachString(doc, "", func(fieldPath, value string) {
				if !strings.Contains(value, "cluster.local") {
					return
				}
				t.Fatalf("%s: %s %s field %s contains %q; this cluster's DNS domain is cozy.local, so cluster.local names are NXDOMAIN — use the search-relative <svc>.<ns>.svc form", path, kind, name, fieldPath, value)
			})
		}
	}
	walkYAMLFiles(t, filepath.Join(root, "src/infrastructure/base"), walk)
	walkYAMLFiles(t, filepath.Join(root, "src/infrastructure/deployments"), walk)
	if docs == 0 {
		t.Fatalf("cluster-domain conformance walked 0 YAML documents; the walk roots or data deps are wrong")
	}
}

func isCertManagerCertificate(doc map[string]interface{}) bool {
	group, _, _ := strings.Cut(stringValue(doc["apiVersion"]), "/")
	return group == "cert-manager.io" && stringValue(doc["kind"]) == "Certificate"
}

// forEachString visits every string scalar in a parsed YAML tree, reporting a
// dotted field path for error messages.
func forEachString(value interface{}, fieldPath string, fn func(fieldPath, value string)) {
	switch v := value.(type) {
	case string:
		fn(fieldPath, v)
	case map[string]interface{}:
		for key, child := range v {
			forEachString(child, fieldPath+"."+key, fn)
		}
	case []interface{}:
		for i, child := range v {
			forEachString(child, fmt.Sprintf("%s[%d]", fieldPath, i), fn)
		}
	}
}
