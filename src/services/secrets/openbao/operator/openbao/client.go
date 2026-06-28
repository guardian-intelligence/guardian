package openbao

import (
	"context"
	"errors"
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

type AuthBackend struct {
	Path        string
	Type        string
	Description string
}

type Mount struct {
	Path        string
	Type        string
	Description string
	Options     map[string]string
}

type KubernetesAuthConfig struct {
	KubernetesHost       string
	KubernetesCACert     string
	Issuer               string
	PEMKeys              []string
	DisableISSValidation bool
	DisableLocalCAJWT    bool
}

type TuneConfig struct {
	Description               string
	DefaultLeaseTTL           string
	MaxLeaseTTL               string
	ListingVisibility         string
	PassthroughRequestHeaders []string
	AllowedResponseHeaders    []string
	AuditNonHMACRequestKeys   []string
	AuditNonHMACResponseKeys  []string
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

func (c *Client) GetMount(ctx context.Context, path string) (Mount, bool, error) {
	mountPath := strings.Trim(path, "/")
	mounts, err := c.api.Sys().ListMountsWithContext(ctx)
	if err != nil {
		return Mount{}, false, err
	}
	mount, ok := mounts[mountKey(mountPath)]
	if !ok {
		mount, ok = mounts[mountPath]
	}
	if !ok {
		return Mount{}, false, nil
	}
	return Mount{
		Path:        mountPath,
		Type:        mount.Type,
		Description: mount.Description,
		Options:     copyStringMap(mount.Options),
	}, true, nil
}

func (c *Client) EnableMount(ctx context.Context, mount Mount) error {
	return c.api.Sys().MountWithContext(ctx, strings.Trim(mount.Path, "/"), &baoapi.MountInput{
		Type:        mount.Type,
		Description: mount.Description,
		Options:     copyStringMap(mount.Options),
	})
}

func (c *Client) DisableMount(ctx context.Context, path string) error {
	return c.api.Sys().UnmountWithContext(ctx, strings.Trim(path, "/"))
}

func (c *Client) GetMountTune(ctx context.Context, mountPath string) (TuneConfig, bool, error) {
	secret, err := c.api.Logical().ReadWithContext(ctx, mountTunePath(mountPath))
	if err != nil {
		if isOpenBaoNotFound(err) {
			return TuneConfig{}, false, nil
		}
		return TuneConfig{}, false, err
	}
	if secret == nil || secret.Data == nil {
		return TuneConfig{}, false, nil
	}
	return tuneConfigFromSecret(secret.Data), true, nil
}

func (c *Client) PutMountTune(ctx context.Context, mountPath string, tune TuneConfig) error {
	_, err := c.api.Logical().WriteWithContext(ctx, mountTunePath(mountPath), tuneConfigBody(tune))
	return err
}

func (c *Client) GetAuthBackend(ctx context.Context, path string) (AuthBackend, bool, error) {
	backendPath := strings.Trim(path, "/")
	mounts, err := c.api.Sys().ListAuthWithContext(ctx)
	if err != nil {
		return AuthBackend{}, false, err
	}
	mount, ok := mounts[authBackendKey(backendPath)]
	if !ok {
		mount, ok = mounts[backendPath]
	}
	if !ok {
		return AuthBackend{}, false, nil
	}
	return AuthBackend{
		Path:        backendPath,
		Type:        mount.Type,
		Description: mount.Description,
	}, true, nil
}

func (c *Client) EnableAuthBackend(ctx context.Context, backend AuthBackend) error {
	return c.api.Sys().EnableAuthWithOptionsWithContext(ctx, strings.Trim(backend.Path, "/"), &baoapi.EnableAuthOptions{
		Type:        backend.Type,
		Description: backend.Description,
	})
}

func (c *Client) DisableAuthBackend(ctx context.Context, path string) error {
	return c.api.Sys().DisableAuthWithContext(ctx, strings.Trim(path, "/"))
}

func (c *Client) GetKubernetesAuthConfig(ctx context.Context, backendPath string) (KubernetesAuthConfig, bool, error) {
	secret, err := c.api.Logical().ReadWithContext(ctx, kubernetesAuthConfigPath(backendPath))
	if err != nil {
		if isOpenBaoNotFound(err) {
			return KubernetesAuthConfig{}, false, nil
		}
		return KubernetesAuthConfig{}, false, err
	}
	if secret == nil || secret.Data == nil {
		return KubernetesAuthConfig{}, false, nil
	}
	return KubernetesAuthConfig{
		KubernetesHost:       stringFromSecret(secret.Data["kubernetes_host"]),
		KubernetesCACert:     stringFromSecret(secret.Data["kubernetes_ca_cert"]),
		Issuer:               stringFromSecret(secret.Data["issuer"]),
		PEMKeys:              stringSliceFromSecret(secret.Data["pem_keys"]),
		DisableISSValidation: boolFromSecret(secret.Data["disable_iss_validation"]),
		DisableLocalCAJWT:    boolFromSecret(secret.Data["disable_local_ca_jwt"]),
	}, true, nil
}

func (c *Client) PutKubernetesAuthConfig(ctx context.Context, backendPath string, config KubernetesAuthConfig) error {
	body := map[string]interface{}{
		"kubernetes_host":        config.KubernetesHost,
		"kubernetes_ca_cert":     config.KubernetesCACert,
		"issuer":                 config.Issuer,
		"pem_keys":               config.PEMKeys,
		"disable_iss_validation": config.DisableISSValidation,
		"disable_local_ca_jwt":   config.DisableLocalCAJWT,
	}
	_, err := c.api.Logical().WriteWithContext(ctx, kubernetesAuthConfigPath(backendPath), body)
	return err
}

func (c *Client) GetAuthTune(ctx context.Context, backendPath string) (TuneConfig, bool, error) {
	secret, err := c.api.Logical().ReadWithContext(ctx, authTunePath(backendPath))
	if err != nil {
		if isOpenBaoNotFound(err) {
			return TuneConfig{}, false, nil
		}
		return TuneConfig{}, false, err
	}
	if secret == nil || secret.Data == nil {
		return TuneConfig{}, false, nil
	}
	return tuneConfigFromSecret(secret.Data), true, nil
}

func (c *Client) PutAuthTune(ctx context.Context, backendPath string, tune TuneConfig) error {
	_, err := c.api.Logical().WriteWithContext(ctx, authTunePath(backendPath), tuneConfigBody(tune))
	return err
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

func authBackendKey(backendPath string) string {
	return strings.Trim(backendPath, "/") + "/"
}

func mountKey(mountPath string) string {
	return strings.Trim(mountPath, "/") + "/"
}

func authTunePath(backendPath string) string {
	return "sys/auth/" + strings.Trim(backendPath, "/") + "/tune"
}

func mountTunePath(mountPath string) string {
	return "sys/mounts/" + strings.Trim(mountPath, "/") + "/tune"
}

func kubernetesAuthConfigPath(backendPath string) string {
	return "auth/" + strings.Trim(backendPath, "/") + "/config"
}

func kubernetesAuthRolePath(backendPath string, roleName string) string {
	return "auth/" + strings.Trim(backendPath, "/") + "/role/" + strings.Trim(roleName, "/")
}

func isOpenBaoNotFound(err error) bool {
	var responseError *baoapi.ResponseError
	return errors.As(err, &responseError) && responseError.StatusCode == 404
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

func boolFromSecret(value interface{}) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		out, _ := strconv.ParseBool(v)
		return out
	default:
		return false
	}
}

func tuneConfigFromSecret(data map[string]interface{}) TuneConfig {
	return TuneConfig{
		Description:               stringFromSecret(data["description"]),
		DefaultLeaseTTL:           stringFromSecret(data["default_lease_ttl"]),
		MaxLeaseTTL:               stringFromSecret(data["max_lease_ttl"]),
		ListingVisibility:         stringFromSecret(data["listing_visibility"]),
		PassthroughRequestHeaders: stringSliceFromSecret(data["passthrough_request_headers"]),
		AllowedResponseHeaders:    stringSliceFromSecret(data["allowed_response_headers"]),
		AuditNonHMACRequestKeys:   stringSliceFromSecret(data["audit_non_hmac_request_keys"]),
		AuditNonHMACResponseKeys:  stringSliceFromSecret(data["audit_non_hmac_response_keys"]),
	}
}

func tuneConfigBody(tune TuneConfig) map[string]interface{} {
	body := map[string]interface{}{
		"description": tune.Description,
	}
	if tune.DefaultLeaseTTL != "" {
		body["default_lease_ttl"] = tune.DefaultLeaseTTL
	}
	if tune.MaxLeaseTTL != "" {
		body["max_lease_ttl"] = tune.MaxLeaseTTL
	}
	if tune.ListingVisibility != "" {
		body["listing_visibility"] = tune.ListingVisibility
	}
	if tune.PassthroughRequestHeaders != nil {
		body["passthrough_request_headers"] = tune.PassthroughRequestHeaders
	}
	if tune.AllowedResponseHeaders != nil {
		body["allowed_response_headers"] = tune.AllowedResponseHeaders
	}
	if tune.AuditNonHMACRequestKeys != nil {
		body["audit_non_hmac_request_keys"] = tune.AuditNonHMACRequestKeys
	}
	if tune.AuditNonHMACResponseKeys != nil {
		body["audit_non_hmac_response_keys"] = tune.AuditNonHMACResponseKeys
	}
	return body
}

func copyStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
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
