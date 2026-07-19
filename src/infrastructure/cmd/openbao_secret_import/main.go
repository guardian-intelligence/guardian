package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	baoapi "github.com/openbao/openbao/api/v2"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultEnvFile        = "custody.env"
	defaultNamespace      = "tenant-guardian"
	defaultService        = "guardian-openbao-active"
	defaultServiceAccount = "guardian-secret-importer"
	defaultAuthPath       = "kubernetes"
	defaultAuthRole       = "guardian-secret-importer"
	defaultCASecret       = "guardian-openbao-api-tls"
	defaultLocalPort      = 18200
	importerPolicy        = "guardian-secret-importer"

	// The transit signing key behind image countersignatures. Its material
	// must survive reinits (fresh material would orphan every existing
	// countersignature), so custody is the source of truth and this importer
	// is the restore-or-create path — see ensureTransitSigningKey.
	transitSigningKey = "guardian-images"
	transitBackupEnv  = "openbao_transit_images_signing_key_backup"
)

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type options struct {
	EnvFile        string
	ValidateOnly   bool
	DeleteEnvFile  bool
	Kubectl        string
	Kubeconfig     string
	KubeAPIServer  string
	RequestTimeout string
	Namespace      string
	Service        string
	ServiceAccount string
	AuthPath       string
	AuthRole       string
	CASecret       string
	LocalPort      int
}

type secretWrite struct {
	APIPath string
	Data    map[string]string
}

type kubectlRunner struct {
	bin            string
	kubeconfig     string
	kubeAPIServer  string
	requestTimeout string
	namespace      string
}

type kubeSecret struct {
	Data map[string]string `json:"data"`
}

