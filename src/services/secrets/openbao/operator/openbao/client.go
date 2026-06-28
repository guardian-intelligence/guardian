package openbao

import (
	"context"
	"fmt"
	"os"
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

func loginPath(authPath string) string {
	return "auth/" + strings.Trim(authPath, "/") + "/login"
}
