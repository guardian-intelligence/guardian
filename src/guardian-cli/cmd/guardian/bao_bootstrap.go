package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	baoKVMountPath              = "kv"
	baoKubernetesAuthMountPath  = "kubernetes"
	baoObservabilityPolicyName  = "observability-secrets"
	baoObservabilityRoleName    = "observability-secrets"
	baoObservabilitySAName      = "external-secrets-observability"
	baoObservabilitySANamespace = "observability"
	baoOCIPolicyName            = "guardian-oci-secrets"
	baoOCIRoleName              = "guardian-oci-secrets"
	baoOCISAName                = "external-secrets-guardian-oci"
	baoOCISANamespace           = "guardian-oci"
	baoExternalSecretsAudience  = "openbao"
	baoZotPublisherUsername     = "guardian-release"
	baoRootTokenEnv             = "GUARDIAN_OPENBAO_TOKEN"
	baoUnsealKeyEnv             = "GUARDIAN_OPENBAO_UNSEAL_KEY"
	baoUnsealKeysEnv            = "GUARDIAN_OPENBAO_UNSEAL_KEYS"
	baoAllowSecretMigrationEnv  = "GUARDIAN_OPENBAO_ALLOW_SECRET_MIGRATION"
)

type baoRequiredSecret struct {
	name     string
	path     func(*Site) string
	required []string
	generate func() (map[string]string, error)
	enabled  func(*Site) bool
}

var requiredBaoSecrets = []baoRequiredSecret{{
	name:     "clickhouse-admin",
	required: []string{"password"},
	generate: passwordSecretData,
	path: func(site *Site) string {
		return "guardian/" + site.Cluster.Name + "/observability/clickhouse-admin"
	},
}, {
	name:     "grafana-admin",
	required: []string{"password"},
	generate: passwordSecretData,
	path: func(site *Site) string {
		return "guardian/" + site.Cluster.Name + "/observability/grafana-admin"
	},
}, {
	name:     "zot-publisher",
	required: []string{"username", "password", "htpasswd"},
	generate: zotPublisherSecretData,
	enabled:  siteUsesPlatformTLS,
	path: func(site *Site) string {
		return "guardian/" + site.Cluster.Name + "/oci/zot-publisher"
	},
}}

type baoInitResult struct {
	KeysB64   []string `json:"keys_base64"`
	RootToken string   `json:"root_token"`
}

func initFreshBao(addr string) (*baoInitResult, error) {
	var initResp baoInitResult
	body := strings.NewReader(`{"secret_shares":1,"secret_threshold":1}`)
	if err := baoAPI(addr, "PUT", "/v1/sys/init", "", body, &initResp); err != nil {
		return nil, fmt.Errorf("openbao init: %w", err)
	}
	if len(initResp.KeysB64) != 1 || initResp.RootToken == "" {
		return nil, errors.New("openbao init returned no unseal key or root token")
	}
	return &initResp, nil
}

func unsealBao(addr string, keys []string) error {
	if len(keys) == 0 {
		return errors.New("openbao unseal requires at least one key")
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		var unsealResp struct {
			Sealed bool `json:"sealed"`
		}
		body, err := json.Marshal(map[string]string{"key": key})
		if err != nil {
			return err
		}
		if err := baoAPI(addr, "PUT", "/v1/sys/unseal", "", strings.NewReader(string(body)), &unsealResp); err != nil {
			return fmt.Errorf("openbao unseal: %w", err)
		}
		if !unsealResp.Sealed {
			return nil
		}
	}
	return errors.New("openbao remains sealed after provided unseal keys")
}

func openBaoUnsealKeysFromEnv() []string {
	if one := strings.TrimSpace(os.Getenv(baoUnsealKeyEnv)); one != "" {
		return []string{one}
	}
	raw := strings.TrimSpace(os.Getenv(baoUnsealKeysEnv))
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	keys := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			keys = append(keys, f)
		}
	}
	return keys
}

