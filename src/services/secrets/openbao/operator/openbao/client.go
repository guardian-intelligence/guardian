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

type PKIRole struct {
	MountPath                 string
	RoleName                  string
	IssuerRef                 string
	TTL                       string
	MaxTTL                    string
	AllowLocalhost            bool
	AllowedDomains            []string
	AllowBareDomains          bool
	AllowSubdomains           bool
	AllowGlobDomains          bool
	AllowWildcardCertificates bool
	AllowAnyName              bool
	EnforceHostnames          bool
	AllowIPSANs               bool
	AllowedIPSANsCIDR         []string
	ServerFlag                bool
	ClientFlag                bool
	CodeSigningFlag           bool
	EmailProtectionFlag       bool
	KeyType                   string
	KeyBits                   int
	KeyUsage                  []string
	ExtKeyUsage               []string
	CNValidations             []string
	UseCSRCommonName          bool
	UseCSRSANs                bool
	GenerateLease             bool
	NoStore                   bool
	RequireCN                 bool
	NotBeforeDuration         string
	NotBeforeBound            string
	NotAfterBound             string
}

type PKIRootIssuer struct {
	MountPath  string
	IssuerName string
	CommonName string
	TTL        string
	KeyType    string
	KeyBits    int
}

type PKIIssuer struct {
	MountPath   string
	IssuerRef   string
	IssuerID    string
	IssuerName  string
	KeyID       string
	KeyName     string
	Certificate string
}

