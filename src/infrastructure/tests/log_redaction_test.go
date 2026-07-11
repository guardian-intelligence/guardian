package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"testing"
)

func TestLogCredentialRedactorsAreIdenticalAndWired(t *testing.T) {
	monitoringPath := runfilePath("src/infrastructure/base/platform-patches/cozystack-monitoring-agents.yaml")
	monitoringPackage := singleYAMLDoc(t, monitoringPath)
	fluentBit := nestedMap(t, monitoringPackage, "spec", "components", "monitoring-agents", "values", "fluent-bit")
	luaScripts := mapValue(fluentBit["luaScripts"])
	monitoringScript := stringValue(luaScripts["redact_credentials.lua"])
	if monitoringScript == "" {
		t.Fatalf("%s does not define fluent-bit.luaScripts.redact_credentials.lua", monitoringPath)
	}

	config := mapValue(fluentBit["config"])
	if _, overridesChartFilters := config["filters"]; overridesChartFilters {
		t.Fatalf("%s overrides the monitoring-agents chart filter chain; the redactor must be prepended to config.outputs so chart-owned Kubernetes/event/audit filters remain intact", monitoringPath)
	}
	monitoringPipeline := stringValue(config["outputs"])
	assertRedactorFilter(t, monitoringPipeline, "/fluent-bit/scripts/redact_credentials.lua", monitoringPath)

	talosPath := runfilePath("src/infrastructure/base/observability/talos-log-receiver.yaml")
	talosConfigMap := findDoc(t, yamlDocs(t, talosPath), "ConfigMap", "talos-log-receiver")
	talosData := nestedMap(t, talosConfigMap, "data")
	talosScript := stringValue(talosData["redact_credentials.lua"])
	if talosScript == "" {
		t.Fatalf("%s does not define data.redact_credentials.lua", talosPath)
	}
	if talosScript != monitoringScript {
		t.Fatalf("the monitoring-agents and Talos credential redactors differ; both ingestion paths must run the same sanitizer")
	}
	talosPipeline := stringValue(talosData["fluent-bit.conf"])
	assertRedactorFilter(t, talosPipeline, "/fluent-bit/etc/conf/redact_credentials.lua", talosPath)

	for _, pipeline := range []string{monitoringPipeline, talosPipeline} {
		for _, line := range strings.Split(pipeline, "\n") {
			if strings.Contains(line, "_stream_fields=") {
				assertTextNotContains(t, line, "guardian_redacted", "VictoriaLogs stream fields")
				assertTextNotContains(t, line, "guardian_redaction_rule", "VictoriaLogs stream fields")
			}
		}
	}
}

