package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	cloudflareDNSTokenSecretName = "cloudflare-guardianintelligence-org-dns-token"
	cloudflareDNSTokenSecretKey  = "api-token"
)

var cloudflareDNSTokenEnvVars = []string{
	"CLOUDFLARE_GUARDIAN_INTELLIGENCE_ORG_DNS_ZONE_API_TOKEN",
	"cloudflare_guardian_intelligence_org_dns_zone_api_token",
	// Backward-compatible with the current gitignored secret.env spelling.
	"cloudflare_guardian_intelligence_org_dnz_zone_api_token",
}

func lookupCloudflareDNSToken(getenv func(string) string) (string, string) {
	for _, key := range cloudflareDNSTokenEnvVars {
		if token := strings.TrimSpace(getenv(key)); token != "" {
			return token, key
		}
	}
	return "", ""
}

func lookupCloudflareDNSTokenSecretEnv(path string) (string, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", err
	}
	values := parseSecretEnv(raw)
	for _, key := range cloudflareDNSTokenEnvVars {
		if token := strings.TrimSpace(values[key]); token != "" {
			return token, path + ":" + key, nil
		}
	}
	return "", "", nil
}

func parseSecretEnv(raw []byte) map[string]string {
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if unquoted, err := strconv.Unquote(value); err == nil {
			value = unquoted
		}
		values[key] = value
	}
	return values
}

func applyCloudflareDNSTokenSecret(kubectl, kubeconfig string) error {
	token, source := lookupCloudflareDNSToken(os.Getenv)
	if token == "" {
		fileToken, fileSource, err := lookupCloudflareDNSTokenSecretEnv(resolvePath("secret.env"))
		if err != nil {
			return fmt.Errorf("read secret.env: %w", err)
		}
		token, source = fileToken, fileSource
	}
	if token == "" {
		if _, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "-n", "cert-manager", "get", "secret", cloudflareDNSTokenSecretName); err == nil {
			fmt.Fprintf(os.Stderr, "using existing cert-manager Cloudflare DNS token secret %s/%s\n", "cert-manager", cloudflareDNSTokenSecretName)
			return nil
		}
		return fmt.Errorf("up: oci.domain requires Cloudflare DNS-01 credentials; export one of %s or set it in ./secret.env", strings.Join(cloudflareDNSTokenEnvVars, ", "))
	}
	secret := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"metadata": map[string]string{
			"name":      cloudflareDNSTokenSecretName,
			"namespace": "cert-manager",
		},
		"type": "Opaque",
		"stringData": map[string]string{
			cloudflareDNSTokenSecretKey: token,
		},
	}
	raw, err := json.Marshal(secret)
	if err != nil {
		return fmt.Errorf("cloudflare dns token secret: %w", err)
	}
	if err := runToolInput(raw, kubectl, "--kubeconfig", kubeconfig, "apply", "-f", "-"); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "applied cert-manager Cloudflare DNS token secret from %s\n", source)
	return nil
}