func configureBaoForProjection(addr, token string, site *Site, allowCreateMissing bool) error {
	if token == "" {
		return errors.New("openbao configuration requires a token")
	}
	if err := ensureBaoKVMount(addr, token); err != nil {
		return err
	}
	if err := ensureBaoKubernetesAuth(addr, token, site); err != nil {
		return err
	}
	for _, secret := range requiredBaoSecrets {
		if secret.enabled != nil && !secret.enabled(site) {
			continue
		}
		if err := ensureBaoRequiredSecret(addr, token, secret, site, allowCreateMissing); err != nil {
			return err
		}
	}
	return nil
}

func ensureBaoKVMount(addr, token string) error {
	mounts, err := baoMounts(addr, token)
	if err != nil {
		return err
	}
	if mount, ok := mounts[baoKVMountPath+"/"]; ok {
		if mount.Type != "kv" || mount.Options["version"] != "2" {
			return fmt.Errorf("openbao mount %s/ exists as type=%q version=%q, want kv v2", baoKVMountPath, mount.Type, mount.Options["version"])
		}
		return nil
	}
	body := map[string]any{
		"type": "kv",
		"options": map[string]string{
			"version": "2",
		},
	}
	if err := baoJSON(addr, "POST", "/v1/sys/mounts/"+baoKVMountPath, token, body, nil); err != nil {
		return fmt.Errorf("openbao enable kv v2 at %s/: %w", baoKVMountPath, err)
	}
	return nil
}

func ensureBaoKubernetesAuth(addr, token string, site *Site) error {
	auths, err := baoAuths(addr, token)
	if err != nil {
		return err
	}
	if mount, ok := auths[baoKubernetesAuthMountPath+"/"]; ok {
		if mount.Type != "kubernetes" {
			return fmt.Errorf("openbao auth mount %s/ exists as type=%q, want kubernetes", baoKubernetesAuthMountPath, mount.Type)
		}
	} else {
		if err := baoJSON(addr, "POST", "/v1/sys/auth/"+baoKubernetesAuthMountPath, token, map[string]string{"type": "kubernetes"}, nil); err != nil {
			return fmt.Errorf("openbao enable kubernetes auth: %w", err)
		}
	}
	if err := baoJSON(addr, "POST", "/v1/auth/"+baoKubernetesAuthMountPath+"/config", token, map[string]any{
		"kubernetes_host": "https://kubernetes.default.svc",
	}, nil); err != nil {
		return fmt.Errorf("openbao configure kubernetes auth: %w", err)
	}
	observabilityPolicy := fmt.Sprintf(`path "%s/data/guardian/%s/observability/*" {
  capabilities = ["read"]
}
`, baoKVMountPath, site.Cluster.Name)
	if err := baoJSON(addr, "PUT", "/v1/sys/policies/acl/"+baoObservabilityPolicyName, token, map[string]string{"policy": observabilityPolicy}, nil); err != nil {
		return fmt.Errorf("openbao write %s policy: %w", baoObservabilityPolicyName, err)
	}
	if err := ensureBaoKubernetesRole(addr, token, baoObservabilityRoleName, baoObservabilitySAName, baoObservabilitySANamespace, baoObservabilityPolicyName); err != nil {
		return err
	}
	if !siteUsesPlatformTLS(site) {
		return nil
	}
	ociPolicy := fmt.Sprintf(`path "%s/data/guardian/%s/oci/*" {
  capabilities = ["read"]
}
`, baoKVMountPath, site.Cluster.Name)
	if err := baoJSON(addr, "PUT", "/v1/sys/policies/acl/"+baoOCIPolicyName, token, map[string]string{"policy": ociPolicy}, nil); err != nil {
		return fmt.Errorf("openbao write %s policy: %w", baoOCIPolicyName, err)
	}
	return ensureBaoKubernetesRole(addr, token, baoOCIRoleName, baoOCISAName, baoOCISANamespace, baoOCIPolicyName)
}