func main() {
	var opts options
	flag.StringVar(&opts.EnvFile, "env-file", defaultEnvFile, "local env file to import")
	flag.BoolVar(&opts.ValidateOnly, "validate-only", false, "parse the env file and construct the import plan, then exit without touching the cluster (the reinit ceremony's pre-wipe gate)")
	flag.BoolVar(&opts.DeleteEnvFile, "delete-env-file", false, "delete env file after a successful import (custody.env normally lives in the restored custody bundle; wipe the bundle instead)")
	flag.StringVar(&opts.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&opts.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&opts.KubeAPIServer, "kube-api-server", "", "optional Kubernetes API server override for off-VLAN proof runs")
	flag.StringVar(&opts.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&opts.Namespace, "namespace", defaultNamespace, "OpenBao namespace")
	flag.StringVar(&opts.Service, "service", defaultService, "OpenBao service used for port-forward")
	flag.StringVar(&opts.ServiceAccount, "service-account", defaultServiceAccount, "service account used for Kubernetes auth TokenRequest")
	flag.StringVar(&opts.AuthPath, "auth-path", defaultAuthPath, "OpenBao Kubernetes auth path")
	flag.StringVar(&opts.AuthRole, "auth-role", defaultAuthRole, "temporary OpenBao Kubernetes auth role")
	flag.StringVar(&opts.CASecret, "ca-secret", defaultCASecret, "Secret containing OpenBao API ca.crt")
	flag.IntVar(&opts.LocalPort, "local-port", defaultLocalPort, "local port for OpenBao port-forward")
	flag.Parse()

	exitIfErr(run(context.Background(), opts))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func run(ctx context.Context, opts options) error {
	if err := validateOptions(opts); err != nil {
		return err
	}
	raw, err := os.ReadFile(opts.EnvFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", opts.EnvFile, err)
	}
	env, err := parseEnv(raw)
	if err != nil {
		return fmt.Errorf("parse %s: %w", opts.EnvFile, err)
	}
	writes, err := importPlan(env)
	if err != nil {
		return err
	}
	if opts.ValidateOnly {
		fmt.Printf("import plan valid: %d kv writes from %s\n", len(writes), opts.EnvFile)
		return nil
	}

	runner := kubectlRunner{
		bin:            opts.Kubectl,
		kubeconfig:     opts.Kubeconfig,
		kubeAPIServer:  opts.KubeAPIServer,
		requestTimeout: opts.RequestTimeout,
		namespace:      opts.Namespace,
	}
	caPEM, err := openBaoCA(ctx, runner, opts.CASecret)
	if err != nil {
		return err
	}
	jwt, err := serviceAccountToken(ctx, runner, opts.ServiceAccount)
	if err != nil {
		return err
	}
	forward, err := startPortForward(ctx, runner, opts.Service, opts.LocalPort)
	if err != nil {
		return err
	}
	defer forward.stop()

	client, err := authenticatedOpenBaoClient(ctx, opts.LocalPort, caPEM, opts.AuthPath, opts.AuthRole, jwt)
	if err != nil {
		return err
	}
	for _, write := range writes {
		if err := writeAndVerify(ctx, client, write); err != nil {
			return err
		}
		fmt.Printf("imported %s properties: %s\n", write.APIPath, strings.Join(sortedKeys(write.Data), ","))
	}
	if err := ensureTransitSigningKey(ctx, client, env, opts.EnvFile); err != nil {
		return err
	}
	if err := cleanupImporter(ctx, client, opts.AuthPath, opts.AuthRole); err != nil {
		return err
	}
	fmt.Printf("removed temporary OpenBao importer role %s and policy %s\n", opts.AuthRole, importerPolicy)
	if opts.DeleteEnvFile {
		if err := os.Remove(opts.EnvFile); err != nil {
			return fmt.Errorf("delete %s after successful import: %w", opts.EnvFile, err)
		}
		fmt.Printf("deleted %s after successful import\n", opts.EnvFile)
	}
	return nil
}

func validateOptions(opts options) error {
	if opts.Kubectl == "" && !opts.ValidateOnly {
		return errors.New("--kubectl is required")
	}
	for label, value := range map[string]string{
		"env-file":        opts.EnvFile,
		"namespace":       opts.Namespace,
		"service":         opts.Service,
		"service-account": opts.ServiceAccount,
		"auth-path":       opts.AuthPath,
		"auth-role":       opts.AuthRole,
		"ca-secret":       opts.CASecret,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("--%s is required", label)
		}
	}
	if opts.LocalPort <= 0 || opts.LocalPort > 65535 {
		return fmt.Errorf("--local-port %d is outside TCP port range", opts.LocalPort)
	}
	return nil
}

func parseEnv(raw []byte) (map[string]string, error) {
	out := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d has no '='", lineNo)
		}
		key = strings.TrimSpace(key)
		if !envNameRE.MatchString(key) {
			return nil, fmt.Errorf("line %d has invalid variable name %q", lineNo, key)
		}
		value, err := unquoteEnvValue(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("line %d value for %s: %w", lineNo, key, err)
		}
		out[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func unquoteEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, `'`) {
		if !strings.HasSuffix(value, `'`) || len(value) == 1 {
			return "", errors.New("unterminated single-quoted value")
		}
		return strings.TrimSuffix(strings.TrimPrefix(value, `'`), `'`), nil
	}
	if strings.HasPrefix(value, `"`) {
		return strconv.Unquote(value)
	}
	return value, nil
}