func TestLogCredentialRedactorContract(t *testing.T) {
	path := runfilePath("src/infrastructure/base/platform-patches/cozystack-monitoring-agents.yaml")
	monitoringPackage := singleYAMLDoc(t, path)
	fluentBit := nestedMap(t, monitoringPackage, "spec", "components", "monitoring-agents", "values", "fluent-bit")
	script := stringValue(mapValue(fluentBit["luaScripts"])["redact_credentials.lua"])

	for _, key := range []string{
		"access_token",
		"api_key",
		"authorization",
		"client_secret",
		"id_token",
		"password",
		"private_key",
		"refresh_token",
		"x_api_key",
	} {
		assertTextContains(t, script, "\""+key+"\",", path)
	}
	for _, broadKey := range []string{"secret", "token", "key"} {
		assertTextNotContains(t, script, "\n  \""+broadKey+"\",", path)
	}

	for _, contract := range []string{
		`record.guardian_redacted = "true"`,
		`record.guardian_redaction_rule = table.concat(applied, ",")`,
		`return 0, timestamp, record`,
		`return 2, timestamp, record`,
		`assert(unchanged_code == 0)`,
		`assert(redacted_code == 2)`,
		`assert(flb_null ~= nil)`,
		`SENSITIVE_KEYS[canonical] = true`,
		`if canonical == "authorization" then`,
		`string.gsub(base, "_", "-")`,
		`"authorization,known_credential,sensitive_field,url_query"`,
		`"tls: x509: certificate signed by unknown authority\nstack trace preserved"`,
		`"Back-off restarting failed container openbao-0"`,
		`"OpenBao core: security barrier not initialized; service account token expired"`,
		`"/api/v1/namespaces/default/secrets?limit=500&token=page&key=name"`,
		`"POST https://alerta.invalid/api?api-key=[REDACTED]&limit=1"`,
		`"Authorization: Bearer [REDACTED]"`,
		`"password=[REDACTED]\nstack trace preserved"`,
		`"/callback?clientSecret=[REDACTED]&next=/ok"`,
		`nested = { password = fake, privateKey = fake, accessToken = fake, xApiKey = fake, status = 401 }`,
		`assert(structured.guardian_redaction_rule == "authorization")`,
		`assert(redacted.unknown_auth == "Authorization: [REDACTED]")`,
		`assert(redacted.multiple_auth == "authorization=\"[REDACTED]\" authorization=[REDACTED]")`,
		`assert(redacted.quoted == "password=\"[REDACTED]\" suffix=preserved")`,
		`assert(redacted.quoted_auth == "authorization=\"[REDACTED]\" suffix=preserved")`,
		`assert(redacted.openbao == REDACTED .. " " .. REDACTED .. " " .. REDACTED)`,
		`assert(redacted.vault == REDACTED .. " " .. REDACTED .. " " .. REDACTED)`,
		`local pem_begin = "-----BEGIN " .. "ED25519 PRIVATE KEY-----"`,
		`assert(redacted.pem == "prefix " .. pem_begin .. "\n[REDACTED]\n" .. pem_end .. " suffix")`,
		`assert(redacted.nested.privateKey == REDACTED and redacted.nested.xApiKey == REDACTED)`,
		`assert(unchanged.split_pem == split_pem)`,
		`A PEM block split across CRI records cannot be recognized without cross-record state.`,
		`self_test()`,
	} {
		assertTextContains(t, script, contract, path)
	}
	assertTextNotContains(t, script, "return -1", path)
}

func TestTalosLogReceiverRolloutChecksum(t *testing.T) {
	path := runfilePath("src/infrastructure/base/observability/talos-log-receiver.yaml")
	docs := yamlDocs(t, path)
	configMap := findDoc(t, docs, "ConfigMap", "talos-log-receiver")
	deployment := findDoc(t, docs, "Deployment", "talos-log-receiver")
	data := nestedMap(t, configMap, "data")
	annotations := nestedMap(t, deployment, "spec", "template", "metadata", "annotations")
	got := stringValue(annotations["guardian.dev/config-checksum"])
	want := checksumConfigMapData(t, data)
	if got != want {
		t.Fatalf("%s: Talos receiver pod-template checksum = %q, want %q for the embedded ConfigMap data", path, got, want)
	}
}

func checksumConfigMapData(t *testing.T, data map[string]any) string {
	t.Helper()
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		value, ok := data[key].(string)
		if !ok {
			t.Fatalf("ConfigMap data %q is %T, want string", key, data[key])
		}
		hash.Write([]byte(key))
		hash.Write([]byte{0})
		hash.Write([]byte(value))
		hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func assertRedactorFilter(t *testing.T, pipeline, scriptPath, context string) {
	t.Helper()
	filter := "[FILTER]\n" +
		"    Name lua\n" +
		"    Alias guardian_redactor\n" +
		"    Match *\n" +
		"    Script " + scriptPath + "\n" +
		"    Call redact_credentials\n" +
		"    Enable_Flb_Null On"
	assertTextContains(t, pipeline, filter, context)
	if filterPosition, outputPosition := strings.Index(pipeline, filter), strings.Index(pipeline, "[OUTPUT]"); outputPosition >= 0 && filterPosition > outputPosition {
		t.Fatalf("%s places the credential redactor after an output", context)
	}
}
