package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type backupSecretsConfig struct {
	Kubectl               string
	Kubeconfig            string
	RequestTimeout        string
	WaitTimeout           string
	SyncWaitTimeout       string
	PortForwardReadyWait  time.Duration
	Namespace             string
	StatefulSet           string
	Service               string
	BootstrapSecret       string
	Endpoint              string
	Bucket                string
	Region                string
	Stages                string
	DryRun                bool
	ForceSync             bool
	AllowSharedCredential bool
}

type backupSecretCredential struct {
	AccessKeyID string
	SecretKey   string
	Source      string
}

type credentialScope struct {
	Stage     string
	Component string
}

type stageConfig struct {
	Name      string
	Namespace string
}

type secretWrite struct {
	Path             string
	Data             map[string]string
	CredentialSource string
}

type kubectlRunner struct {
	bin            string
	kubeconfig     string
	requestTimeout string
	namespace      string
}

type portForward struct {
	cmd *exec.Cmd
}

var dnsSubdomainRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

func main() {
	var cfg backupSecretsConfig
	var portForwardReadyWait string
	flag.StringVar(&cfg.Kubectl, "kubectl", "", "path to kubectl")
	flag.StringVar(&cfg.Kubeconfig, "kubeconfig", "", "kubeconfig for guardian-mgmt")
	flag.StringVar(&cfg.RequestTimeout, "request-timeout", "15s", "kubectl API request timeout")
	flag.StringVar(&cfg.WaitTimeout, "wait-timeout", "5m", "timeout waiting for OpenBao StatefulSet readiness")
	flag.StringVar(&cfg.SyncWaitTimeout, "sync-wait-timeout", "5m", "timeout waiting for ExternalSecrets to sync after OpenBao writes")
	flag.StringVar(&portForwardReadyWait, "port-forward-ready-timeout", "10s", "timeout waiting for kubectl port-forward readiness")
	flag.StringVar(&cfg.Namespace, "namespace", "tenant-root", "OpenBao namespace")
	flag.StringVar(&cfg.StatefulSet, "statefulset", "openbao-guardian", "OpenBao StatefulSet name")
	flag.StringVar(&cfg.Service, "service", "openbao-guardian", "OpenBao Service name")
	flag.StringVar(&cfg.BootstrapSecret, "bootstrap-secret", "openbao-guardian-bootstrap", "Kubernetes Secret containing cluster-local OpenBao bootstrap material")
	flag.StringVar(&cfg.Endpoint, "endpoint", "", "S3-compatible backup endpoint")
	flag.StringVar(&cfg.Bucket, "bucket", "guardian-vault", "S3-compatible backup bucket name")
	flag.StringVar(&cfg.Region, "region", "auto", "S3-compatible backup region")
	flag.StringVar(&cfg.Stages, "stages", "root,dev,gamma,prod", "comma-separated Guardian stages to populate: root, dev, gamma, prod")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "print target OpenBao paths and properties without writing secret values")
	flag.BoolVar(&cfg.ForceSync, "force-sync", true, "annotate ExternalSecrets with force-sync and wait for target Secrets")
	flag.BoolVar(&cfg.AllowSharedCredential, "allow-shared-backup-credential", false, "allow GUARDIAN_BACKUP_AWS_* to seed all selected backup paths; intended only for temporary bootstrap")
	flag.Parse()

	var err error
	cfg.PortForwardReadyWait, err = time.ParseDuration(portForwardReadyWait)
	exitIfErr(err)
	exitIfErr(validateConfig(cfg))
	exitIfErr(runBackupSecrets(context.Background(), cfg, os.Environ()))
}

func exitIfErr(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	os.Exit(1)
}

func validateConfig(cfg backupSecretsConfig) error {
	if cfg.Kubectl == "" {
		return errors.New("--kubectl is required")
	}
	if cfg.Endpoint == "" {
		return errors.New("--endpoint is required")
	}
	if cfg.Bucket == "" {
		return errors.New("--bucket is required")
	}
	if cfg.Region == "" {
		return errors.New("--region is required")
	}
	if cfg.PortForwardReadyWait <= 0 {
		return errors.New("--port-forward-ready-timeout must be positive")
	}
	for label, value := range map[string]string{
		"namespace":        cfg.Namespace,
		"statefulset":      cfg.StatefulSet,
		"service":          cfg.Service,
		"bootstrap-secret": cfg.BootstrapSecret,
	} {
		if !dnsSubdomainRE.MatchString(value) {
			return fmt.Errorf("--%s %q is not a Kubernetes DNS subdomain", label, value)
		}
	}
	if _, err := parseStages(cfg.Stages); err != nil {
		return err
	}
	return nil
}