func importPlan(env map[string]string) ([]secretWrite, error) {
	required := []string{
		"cloudflare_account_id",
		"cloudflare_r2_secret_access_key",
		"cloudflare_r2_s3_api_endpoint",
		"cloudflare_r2_access_key_id",
		"cloudflare_origin_certificate_b64",
		"cloudflare_origin_private_key_b64",
		"guardian_alerting_ntfy_url",
		"platform_admin_password",
		"platform_agent_password",
		"github_promotions_app_private_key_b64",
		"github_runner_app_prod_app_id",
		"github_runner_app_prod_webhook_secret",
		"github_runner_app_prod_private_key_b64",
		"github_projector_pat",
		"zot_countersigner_password",
	}
	var missing []string
	for _, key := range required {
		if strings.TrimSpace(env[key]) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required import variables: %s", strings.Join(missing, ","))
	}
	// The PEMs travel base64-encoded because the env file is line-oriented.
	githubAppKey, err := decodePEMEnv(env, "github_promotions_app_private_key_b64")
	if err != nil {
		return nil, err
	}
	runnerAppKey, err := decodePEMEnv(env, "github_runner_app_prod_private_key_b64")
	if err != nil {
		return nil, err
	}
	originCertificate, err := decodePEMEnv(env, "cloudflare_origin_certificate_b64")
	if err != nil {
		return nil, err
	}
	originPrivateKey, err := decodePEMEnv(env, "cloudflare_origin_private_key_b64")
	if err != nil {
		return nil, err
	}
	zotHash, err := bcrypt.GenerateFromPassword([]byte(env["zot_countersigner_password"]), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("bcrypt zot_countersigner_password: %w", err)
	}
	zotPasswordHash := string(zotHash)
	// Every path's first segment under guardian-mgmt/ is the consuming
	// namespace (the per-namespace reader/writer roles are scoped to that
	// subtree); operator/ is the exception — custody reference material no
	// in-cluster role can read.
	// Cloudflare lane credentials (external-dns token, backups-r2 keypair)
	// are minted by the guardian-mgmt-cloudflare-tokens tofu root and relayed
	// post-import via scoped writers — see the static-seal runbook's re-relay
	// checklist. Only the tofu state-backend R2 keypair rides in custody: the
	// tokens root's own backend needs it before any state is readable.
	writes := []secretWrite{
		{
			APIPath: "kv/data/guardian/guardian-mgmt/operator/r2",
			Data: map[string]string{
				"cloudflare_r2_access_key_id":     env["cloudflare_r2_access_key_id"],
				"cloudflare_r2_s3_api_endpoint":   env["cloudflare_r2_s3_api_endpoint"],
				"cloudflare_r2_secret_access_key": env["cloudflare_r2_secret_access_key"],
			},
		},
		{
			APIPath: "kv/data/guardian/guardian-mgmt/tenant-root/cloudflare-origin-tls",
			Data: map[string]string{
				"tls.crt": originCertificate,
				"tls.key": originPrivateKey,
			},
		},
		// Alerting pager sink: the ntfy topic URL the tenant-root alert-relay
		// delivers to (deployments/alerting/secrets.yaml projects it as
		// Secret/alert-relay-config). The URL is a low-grade credential — the
		// topic name is the secret — which is why it lives in custody and
		// OpenBao rather than Git.
		{
			APIPath: "kv/data/guardian/guardian-mgmt/tenant-root/alerting",
			Data: map[string]string{
				"ntfy_url": env["guardian_alerting_ntfy_url"],
			},
		},
		// Platform Keycloak admin identities (cozy realm): per-identity
		// login passwords for the KeycloakRealmUser CRs declared in
		// base/cozystack/platform-admins.yaml. ESO projects them 1:1 into
		// Secret/platform-admin-passwords in tenant-root, which the EDP
		// keycloak-operator reads via each user's passwordSecret ref.
		{
			APIPath: "kv/data/guardian/guardian-mgmt/tenant-root/platform-admins",
			Data: map[string]string{
				"platform-admin": env["platform_admin_password"],
				"platform-agent": env["platform_agent_password"],
			},
		},
		{
			APIPath: "kv/data/guardian/guardian-mgmt/company-site/promotion/github-app",
			Data: map[string]string{
				"githubAppPrivateKey": githubAppKey,
			},
		},
		// Same App identity, second consumer namespace: Kargo credentials
		// are project-namespaced, and the scope model requires each
		// namespace to read only its own subtree.
		{
			APIPath: "kv/data/guardian/guardian-mgmt/guardian-iam/promotion/github-app",
			Data: map[string]string{
				"githubAppPrivateKey": githubAppKey,
			},
		},
		{
			APIPath: "kv/data/guardian/guardian-mgmt/guardian-products/promotion/github-app",
			Data: map[string]string{
				"githubAppPrivateKey": githubAppKey,
			},
		},
		// Postflight Runner GitHub App (prod): webhook HMAC secret and the App
		// private key that signs the JWTs installation tokens are minted from.
		// The public App ID rides along because the control plane consumes it.
		{
			APIPath: "kv/data/guardian/guardian-mgmt/postflight-runner/github-app",
			Data: map[string]string{
				"appId":               env["github_runner_app_prod_app_id"],
				"webhookSecret":       env["github_runner_app_prod_webhook_secret"],
				"githubAppPrivateKey": runnerAppKey,
			},
		},
		// zot push credential for the image countersigner: operator-minted
		// (like the Keycloak canary user), so a DR re-seed restores a value
		// consistent with the htpasswd hash zot mounts. `password` is the
		// docker login the countersigner presents; `htpasswd` is the file
		// line zot's auth mount consumes. zot accepts only bcrypt, and the
		// hash is derived here so custody carries a single secret (the hash
		// re-salts on every import, which zot is indifferent to).
		{
			APIPath: "kv/data/guardian/guardian-mgmt/tenant-guardian/zot-countersigner",
			Data: map[string]string{
				"password": env["zot_countersigner_password"],
				"htpasswd": "countersigner:" + zotPasswordHash,
			},
		},
		// ghcr publish credential for the release projector: a classic PAT
		// scoped to write:packages only, held by the machine account
		// guardian-projector — GitHub App installation tokens cannot write
		// ghcr organization packages, so this is the one standing PAT
		// (docs/secrets.md). Expires 2027-07-11: re-mint and re-import
		// before then, re-testing the App write path while at it — the PAT
		// exists only to be deleted.
		{
			APIPath: "kv/data/guardian/guardian-mgmt/tenant-guardian/github-projector",
			Data: map[string]string{
				"token": env["github_projector_pat"],
			},
		},
	}

	// Per-stage Keycloak secrets are optional: unlike the writes above, an
	// env file may legitimately carry only a subset of stages. Everything
	// about the GitHub OAuth Apps other than the client secret (app name,
	// settings id, homepage/callback URL, realm, idp alias, client ID) is
	// not sensitive and is checked into
	// src/infrastructure/deployments/iam/github-oauth-apps.yaml instead.
	// The bootstrap administrator lives in custody so a DR re-seed restores
	// a value consistent with the stage's surviving Keycloak database. The
	// browser canary credentials belong to a dedicated GitHub machine account.
	for _, stage := range []string{"staging", "prod"} {
		prefix := strings.ToUpper(stage)
		base := fmt.Sprintf("kv/data/guardian/guardian-mgmt/tenant-guardian-%s/keycloak", stage)
		if secret := strings.TrimSpace(env[prefix+"_GITHUB_CLIENT_SECRET"]); secret != "" {
			writes = append(writes, secretWrite{
				APIPath: base + "/github-oauth",
				Data: map[string]string{
					"GITHUB_CLIENT_SECRET": secret,
				},
			})
		}
		username := strings.TrimSpace(env[prefix+"_KEYCLOAK_ADMIN_BOOTSTRAP_USERNAME"])
		password := strings.TrimSpace(env[prefix+"_KEYCLOAK_ADMIN_BOOTSTRAP_PASSWORD"])
		if username != "" || password != "" {
			if username == "" || password == "" {
				return nil, fmt.Errorf("%s_KEYCLOAK_ADMIN_BOOTSTRAP_USERNAME and %s_KEYCLOAK_ADMIN_BOOTSTRAP_PASSWORD must be set together", prefix, prefix)
			}
			writes = append(writes, secretWrite{
				APIPath: base + "/admin-bootstrap",
				Data: map[string]string{
					"username": username,
					"password": password,
				},
			})
		}
		canaryUsername := strings.TrimSpace(env[prefix+"_GITHUB_LOGIN_CANARY_USERNAME"])
		canaryPassword := env[prefix+"_GITHUB_LOGIN_CANARY_PASSWORD"]
		canaryTOTP := strings.TrimSpace(env[prefix+"_GITHUB_LOGIN_CANARY_TOTP_SECRET"])
		if canaryUsername != "" || canaryPassword != "" || canaryTOTP != "" {
			if canaryUsername == "" || canaryPassword == "" || canaryTOTP == "" {
				return nil, fmt.Errorf("%s_GITHUB_LOGIN_CANARY_USERNAME, %s_GITHUB_LOGIN_CANARY_PASSWORD, and %s_GITHUB_LOGIN_CANARY_TOTP_SECRET must be set together", prefix, prefix, prefix)
			}
			writes = append(writes, secretWrite{
				APIPath: base + "/login-canary-github",
				Data: map[string]string{
					"username":    canaryUsername,
					"password":    canaryPassword,
					"totp_secret": canaryTOTP,
				},
			})
		}
	}

	return writes, nil
}

