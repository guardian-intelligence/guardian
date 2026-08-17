package tests

import "testing"

func TestChunkiesDedicatedParkTopology(t *testing.T) {
	path := runfilePath("src/infrastructure/deployments/mythra/prod/chunkies.yaml")
	docs := yamlDocs(t, path)

	for _, name := range []string{"chunkies-gateway", "chunkies-park"} {
		deployment := findDoc(t, docs, "Deployment", name)
		spec := mapValue(deployment["spec"])
		if replicas, ok := spec["replicas"].(int); !ok || replicas != 1 {
			t.Fatalf("Deployment %s replicas = %v, want 1", name, spec["replicas"])
		}
		podSpec := mapValue(mapValue(spec["template"])["spec"])
		if enabled, ok := podSpec["hostNetwork"].(bool); !ok || !enabled {
			t.Fatalf("Deployment %s hostNetwork = %v, want true", name, podSpec["hostNetwork"])
		}
		if stringValue(mapValue(podSpec["nodeSelector"])["kubernetes.io/hostname"]) != "ash-worker0" {
			t.Fatalf("Deployment %s is not pinned to ash-worker0", name)
		}
		tolerations := sliceValue(podSpec["tolerations"])
		if len(tolerations) != 1 {
			t.Fatalf("Deployment %s tolerations = %v, want one dedicated WUM toleration", name, tolerations)
		}
		toleration := mapValue(tolerations[0])
		if stringValue(toleration["key"]) != "guardian.dev/dedicated" || stringValue(toleration["value"]) != "wum" || stringValue(toleration["effect"]) != "NoSchedule" {
			t.Fatalf("Deployment %s toleration = %v, want dedicated WUM node", name, toleration)
		}
		containers := sliceValue(podSpec["containers"])
		if len(containers) != 1 {
			t.Fatalf("Deployment %s containers = %d, want 1", name, len(containers))
		}
		resources := mapValue(mapValue(containers[0])["resources"])
		requests := mapValue(resources["requests"])
		limits := mapValue(resources["limits"])
		if stringValue(requests["cpu"]) == "" || stringValue(requests["cpu"]) != stringValue(limits["cpu"]) {
			t.Fatalf("Deployment %s CPU request/limit = %v/%v, want equal exclusive-CPU sizing", name, requests["cpu"], limits["cpu"])
		}
		if stringValue(requests["memory"]) != stringValue(limits["memory"]) {
			t.Fatalf("Deployment %s memory request/limit = %v/%v, want Guaranteed QoS", name, requests["memory"], limits["memory"])
		}
	}

	manifest := readText(t, path)
	assertTextContains(t, manifest, "PARK_BACKENDS", path)
	assertTextContains(t, manifest, "value: park-chunkies-canary=127.0.0.1:9632", path)
	assertTextContains(t, manifest, "name: PARK_NAME\n              value: park-chunkies-canary", path)
	assertTextContains(t, manifest, "name: PUBLIC_ADDR\n              value: 206.223.228.99:4433", path)
	assertTextContains(t, manifest, "name: INTERNAL_HOST\n              value: 127.0.0.1", path)
	assertTextContains(t, manifest, `{"$imagepolicy": "guardian-imageops:chunkies-gateway"}`, path)
	assertTextContains(t, manifest, `{"$imagepolicy": "guardian-imageops:chunkies-park"}`, path)
}