func runBackupSecrets(ctx context.Context, cfg backupSecretsConfig, env []string) error {
	stages, err := parseStages(cfg.Stages)
	if err != nil {
		return err
	}
	creds, err := credentialsFromEnv(stages, env, cfg.AllowSharedCredential, cfg.DryRun)
	if err != nil {
		return err
	}
	writes := backupSecretWrites(stages, creds, cfg.Endpoint, cfg.Bucket, cfg.Region)

	fmt.Printf("guardian cozystack openbao backup secrets\n")
	fmt.Printf("namespace=%s statefulset=%s service=%s bootstrapSecret=%s bucket=%s endpoint=%s region=%s stages=%s dryRun=%t forceSync=%t allowSharedCredential=%t\n",
		cfg.Namespace,
		cfg.StatefulSet,
		cfg.Service,
		cfg.BootstrapSecret,
		cfg.Bucket,
		cfg.Endpoint,
		cfg.Region,
		stageNames(stages),
		cfg.DryRun,
		cfg.ForceSync,
		cfg.AllowSharedCredential,
	)

	for _, write := range writes {
		fmt.Printf("target %s properties=%s credentialSource=%s\n", write.Path, propertyNames(write.Data), write.CredentialSource)
	}
	if cfg.DryRun {
		return nil
	}

	runner := kubectlRunner{
		bin:            cfg.Kubectl,
		kubeconfig:     cfg.Kubeconfig,
		requestTimeout: cfg.RequestTimeout,
		namespace:      cfg.Namespace,
	}
	if err := waitStatefulSetReady(ctx, runner, cfg.StatefulSet, cfg.WaitTimeout); err != nil {
		return err
	}
	token, err := rootToken(ctx, runner, cfg.BootstrapSecret)
	if err != nil {
		return err
	}
	fmt.Printf("read OpenBao bootstrap token from Kubernetes Secret without printing secret material\n")

	localPort, err := freeLocalPort()
	if err != nil {
		return err
	}
	pf, err := startPortForward(ctx, runner, cfg.Service, localPort, cfg.PortForwardReadyWait)
	if err != nil {
		return err
	}
	defer pf.stop()

	openbaoAddr := fmt.Sprintf("http://127.0.0.1:%d", localPort)
	for _, write := range writes {
		if err := writeKVV2(ctx, openbaoAddr, token, write); err != nil {
			return err
		}
		fmt.Printf("wrote OpenBao kv-v2 path %s without printing secret values\n", write.Path)
	}

	if cfg.ForceSync {
		return forceExternalSecretSync(ctx, cfg, stages)
	}
	return nil
}

func parseStages(raw string) ([]stageConfig, error) {
	known := map[string]stageConfig{
		"root":  {Name: "root", Namespace: "tenant-root"},
		"dev":   {Name: "dev", Namespace: "tenant-guardiancommercial-platform-dev"},
		"gamma": {Name: "gamma", Namespace: "tenant-guardiancommercial-platform-gamma"},
		"prod":  {Name: "prod", Namespace: "tenant-guardiancommercial-platform-prod"},
	}
	seen := map[string]bool{}
	var stages []stageConfig
	for _, item := range strings.Split(raw, ",") {
		name := strings.TrimSpace(item)
		if name == "" {
			continue
		}
		stage, ok := known[name]
		if !ok {
			return nil, fmt.Errorf("--stages contains %q; allowed stages are root, dev, gamma, prod", name)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		stages = append(stages, stage)
	}
	if len(stages) == 0 {
		return nil, errors.New("--stages must include at least one stage")
	}
	return stages, nil
}

func credentialsFromEnv(stages []stageConfig, env []string, allowShared bool, dryRun bool) (map[credentialScope]backupSecretCredential, error) {
	out := map[credentialScope]backupSecretCredential{}
	for _, stage := range stages {
		for _, component := range []string{"postgres", "clickhouse"} {
			scope := credentialScope{Stage: stage.Name, Component: component}
			if dryRun {
				out[scope] = backupSecretCredential{
					AccessKeyID: "<redacted>",
					SecretKey:   "<redacted>",
					Source:      "dry-run",
				}
				continue
			}
			creds, err := credentialFromEnv(env, stage, component, allowShared)
			if err != nil {
				return nil, err
			}
			out[scope] = creds
		}
	}
	return out, nil
}

func credentialFromEnv(env []string, stage stageConfig, component string, allowShared bool) (backupSecretCredential, error) {
	values := envMap(env)
	prefix := scopedCredentialEnvPrefix(stage.Name, component)
	accessEnv := prefix + "_AWS_ACCESS_KEY_ID"
	secretEnv := prefix + "_AWS_SECRET_ACCESS_KEY"
	accessKey := values[accessEnv]
	secretKey := values[secretEnv]
	if accessKey != "" && secretKey != "" {
		return backupSecretCredential{
			AccessKeyID: accessKey,
			SecretKey:   secretKey,
			Source:      accessEnv + "/" + secretEnv,
		}, nil
	}
	if accessKey != "" || secretKey != "" {
		return backupSecretCredential{}, fmt.Errorf("incomplete scoped backup credential for %s/%s: set both %s and %s", stage.Name, component, accessEnv, secretEnv)
	}

	if allowShared {
		sharedAccessEnv := "GUARDIAN_BACKUP_AWS_ACCESS_KEY_ID"
		sharedSecretEnv := "GUARDIAN_BACKUP_AWS_SECRET_ACCESS_KEY"
		accessKey = values[sharedAccessEnv]
		secretKey = values[sharedSecretEnv]
		if accessKey != "" && secretKey != "" {
			return backupSecretCredential{
				AccessKeyID: accessKey,
				SecretKey:   secretKey,
				Source:      sharedAccessEnv + "/" + sharedSecretEnv + " (shared bootstrap)",
			}, nil
		}
		if accessKey != "" || secretKey != "" {
			return backupSecretCredential{}, fmt.Errorf("incomplete shared backup credential: set both %s and %s", sharedAccessEnv, sharedSecretEnv)
		}
	}
	return backupSecretCredential{}, fmt.Errorf("missing scoped backup credential for %s/%s: set %s and %s", stage.Name, component, accessEnv, secretEnv)
}

func scopedCredentialEnvPrefix(stage, component string) string {
	stage = strings.ToUpper(strings.ReplaceAll(stage, "-", "_"))
	component = strings.ToUpper(strings.ReplaceAll(component, "-", "_"))
	return "GUARDIAN_BACKUP_" + stage + "_" + component
}

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		out[key] = value
	}
	return out
}