func decodePEMEnv(env map[string]string, key string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(env[key]))
	if err != nil {
		return "", fmt.Errorf("decode %s: %w", key, err)
	}
	if !strings.HasPrefix(string(decoded), "-----BEGIN") {
		return "", fmt.Errorf("%s does not decode to a PEM block", key)
	}
	return string(decoded), nil
}

func openBaoCA(ctx context.Context, runner kubectlRunner, secretName string) ([]byte, error) {
	out, err := runner.output(ctx, "read OpenBao CA Secret", "get", "secret/"+secretName, "-o", "json")
	if err != nil {
		return nil, err
	}
	var secret kubeSecret
	if err := json.Unmarshal([]byte(out), &secret); err != nil {
		return nil, fmt.Errorf("parse CA Secret: %w", err)
	}
	encoded := secret.Data["ca.crt"]
	if encoded == "" {
		return nil, fmt.Errorf("Secret %s has no ca.crt", secretName)
	}
	caPEM, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode Secret %s ca.crt: %w", secretName, err)
	}
	return caPEM, nil
}

func serviceAccountToken(ctx context.Context, runner kubectlRunner, serviceAccount string) (string, error) {
	out, err := runner.output(ctx, "create OpenBao importer TokenRequest", "create", "token", serviceAccount, "--audience=openbao", "--duration=10m")
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(out)
	if token == "" {
		return "", errors.New("kubectl create token returned an empty token")
	}
	return token, nil
}