type PKIIssuerConfig struct {
	Default                    string
	DefaultFollowsLatestIssuer bool
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

func (c *Client) GetPKIRole(ctx context.Context, mountPath string, roleName string) (PKIRole, bool, error) {
	path := pkiRolePath(mountPath, roleName)
	secret, err := c.api.Logical().ReadWithContext(ctx, path)
	if err != nil {
		if isOpenBaoNotFound(err) {
			return PKIRole{}, false, nil
		}
		return PKIRole{}, false, err
	}
	if secret == nil || secret.Data == nil {
		return PKIRole{}, false, nil
	}
	return pkiRoleFromSecret(mountPath, roleName, secret.Data), true, nil
}

func (c *Client) PutPKIRole(ctx context.Context, role PKIRole) error {
	_, err := c.api.Logical().WriteWithContext(ctx, pkiRolePath(role.MountPath, role.RoleName), pkiRoleBody(role))
	return err
}

func (c *Client) DeletePKIRole(ctx context.Context, mountPath string, roleName string) error {
	_, err := c.api.Logical().DeleteWithContext(ctx, pkiRolePath(mountPath, roleName))
	return err
}

func (c *Client) GetPKIIssuer(ctx context.Context, mountPath string, issuerRef string) (PKIIssuer, bool, error) {
	path := pkiIssuerPath(mountPath, issuerRef)
	secret, err := c.api.Logical().ReadWithContext(ctx, path)
	if err != nil {
		if isOpenBaoNotFound(err) {
			return PKIIssuer{}, false, nil
		}
		return PKIIssuer{}, false, err
	}
	if secret == nil || secret.Data == nil {
		return PKIIssuer{}, false, nil
	}
	return pkiIssuerFromSecret(mountPath, issuerRef, secret.Data), true, nil
}

func (c *Client) GeneratePKIRootIssuer(ctx context.Context, issuer PKIRootIssuer) (PKIIssuer, error) {
	secret, err := c.api.Logical().WriteWithContext(ctx, pkiRootGenerateInternalPath(issuer.MountPath), pkiRootIssuerBody(issuer))
	if err != nil {
		return PKIIssuer{}, err
	}
	if secret == nil || secret.Data == nil {
		return PKIIssuer{}, fmt.Errorf("OpenBao PKI root issuer generation returned no data")
	}
	return pkiIssuerFromSecret(issuer.MountPath, issuer.IssuerName, secret.Data), nil
}

func (c *Client) GetPKIIssuerConfig(ctx context.Context, mountPath string) (PKIIssuerConfig, bool, error) {
	secret, err := c.api.Logical().ReadWithContext(ctx, pkiIssuerConfigPath(mountPath))
	if err != nil {
		if isOpenBaoNotFound(err) {
			return PKIIssuerConfig{}, false, nil
		}
		return PKIIssuerConfig{}, false, err
	}
	if secret == nil || secret.Data == nil {
		return PKIIssuerConfig{}, false, nil
	}
	return PKIIssuerConfig{
		Default:                    stringFromSecret(secret.Data["default"]),
		DefaultFollowsLatestIssuer: boolFromSecret(secret.Data["default_follows_latest_issuer"]),
	}, true, nil
}

func (c *Client) PutPKIIssuerConfig(ctx context.Context, mountPath string, config PKIIssuerConfig) error {
	_, err := c.api.Logical().WriteWithContext(ctx, pkiIssuerConfigPath(mountPath), map[string]interface{}{
		"default":                       config.Default,
		"default_follows_latest_issuer": config.DefaultFollowsLatestIssuer,
	})
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

func pkiRolePath(mountPath string, roleName string) string {
	return strings.Trim(mountPath, "/") + "/roles/" + strings.Trim(roleName, "/")
}

func pkiIssuerPath(mountPath string, issuerRef string) string {
	return strings.Trim(mountPath, "/") + "/issuer/" + strings.Trim(issuerRef, "/")
}

func pkiIssuerConfigPath(mountPath string) string {
	return strings.Trim(mountPath, "/") + "/config/issuers"
}

func pkiRootGenerateInternalPath(mountPath string) string {
	return strings.Trim(mountPath, "/") + "/root/generate/internal"
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

func intFromSecret(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		out, _ := strconv.Atoi(v)
		return out
	default:
		return 0
	}
}

func pkiRoleFromSecret(mountPath string, roleName string, data map[string]interface{}) PKIRole {
	return PKIRole{
		MountPath:                 strings.Trim(mountPath, "/"),
		RoleName:                  roleName,
		IssuerRef:                 stringFromSecret(data["issuer_ref"]),
		TTL:                       stringFromSecret(data["ttl"]),
		MaxTTL:                    stringFromSecret(data["max_ttl"]),
		AllowLocalhost:            boolFromSecret(data["allow_localhost"]),
		AllowedDomains:            stringSliceFromSecret(data["allowed_domains"]),
		AllowBareDomains:          boolFromSecret(data["allow_bare_domains"]),
		AllowSubdomains:           boolFromSecret(data["allow_subdomains"]),
		AllowGlobDomains:          boolFromSecret(data["allow_glob_domains"]),
		AllowWildcardCertificates: boolFromSecret(data["allow_wildcard_certificates"]),
		AllowAnyName:              boolFromSecret(data["allow_any_name"]),
		EnforceHostnames:          boolFromSecret(data["enforce_hostnames"]),
		AllowIPSANs:               boolFromSecret(data["allow_ip_sans"]),
		AllowedIPSANsCIDR:         stringSliceFromSecret(data["allowed_ip_sans_cidr"]),
		ServerFlag:                boolFromSecret(data["server_flag"]),
		ClientFlag:                boolFromSecret(data["client_flag"]),
		CodeSigningFlag:           boolFromSecret(data["code_signing_flag"]),
		EmailProtectionFlag:       boolFromSecret(data["email_protection_flag"]),
		KeyType:                   stringFromSecret(data["key_type"]),
		KeyBits:                   intFromSecret(data["key_bits"]),
		KeyUsage:                  stringSliceFromSecret(data["key_usage"]),
		ExtKeyUsage:               stringSliceFromSecret(data["ext_key_usage"]),
		CNValidations:             stringSliceFromSecret(data["cn_validations"]),
		UseCSRCommonName:          boolFromSecret(data["use_csr_common_name"]),
		UseCSRSANs:                boolFromSecret(data["use_csr_sans"]),
		GenerateLease:             boolFromSecret(data["generate_lease"]),
		NoStore:                   boolFromSecret(data["no_store"]),
		RequireCN:                 boolFromSecret(data["require_cn"]),
		NotBeforeDuration:         stringFromSecret(data["not_before_duration"]),
		NotBeforeBound:            stringFromSecret(data["not_before_bound"]),
		NotAfterBound:             stringFromSecret(data["not_after_bound"]),
	}
}

func pkiRoleBody(role PKIRole) map[string]interface{} {
	body := map[string]interface{}{
		"allow_localhost":             role.AllowLocalhost,
		"allowed_domains":             role.AllowedDomains,
		"allow_bare_domains":          role.AllowBareDomains,
		"allow_subdomains":            role.AllowSubdomains,
		"allow_glob_domains":          role.AllowGlobDomains,
		"allow_wildcard_certificates": role.AllowWildcardCertificates,
		"allow_any_name":              role.AllowAnyName,
		"enforce_hostnames":           role.EnforceHostnames,
		"allow_ip_sans":               role.AllowIPSANs,
		"server_flag":                 role.ServerFlag,
		"client_flag":                 role.ClientFlag,
		"code_signing_flag":           role.CodeSigningFlag,
		"email_protection_flag":       role.EmailProtectionFlag,
		"key_type":                    role.KeyType,
		"key_usage":                   role.KeyUsage,
		"use_csr_common_name":         role.UseCSRCommonName,
		"use_csr_sans":                role.UseCSRSANs,
		"generate_lease":              role.GenerateLease,
		"no_store":                    role.NoStore,
		"require_cn":                  role.RequireCN,
	}
	if role.AllowedIPSANsCIDR != nil {
		body["allowed_ip_sans_cidr"] = role.AllowedIPSANsCIDR
	}
	if role.ExtKeyUsage != nil {
		body["ext_key_usage"] = role.ExtKeyUsage
	}
	if role.CNValidations != nil {
		body["cn_validations"] = role.CNValidations
	}
	if role.IssuerRef != "" {
		body["issuer_ref"] = role.IssuerRef
	}
	if role.TTL != "" {
		body["ttl"] = role.TTL
	}
	if role.MaxTTL != "" {
		body["max_ttl"] = role.MaxTTL
	}
	if role.KeyBits != 0 {
		body["key_bits"] = role.KeyBits
	}
	if role.NotBeforeDuration != "" {
		body["not_before_duration"] = role.NotBeforeDuration
	}
	if role.NotBeforeBound != "" {
		body["not_before_bound"] = role.NotBeforeBound
	}
	if role.NotAfterBound != "" {
		body["not_after_bound"] = role.NotAfterBound
	}
	return body
}

func pkiIssuerFromSecret(mountPath string, issuerRef string, data map[string]interface{}) PKIIssuer {
	return PKIIssuer{
		MountPath:   strings.Trim(mountPath, "/"),
		IssuerRef:   strings.Trim(issuerRef, "/"),
		IssuerID:    stringFromSecret(data["issuer_id"]),
		IssuerName:  stringFromSecret(data["issuer_name"]),
		KeyID:       stringFromSecret(data["key_id"]),
		KeyName:     stringFromSecret(data["key_name"]),
		Certificate: stringFromSecret(data["certificate"]),
	}
}

func pkiRootIssuerBody(issuer PKIRootIssuer) map[string]interface{} {
	body := map[string]interface{}{
		"issuer_name": issuer.IssuerName,
		"key_name":    issuer.IssuerName,
		"common_name": issuer.CommonName,
		"ttl":         issuer.TTL,
		"key_type":    issuer.KeyType,
	}
	if issuer.KeyBits != 0 {
		body["key_bits"] = issuer.KeyBits
	}
	return body
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
