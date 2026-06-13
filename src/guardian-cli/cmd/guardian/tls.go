package main

import (
	"encoding/json"
	"fmt"
	"os"
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

func applyCloudflareDNSTokenSecret(kubectl, kubeconfig string) error {
	token, source := lookupCloudflareDNSToken(os.Getenv)
	if token == "" {
		if _, err := outputTool(kubectl, "--kubeconfig", kubeconfig, "-n", "cert-manager", "get", "secret", cloudflareDNSTokenSecretName); err == nil {
			fmt.Fprintf(os.Stderr, "using existing cert-manager Cloudflare DNS token secret %s/%s\n", "cert-manager", cloudflareDNSTokenSecretName)
			return nil
		}
		return fmt.Errorf("up: oci.domain requires Cloudflare DNS-01 credentials; export one of: %s", strings.Join(cloudflareDNSTokenEnvVars, ", "))
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
