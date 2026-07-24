package tests

import "testing"

// Rotation-tolerance is a property of the consumer: a Secret captured into an
// env var is frozen for the pod's lifetime, so a long-running workload doing
// that must either restart on rotation (Reloader) or carry a documented
// exemption. Jobs and CronJobs get a fresh pod per run and are out of scope.
func TestSecretEnvConsumersSurviveRotation(t *testing.T) {
	exempt := map[string]string{
		// Env copies feed only the startup import that creates a fresh
		// realm; steady state resolves the same secrets through the live
		// file-vault mount, and a Reloader roll would trigger a Flagger
		// canary per rotation.
		"tenant-guardian-prod/keycloak": "file-vault live-read at steady state",
	}
	walkYAMLFiles(t, runfilePath("src/infrastructure/deployments"), func(path string, docs []map[string]interface{}) {
		for _, doc := range docs {
			kind, _ := doc["kind"].(string)
			if kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
				continue
			}
			meta, _ := doc["metadata"].(map[string]interface{})
			if meta == nil {
				continue
			}
			name, _ := meta["name"].(string)
			namespace, _ := meta["namespace"].(string)
			if _, ok := exempt[namespace+"/"+name]; ok {
				continue
			}
			if !podEnvReferencesSecret(doc) {
				continue
			}
			annotations, _ := meta["annotations"].(map[string]interface{})
			if auto, _ := annotations["reloader.stakater.com/auto"].(string); auto != "true" {
				t.Errorf(
					"%s: %s %s/%s consumes a Secret via env without reloader.stakater.com/auto=true — an env-frozen secret keeps its pre-rotation value until the next deploy; live-read it from a volume mount or add the annotation",
					path, kind, namespace, name,
				)
			}
		}
	})
}

func podEnvReferencesSecret(doc map[string]interface{}) bool {
	spec, _ := doc["spec"].(map[string]interface{})
	template, _ := spec["template"].(map[string]interface{})
	podSpec, _ := template["spec"].(map[string]interface{})
	if podSpec == nil {
		return false
	}
	for _, listKey := range []string{"containers", "initContainers"} {
		containers, _ := podSpec[listKey].([]interface{})
		for _, entry := range containers {
			container, _ := entry.(map[string]interface{})
			envs, _ := container["env"].([]interface{})
			for _, envEntry := range envs {
				envVar, _ := envEntry.(map[string]interface{})
				valueFrom, _ := envVar["valueFrom"].(map[string]interface{})
				if _, ok := valueFrom["secretKeyRef"]; ok {
					return true
				}
			}
			envFroms, _ := container["envFrom"].([]interface{})
			for _, envFromEntry := range envFroms {
				envFrom, _ := envFromEntry.(map[string]interface{})
				if _, ok := envFrom["secretRef"]; ok {
					return true
				}
			}
		}
	}
	return false
}