func authenticatedOpenBaoClient(ctx context.Context, localPort int, caPEM []byte, authPath, authRole, jwt string) (*baoapi.Client, error) {
	cfg := baoapi.DefaultConfig()
	cfg.Address = fmt.Sprintf("https://127.0.0.1:%d", localPort)
	if err := cfg.ConfigureTLS(&baoapi.TLSConfig{CACertBytes: caPEM}); err != nil {
		return nil, fmt.Errorf("configure OpenBao TLS: %w", err)
	}
	client, err := baoapi.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	secret, err := client.Logical().WriteWithContext(ctx, "auth/"+strings.Trim(authPath, "/")+"/login", map[string]interface{}{
		"role": authRole,
		"jwt":  jwt,
	})
	if err != nil {
		return nil, fmt.Errorf("login to OpenBao importer role: %w", err)
	}
	if secret == nil || secret.Auth == nil || secret.Auth.ClientToken == "" {
		return nil, errors.New("login to OpenBao importer role returned no client token")
	}
	client.SetToken(secret.Auth.ClientToken)
	return client, nil
}

func writeAndVerify(ctx context.Context, client *baoapi.Client, write secretWrite) error {
	body := map[string]interface{}{"data": stringMapToInterface(write.Data)}
	if _, err := client.Logical().WriteWithContext(ctx, write.APIPath, body); err != nil {
		return fmt.Errorf("write %s: %w", write.APIPath, err)
	}
	secret, err := client.Logical().ReadWithContext(ctx, write.APIPath)
	if err != nil {
		return fmt.Errorf("verify %s: %w", write.APIPath, err)
	}
	if secret == nil || secret.Data == nil {
		return fmt.Errorf("verify %s: empty readback", write.APIPath)
	}
	data, ok := secret.Data["data"].(map[string]interface{})
	if !ok {
		return fmt.Errorf("verify %s: readback missing kv-v2 data", write.APIPath)
	}
	for key, want := range write.Data {
		if got, ok := data[key].(string); !ok || got != want {
			return fmt.Errorf("verify %s: property %s mismatch", write.APIPath, key)
		}
	}
	return nil
}

