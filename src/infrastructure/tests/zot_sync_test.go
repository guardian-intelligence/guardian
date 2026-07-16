package tests

import (
	"encoding/json"
	"testing"
)

// A normal countersignature verification uses the OCI referrers API. zot's
// on-demand sync defaults to enumerating legacy Cosign tags during that read,
// then compares each signature manifest with its subject digest and recopies
// it from upstream. That makes a local verification wait on ghcr and can hold
// the shared registry path past both the countersigner and mirror-canary
// deadlines. The countersigner fetches the exact legacy CI signature tag in a
// separate verification step, so implicit legacy-tag discovery must stay off.
func TestZotReferrerSyncSkipsLegacyCosignTags(t *testing.T) {
	const manifest = "src/infrastructure/deployments/guardian/system/zot-helmrelease.yaml"

	configJSON := ""
	for _, doc := range yamlDocs(t, runfilePath(manifest)) {
		metadata := mapValue(doc["metadata"])
		if stringValue(doc["kind"]) != "HelmRelease" || stringValue(metadata["name"]) != "zot" {
			continue
		}

		values := mapValue(mapValue(doc["spec"])["values"])
		configJSON = stringValue(mapValue(values["configFiles"])["config.json"])
	}
	if configJSON == "" {
		t.Fatalf("%s: zot HelmRelease has no values.configFiles.config.json", manifest)
	}

	var config struct {
		Extensions struct {
			Sync struct {
				Registries []struct {
					URLs                 []string `json:"urls"`
					OnDemand             bool     `json:"onDemand"`
					SyncLegacyCosignTags *bool    `json:"syncLegacyCosignTags"`
				} `json:"registries"`
			} `json:"sync"`
		} `json:"extensions"`
	}
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		t.Fatalf("%s: parse zot config.json: %v", manifest, err)
	}

	for _, registry := range config.Extensions.Sync.Registries {
		if len(registry.URLs) != 1 || registry.URLs[0] != "https://ghcr.io" || !registry.OnDemand {
			continue
		}
		if registry.SyncLegacyCosignTags == nil || *registry.SyncLegacyCosignTags {
			t.Fatalf("%s: the on-demand ghcr sync must set syncLegacyCosignTags=false so OCI referrers reads do not enumerate and recopy legacy CI signature tags", manifest)
		}

		return
	}

	t.Fatalf("%s: no on-demand ghcr sync registry found", manifest)
}