func backupSecretWrites(stages []stageConfig, creds map[credentialScope]backupSecretCredential, endpoint, bucket, region string) []secretWrite {
	var writes []secretWrite
	for _, stage := range stages {
		postgresCreds := creds[credentialScope{Stage: stage.Name, Component: "postgres"}]
		writes = append(writes, secretWrite{
			Path: "guardian/guardian-mgmt/" + stage.Namespace + "/postgres/guardian/cnpg-backup",
			Data: map[string]string{
				"AWS_ACCESS_KEY_ID":     postgresCreds.AccessKeyID,
				"AWS_SECRET_ACCESS_KEY": postgresCreds.SecretKey,
			},
			CredentialSource: postgresCreds.Source,
		})
		clickHouseCreds := creds[credentialScope{Stage: stage.Name, Component: "clickhouse"}]
		writes = append(writes, secretWrite{
			Path: "guardian/guardian-mgmt/" + stage.Namespace + "/clickhouse/guardian/backup",
			Data: map[string]string{
				"bucketName": bucket,
				"endpoint":   endpoint,
				"region":     region,
				"accessKey":  clickHouseCreds.AccessKeyID,
				"secretKey":  clickHouseCreds.SecretKey,
			},
			CredentialSource: clickHouseCreds.Source,
		})
	}
	return writes
}

func writeKVV2(ctx context.Context, openbaoAddr, token string, write secretWrite) error {
	body, err := json.Marshal(map[string]any{"data": write.Data})
	if err != nil {
		return err
	}
	requestURL := strings.TrimRight(openbaoAddr, "/") + "/v1/kv/data/" + escapeVaultPath(write.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Vault-Token", token)
	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("write OpenBao kv-v2 path %s: %w", write.Path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		out, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("write OpenBao kv-v2 path %s: status %s: %s", write.Path, resp.Status, strings.TrimSpace(string(out)))
	}
	return nil
}

func escapeVaultPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return strings.Join(parts, "/")
}

func forceExternalSecretSync(ctx context.Context, cfg backupSecretsConfig, stages []stageConfig) error {
	stamp := fmt.Sprintf("%d", time.Now().Unix())
	for _, stage := range stages {
		runner := kubectlRunner{
			bin:            cfg.Kubectl,
			kubeconfig:     cfg.Kubeconfig,
			requestTimeout: cfg.RequestTimeout,
			namespace:      stage.Namespace,
		}
		for _, name := range []string{"guardian-cnpg-backup-creds", "guardian-clickhouse-backup-creds"} {
			if err := runner.run(ctx, "force-sync "+stage.Namespace+"/"+name, "annotate", "externalsecrets.external-secrets.io/"+name, "force-sync="+stamp, "--overwrite"); err != nil {
				return err
			}
		}
		if err := runner.run(ctx, "wait "+stage.Namespace+" backup ExternalSecrets", "wait", "--for=condition=Ready", "externalsecrets.external-secrets.io/guardian-cnpg-backup-creds", "externalsecrets.external-secrets.io/guardian-clickhouse-backup-creds", "--timeout="+cfg.SyncWaitTimeout); err != nil {
			return err
		}
		for _, name := range []string{"guardian-cnpg-backup-creds", "guardian-clickhouse-backup-creds"} {
			if err := runner.run(ctx, "get "+stage.Namespace+"/"+name, "get", "secret/"+name); err != nil {
				return err
			}
		}
	}
	return nil
}