// ensureTransitSigningKey makes the guardian-images transit signing key live
// and custody-durable. A reinit recreates the transit mount empty, and fresh
// key material would orphan every countersignature already attached in the
// registry, so custody is the source of truth: when the env file carries the
// transit/backup blob the key is restored from it verbatim; on first run the
// key is created (exportable + allow_plaintext_backup, both required for the
// backup endpoint and irreversible once set) and its backup is exported next
// to the env file for the operator to fold into custody.
func ensureTransitSigningKey(ctx context.Context, client *baoapi.Client, env map[string]string, envFile string) error {
	if blob := strings.TrimSpace(env[transitBackupEnv]); blob != "" {
		// force makes a re-run after a partial import idempotent: restoring
		// the same backup over the same key is a no-op for the material.
		if _, err := client.Logical().WriteWithContext(ctx, "transit/restore/"+transitSigningKey, map[string]interface{}{
			"backup": blob,
			"force":  true,
		}); err != nil {
			return fmt.Errorf("restore transit signing key %s from custody backup: %w", transitSigningKey, err)
		}
		fmt.Printf("restored transit signing key %s from the custody backup\n", transitSigningKey)
		return printTransitKeyFingerprint(ctx, client)
	}
	if _, err := client.Logical().WriteWithContext(ctx, "transit/keys/"+transitSigningKey, map[string]interface{}{
		"type":                   "ecdsa-p256",
		"exportable":             true,
		"allow_plaintext_backup": true,
	}); err != nil {
		return fmt.Errorf("create transit signing key %s: %w", transitSigningKey, err)
	}
	backup, err := client.Logical().ReadWithContext(ctx, "transit/backup/"+transitSigningKey)
	if err != nil {
		return fmt.Errorf("export transit signing key %s backup: %w", transitSigningKey, err)
	}
	blob := ""
	if backup != nil && backup.Data != nil {
		blob, _ = backup.Data["backup"].(string)
	}
	if blob == "" {
		return fmt.Errorf("transit signing key %s backup export returned no payload", transitSigningKey)
	}
	outPath := filepath.Join(filepath.Dir(envFile), "openbao-transit-guardian-images.backup.b64")
	if err := os.WriteFile(outPath, []byte(blob), 0o600); err != nil {
		return fmt.Errorf("write transit signing key backup to %s: %w", outPath, err)
	}
	fmt.Printf("created transit signing key %s and exported its plaintext backup to %s\n", transitSigningKey, outPath)
	fmt.Printf("CUSTODY ACTION REQUIRED: append %s=<contents of %s> to custody.env, snapshot the bundle (aspect infra custody --action create), then delete the exported file — the key is not DR-durable until the snapshot lands, and the next reinit would otherwise mint new material and orphan every countersignature\n", transitBackupEnv, filepath.Base(outPath))
	return printTransitKeyFingerprint(ctx, client)
}