func ensureBaoKubernetesRole(addr, token, roleName, serviceAccountName, serviceAccountNamespace, policyName string) error {
	role := map[string]any{
		"bound_service_account_names":      []string{serviceAccountName},
		"bound_service_account_namespaces": []string{serviceAccountNamespace},
		"audience":                         baoExternalSecretsAudience,
		"token_policies":                   []string{policyName},
		"token_ttl":                        "1h",
		"token_max_ttl":                    "1h",
	}
	if err := baoJSON(addr, "POST", "/v1/auth/"+baoKubernetesAuthMountPath+"/role/"+roleName, token, role, nil); err != nil {
		return fmt.Errorf("openbao write %s role: %w", roleName, err)
	}
	return nil
}

func ensureBaoRequiredSecret(addr, token string, secret baoRequiredSecret, site *Site, allowCreateMissing bool) error {
	path := secret.path(site)
	var out struct {
		Data map[string]any `json:"data"`
	}
	err := baoAPI(addr, "GET", "/v1/"+baoKVMountPath+"/data/"+path, token, nil, &out)
	if err == nil {
		if out.Data == nil {
			return fmt.Errorf("openbao secret %s has no data", path)
		}
		data, ok := out.Data["data"].(map[string]any)
		if !ok {
			return fmt.Errorf("openbao secret %s has invalid data", path)
		}
		for _, key := range secret.required {
			if data[key] == "" {
				return fmt.Errorf("openbao secret %s missing %s", path, key)
			}
		}
		return nil
	}
	var httpErr *baoHTTPError
	if !errors.As(err, &httpErr) || httpErr.status != 404 {
		return fmt.Errorf("openbao read %s: %w", path, err)
	}
	if !allowCreateMissing {
		return fmt.Errorf("openbao required secret %s is absent; restore should reuse backed-up values, or set %s=1 for an intentional schema migration", path, baoAllowSecretMigrationEnv)
	}
	data, err := secret.generate()
	if err != nil {
		return fmt.Errorf("generate %s secret: %w", secret.name, err)
	}
	body := map[string]any{"data": data}
	if err := baoJSON(addr, "POST", "/v1/"+baoKVMountPath+"/data/"+path, token, body, nil); err != nil {
		return fmt.Errorf("openbao write %s: %w", path, err)
	}
	fmt.Fprintf(os.Stderr, "created OpenBao secret %s/%s\n", baoKVMountPath, path)
	return nil
}

type baoMount struct {
	Type    string            `json:"type"`
	Options map[string]string `json:"options"`
}

func baoMounts(addr, token string) (map[string]baoMount, error) {
	var out struct {
		Data map[string]baoMount `json:"data"`
	}
	if err := baoAPI(addr, "GET", "/v1/sys/mounts", token, nil, &out); err != nil {
		return nil, fmt.Errorf("openbao list mounts: %w", err)
	}
	return out.Data, nil
}

func baoAuths(addr, token string) (map[string]baoMount, error) {
	var out struct {
		Data map[string]baoMount `json:"data"`
	}
	if err := baoAPI(addr, "GET", "/v1/sys/auth", token, nil, &out); err != nil {
		return nil, fmt.Errorf("openbao list auth mounts: %w", err)
	}
	return out.Data, nil
}

func baoJSON(addr, method, path, token string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return baoAPI(addr, method, path, token, strings.NewReader(string(raw)), out)
}

func randomSecretString() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func passwordSecretData() (map[string]string, error) {
	password, err := randomSecretString()
	if err != nil {
		return nil, err
	}
	return map[string]string{"password": password}, nil
}

func zotPublisherSecretData() (map[string]string, error) {
	password, err := randomSecretString()
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"username": baoZotPublisherUsername,
		"password": password,
		"htpasswd": baoZotPublisherUsername + ":" + string(hash) + "\n",
	}, nil
}

func allowBaoSecretMigrationFromEnv() bool {
	return os.Getenv(baoAllowSecretMigrationEnv) == "1"
}