func waitStatefulSetReady(ctx context.Context, runner kubectlRunner, statefulSet, timeout string) error {
	replicas, err := runner.output(ctx, "OpenBao StatefulSet replicas", "get", "statefulset.apps/"+statefulSet, "-o", "jsonpath={.spec.replicas}")
	if err != nil {
		return err
	}
	want := strings.TrimSpace(replicas)
	if want == "" {
		return fmt.Errorf("OpenBao StatefulSet %s has empty spec.replicas", statefulSet)
	}
	return runner.run(ctx, "wait OpenBao StatefulSet ready", "wait", "--for=jsonpath={.status.readyReplicas}="+want, "statefulset.apps/"+statefulSet, "--timeout="+timeout)
}

func rootToken(ctx context.Context, runner kubectlRunner, secret string) (string, error) {
	out, err := runner.output(ctx, "OpenBao bootstrap root token", "get", "secret/"+secret, "-o", "jsonpath={.data.root-token}")
	if err != nil {
		return "", err
	}
	return decodeRootToken(strings.TrimSpace(out))
}

func decodeRootToken(encoded string) (string, error) {
	if encoded == "" {
		return "", errors.New("OpenBao bootstrap root-token is empty")
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode OpenBao bootstrap root-token: %w", err)
	}
	token := string(raw)
	if token == "" {
		return "", errors.New("decoded OpenBao bootstrap root-token is empty")
	}
	return token, nil
}

func stageNames(stages []stageConfig) string {
	var names []string
	for _, stage := range stages {
		names = append(names, stage.Name)
	}
	return strings.Join(names, ",")
}

func propertyNames(data map[string]string) string {
	names := make([]string, 0, len(data))
	for name := range data {
		names = append(names, name)
	}
	ordered := []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "bucketName", "endpoint", "region", "accessKey", "secretKey"}
	var out []string
	for _, name := range ordered {
		for i, candidate := range names {
			if candidate == name {
				out = append(out, name)
				names = append(names[:i], names[i+1:]...)
				break
			}
		}
	}
	out = append(out, names...)
	return strings.Join(out, ",")
}

func (r kubectlRunner) args(args ...string) []string {
	out := []string{}
	if r.kubeconfig != "" {
		out = append(out, "--kubeconfig", r.kubeconfig)
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

func (r kubectlRunner) run(ctx context.Context, label string, args ...string) error {
	fmt.Printf("\n## %s\n", label)
	cmd := exec.CommandContext(ctx, r.bin, r.args(args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func (r kubectlRunner) output(ctx context.Context, label string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.bin, r.args(args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w\n%s", label, err, out)
	}
	return string(out), nil
}

func freeLocalPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func startPortForward(ctx context.Context, runner kubectlRunner, service string, localPort int, readyWait time.Duration) (*portForward, error) {
	args := openBaoPortForwardArgs(runner, service, localPort)
	cmd := exec.CommandContext(ctx, runner.bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	ready := make(chan string, 1)
	done := make(chan string, 2)
	readPortForwardOutput := func(scanner *bufio.Scanner) {
		var buf bytes.Buffer
		for scanner.Scan() {
			line := scanner.Text()
			buf.WriteString(line)
			buf.WriteByte('\n')
			if strings.Contains(line, "Forwarding from") {
				select {
				case ready <- line:
				default:
				}
			}
		}
		done <- buf.String()
	}
	go readPortForwardOutput(bufio.NewScanner(stdout))
	go readPortForwardOutput(bufio.NewScanner(stderr))

	timer := time.NewTimer(readyWait)
	defer timer.Stop()
	select {
	case <-ready:
		fmt.Printf("kubectl port-forward openbao established on 127.0.0.1:%d\n", localPort)
		return &portForward{cmd: cmd}, nil
	case <-timer.C:
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("timed out waiting for OpenBao port-forward readiness: %s", drainOutput(done))
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ctx.Err()
	}
}

func openBaoPortForwardArgs(runner kubectlRunner, service string, localPort int) []string {
	return runner.args("port-forward", "--address", "127.0.0.1", "svc/"+service, fmt.Sprintf("%d:8200", localPort))
}

func (pf *portForward) stop() {
	if pf == nil || pf.cmd == nil || pf.cmd.Process == nil {
		return
	}
	_ = pf.cmd.Process.Kill()
	_ = pf.cmd.Wait()
}

func drainOutput(done chan string) string {
	var out strings.Builder
	for i := 0; i < cap(done); i++ {
		select {
		case value := <-done:
			out.WriteString(value)
		default:
		}
	}
	return out.String()
}
