package openbao

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	baoapi "github.com/openbao/openbao/api/v2"
)

const defaultKubernetesJWTPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"

type Config struct {
	Address            string
	KubernetesAuthPath string
	KubernetesAuthRole string
	KubernetesJWTPath  string
}

type Client struct {
	api *baoapi.Client
}

type KubernetesAuthRole struct {
	BackendPath                   string
	RoleName                      string
	BoundServiceAccountNames      []string
	BoundServiceAccountNamespaces []string
	Audience                      string
	TokenPolicies                 []string
	TokenTTL                      string
	TokenMaxTTL                   string
}

func LoadConfigFromEnv() (Config, error) {
	cfg := Config{
		Address:            os.Getenv("OPENBAO_ADDR"),
		KubernetesAuthPath: os.Getenv("OPENBAO_AUTH_PATH"),
		KubernetesAuthRole: os.Getenv("OPENBAO_AUTH_ROLE"),
		KubernetesJWTPath:  os.Getenv("OPENBAO_KUBERNETES_JWT_PATH"),
	}
	if cfg.Address == "" {
		return Config{}, fmt.Errorf("OPENBAO_ADDR is required")
	}
	if cfg.KubernetesAuthPath == "" {
		cfg.KubernetesAuthPath = "kubernetes"
	}
	if cfg.KubernetesAuthRole == "" {
		return Config{}, fmt.Errorf("OPENBAO_AUTH_ROLE is required")
	}
	if cfg.KubernetesJWTPath == "" {
		cfg.KubernetesJWTPath = defaultKubernetesJWTPath
	}
	return cfg, nil
}

func NewClientFromEnv() (*baoapi.Client, error) {
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		return nil, err
	}
	apiConfig := baoapi.DefaultConfig()
	apiConfig.Address = cfg.Address
	return baoapi.NewClient(apiConfig)
}

func NewAuthenticatedClientFromEnv(ctx context.Context) (*Client, error) {
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		return nil, err
	}
	apiConfig := baoapi.DefaultConfig()
	apiConfig.Address = cfg.Address
	apiClient, err := baoapi.NewClient(apiConfig)
	if err != nil {
		return nil, err
	}
	jwt, err := os.ReadFile(cfg.KubernetesJWTPath)
	if err != nil {
		return nil, fmt.Errorf("read Kubernetes service account token: %w", err)
	}
	secret, err := apiClient.Logical().WriteWithContext(ctx, loginPath(cfg.KubernetesAuthPath), map[string]interface{}{
		"role": cfg.KubernetesAuthRole,
		"jwt":  strings.TrimSpace(string(jwt)),
	})
	if err != nil {
		return nil, fmt.Errorf("login to OpenBao with Kubernetes auth: %w", err)
	}
	if secret == nil || secret.Auth == nil || secret.Auth.ClientToken == "" {
		return nil, fmt.Errorf("login to OpenBao with Kubernetes auth returned no client token")
	}
	apiClient.SetToken(secret.Auth.ClientToken)
	return &Client{api: apiClient}, nil
}

func (c *Client) GetPolicy(ctx context.Context, name string) (string, error) {
	return c.api.Sys().GetPolicyWithContext(ctx, name)
}

func (c *Client) PutPolicy(ctx context.Context, name string, rules string) error {
	return c.api.Sys().PutPolicyWithContext(ctx, name, rules)
}

func (c *Client) DeletePolicy(ctx context.Context, name string) error {
	return c.api.Sys().DeletePolicyWithContext(ctx, name)
}

func (c *Client) GetKubernetesAuthRole(ctx context.Context, backendPath string, roleName string) (KubernetesAuthRole, bool, error) {
	path := kubernetesAuthRolePath(backendPath, roleName)
	secret, err := c.api.Logical().ReadWithContext(ctx, path)
	if err != nil {
		return KubernetesAuthRole{}, false, err
	}
	if secret == nil || secret.Data == nil {
		return KubernetesAuthRole{}, false, nil
	}
	return KubernetesAuthRole{
		BackendPath:                   strings.Trim(backendPath, "/"),
		RoleName:                      roleName,
		BoundServiceAccountNames:      stringSliceFromSecret(secret.Data["bound_service_account_names"]),
		BoundServiceAccountNamespaces: stringSliceFromSecret(secret.Data["bound_service_account_namespaces"]),
		Audience:                      stringFromSecret(secret.Data["audience"]),
		TokenPolicies:                 stringSliceFromSecret(secret.Data["token_policies"]),
		TokenTTL:                      stringFromSecret(secret.Data["token_ttl"]),
		TokenMaxTTL:                   stringFromSecret(secret.Data["token_max_ttl"]),
	}, true, nil
}

func (c *Client) PutKubernetesAuthRole(ctx context.Context, role KubernetesAuthRole) error {
	body := map[string]interface{}{
		"bound_service_account_names":      role.BoundServiceAccountNames,
		"bound_service_account_namespaces": role.BoundServiceAccountNamespaces,
	}
	if role.Audience != "" {
		body["audience"] = role.Audience
	}
	if role.TokenPolicies != nil {
		body["token_policies"] = role.TokenPolicies
	}
	if role.TokenTTL != "" {
		body["token_ttl"] = role.TokenTTL
	}
	if role.TokenMaxTTL != "" {
		body["token_max_ttl"] = role.TokenMaxTTL
	}
	_, err := c.api.Logical().WriteWithContext(ctx, kubernetesAuthRolePath(role.BackendPath, role.RoleName), body)
	return err
}

func (c *Client) DeleteKubernetesAuthRole(ctx context.Context, backendPath string, roleName string) error {
	_, err := c.api.Logical().DeleteWithContext(ctx, kubernetesAuthRolePath(backendPath, roleName))
	return err
}

func loginPath(authPath string) string {
	return "auth/" + strings.Trim(authPath, "/") + "/login"
}

func kubernetesAuthRolePath(backendPath string, roleName string) string {
	return "auth/" + strings.Trim(backendPath, "/") + "/role/" + strings.Trim(roleName, "/")
}

func stringFromSecret(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	default:
		return ""
	}
}

func stringSliceFromSecret(value interface{}) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []interface{}:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	case string:
		if v == "" {
			return nil
		}
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			item := strings.TrimSpace(part)
			if item != "" {
				out = append(out, item)
			}
		}
		return out
	default:
		return nil
	}
}