// printTransitKeyFingerprint records the public half for custody cross-checks
// (the same role the seal-key fingerprint plays): the operator can compare it
// against prior importer runs and against what a restore drill reproduces,
// without any private material leaving OpenBao.
func printTransitKeyFingerprint(ctx context.Context, client *baoapi.Client) error {
	key, err := client.Logical().ReadWithContext(ctx, "transit/keys/"+transitSigningKey)
	if err != nil {
		return fmt.Errorf("read transit signing key %s: %w", transitSigningKey, err)
	}
	if key == nil || key.Data == nil {
		return fmt.Errorf("read transit signing key %s: empty response", transitSigningKey)
	}
	latest := fmt.Sprintf("%v", key.Data["latest_version"])
	versions, _ := key.Data["keys"].(map[string]interface{})
	entry, _ := versions[latest].(map[string]interface{})
	publicKey, _ := entry["public_key"].(string)
	if publicKey == "" {
		return fmt.Errorf("transit signing key %s version %s has no public key in the read response", transitSigningKey, latest)
	}
	fmt.Printf("transit signing key %s: type=%v latest_version=%s public-key-sha256=%x\n",
		transitSigningKey, key.Data["type"], latest, sha256.Sum256([]byte(publicKey)))
	return nil
}

func cleanupImporter(ctx context.Context, client *baoapi.Client, authPath, authRole string) error {
	if _, err := client.Logical().DeleteWithContext(ctx, "auth/"+strings.Trim(authPath, "/")+"/role/"+strings.Trim(authRole, "/")); err != nil {
		return fmt.Errorf("delete OpenBao importer role %s: %w", authRole, err)
	}
	if err := client.Sys().DeletePolicyWithContext(ctx, importerPolicy); err != nil {
		return fmt.Errorf("delete OpenBao importer policy %s: %w", importerPolicy, err)
	}
	return nil
}

func stringMapToInterface(in map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func sortedKeys(in map[string]string) []string {
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type portForward struct {
	cmd    *exec.Cmd
	done   chan error
	output *bytes.Buffer
}

func startPortForward(ctx context.Context, runner kubectlRunner, service string, localPort int) (*portForward, error) {
	var output bytes.Buffer
	cmd := exec.CommandContext(ctx, runner.bin, runner.args("port-forward", "svc/"+service, fmt.Sprintf("%d:8200", localPort))...)
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start OpenBao port-forward: %w", err)
	}
	forward := &portForward{
		cmd:    cmd,
		done:   make(chan error, 1),
		output: &output,
	}
	go func() {
		forward.done <- cmd.Wait()
	}()
	if err := forward.wait(localPort); err != nil {
		forward.stop()
		return nil, err
	}
	return forward, nil
}

func (p *portForward) wait(localPort int) error {
	deadline := time.Now().Add(15 * time.Second)
	address := fmt.Sprintf("127.0.0.1:%d", localPort)
	for time.Now().Before(deadline) {
		select {
		case err := <-p.done:
			return fmt.Errorf("OpenBao port-forward exited before it was ready: %w\n%s", err, p.output.String())
		default:
		}
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("OpenBao port-forward did not become ready on %s\n%s", address, p.output.String())
}

func (p *portForward) stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
	}
}

func (r kubectlRunner) args(args ...string) []string {
	out := make([]string, 0, len(args)+8)
	if r.kubeconfig != "" {
		out = append(out, "--kubeconfig", r.kubeconfig)
	}
	if r.kubeAPIServer != "" {
		out = append(out, "--server", r.kubeAPIServer)
	}
	if r.requestTimeout != "" {
		out = append(out, "--request-timeout="+r.requestTimeout)
	}
	if r.namespace != "" {
		out = append(out, "-n", r.namespace)
	}
	out = append(out, args...)
	return out
}

func (r kubectlRunner) output(ctx context.Context, label string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin, r.args(args...)...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("%s: %w\n%s", label, err, buf.String())
	}
	return buf.String(), nil
}
