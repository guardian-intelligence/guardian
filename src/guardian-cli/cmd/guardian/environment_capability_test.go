package main

import "testing"

func TestEnvironmentCapabilities(t *testing.T) {
	for _, siteName := range []string{"dev", "gamma", "prod"} {
		t.Run(siteName, func(t *testing.T) {
			site := loadTestSite(t, siteName)
			caps, err := environmentCapabilities(site)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]string{
				"AisucksProduct/aisucks":           "aisucksproducts.products.guardian.dev",
				"CompanySite/company-site":         "companysites.products.guardian.dev",
				"DirectusInstance/directus":        "directusinstances.platform.guardian.dev",
				"ObservabilityStack/observability": "observabilitystacks.platform.guardian.dev",
				"StatusSurface/status":             "statussurfaces.platform.guardian.dev",
				"StoragePlane/local-zfs":           "storageplanes.platform.guardian.dev",
			}
			wantRollouts := map[string][]environmentRollout{
				"AisucksProduct/aisucks": {
					{namespace: "aisucks", resource: "deployment/aisucks"},
				},
				"CompanySite/company-site": {
					{namespace: "company", resource: "deployment/company-site"},
				},
				"DirectusInstance/directus": nil,
				"ObservabilityStack/observability": {
					{namespace: "observability", resource: "deployment/victoria-metrics"},
				},
				"StoragePlane/local-zfs": nil,
			}
			if siteName == "prod" {
				wantRollouts["StatusSurface/status"] = nil
			} else {
				wantRollouts["StatusSurface/status"] = []environmentRollout{
					{namespace: "status", resource: "deployment/status"},
				}
			}
			if siteName == "dev" {
				want["OCIRegistry/zot"] = "ociregistries.platform.guardian.dev"
				wantRollouts["OCIRegistry/zot"] = []environmentRollout{
					{namespace: "guardian-oci", resource: "deployment/zot"},
				}
			}
			got := map[string]string{}
			gotRollouts := map[string][]environmentRollout{}
			for _, cap := range caps {
				key := cap.kind + "/" + cap.name
				got[key] = cap.resource
				gotRollouts[key] = cap.rollouts
			}
			for key, resource := range want {
				if got[key] != resource {
					t.Fatalf("capability %s resource = %q, want %q; all = %#v", key, got[key], resource, got)
				}
				if !sameRollouts(gotRollouts[key], wantRollouts[key]) {
					t.Fatalf("capability %s rollouts = %#v, want %#v", key, gotRollouts[key], wantRollouts[key])
				}
			}
		})
	}
}

func TestEnvironmentCapabilityRolloutsRequireDirectusNamespace(t *testing.T) {
	_, err := environmentCapabilityRollouts("DirectusInstance", "", true)
	if err == nil {
		t.Fatal("environmentCapabilityRollouts DirectusInstance with empty namespace succeeded")
	}
}

func sameRollouts(a, b []environmentRollout) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
