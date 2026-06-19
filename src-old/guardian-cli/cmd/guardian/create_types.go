package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type createOutcome string

const (
	createOutcomeNeedsConfig   createOutcome = "NeedsConfig"
	createOutcomeNeedsApproval createOutcome = "NeedsApproval"
	createOutcomeRefused       createOutcome = "Refused"
	createOutcomeRetryable     createOutcome = "Retryable"
	createOutcomeConverged     createOutcome = "Converged"
	createOutcomeCreating      createOutcome = "Creating"
)

type createStage string

const (
	createStageProvisionServer     createStage = "ProvisionServer"
	createStageAcquireJITAccess    createStage = "AcquireJITAccess"
	createStageEnsureTalos         createStage = "EnsureTalos"
	createStagePreflightNodeConfig createStage = "PreflightNodeConfig"
	createStageApplyNodeConfig     createStage = "ApplyNodeConfig"
	createStageBootstrapKubernetes createStage = "BootstrapKubernetes"
	createStageInstallCozystack    createStage = "InstallCozystack"
	createStageWriteMarkerAndLock  createStage = "WriteMarkerAndLock"
)

var createStageOrder = []createStage{
	createStageProvisionServer,
	createStageAcquireJITAccess,
	createStageEnsureTalos,
	createStagePreflightNodeConfig,
	createStageApplyNodeConfig,
	createStageBootstrapKubernetes,
	createStageInstallCozystack,
	createStageWriteMarkerAndLock,
}

type createOptions struct {
	Approved bool
	Output   string
}

type createResult struct {
	Outcome     createOutcome      `json:"outcome"`
	Stage       createStage        `json:"stage,omitempty"`
	Reason      string             `json:"reason,omitempty"`
	Retryable   bool               `json:"retryable,omitempty"`
	Plan        *createPlan        `json:"plan,omitempty"`
	Operation   *operationRecord   `json:"operation,omitempty"`
	Diagnostics []createDiagnostic `json:"diagnostics,omitempty"`
	Kubeconfig  string             `json:"kubeconfig,omitempty"`
}

type createDiagnostic struct {
	Subsystem string `json:"subsystem"`
	Message   string `json:"message"`
}

type createPlan struct {
	OperationID            string            `json:"operationId,omitempty"`
	ClusterName            string            `json:"clusterName"`
	ServerID               string            `json:"serverId,omitempty"`
	Provider               string            `json:"provider"`
	SpecDigest             string            `json:"specDigest"`
	Create                 *serverCreatePlan `json:"create,omitempty"`
	Mutations              []string          `json:"mutations"`
	RequiresApproval       bool              `json:"requiresApproval,omitempty"`
	ApprovalRerunHint      string            `json:"approvalRerunHint,omitempty"`
	DestructiveReplacement bool              `json:"destructiveReplacement,omitempty"`
}

type createSpec struct {
	Provider  createProviderSpec  `json:"provider"`
	Create    *serverCreateSpec   `json:"create,omitempty"`
	Cluster   createClusterSpec   `json:"cluster"`
	Host      createHostSpec      `json:"host"`
	Talos     createTalosSpec     `json:"talos"`
	Cozystack createCozystackSpec `json:"cozystack"`
}

type createProviderSpec struct {
	Name     string `json:"name"`
	ServerID string `json:"serverId,omitempty"`
	Project  string `json:"project,omitempty"`
}

type serverCreateSpec struct {
	Hostname string `json:"hostname"`
	Metro    string `json:"metro"`
	Plan     string `json:"plan"`
}

type createClusterSpec struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
}

type createHostSpec struct {
	Address           string `json:"address"`
	Hostname          string `json:"hostname"`
	InterfaceMAC      string `json:"interfaceMac"`
	InstallDiskSerial string `json:"installDiskSerial"`
}

type createTalosSpec struct {
	Version string `json:"version"`
}

type createCozystackSpec struct {
	Version string `json:"version"`
}

type createMarker struct {
	Guardian         bool      `json:"guardian"`
	ClusterName      string    `json:"clusterName"`
	ServerID         string    `json:"serverId"`
	SpecDigest       string    `json:"specDigest"`
	OperationID      string    `json:"operationId"`
	GuardianVersion  string    `json:"guardianVersion"`
	CozystackVersion string    `json:"cozystackVersion"`
	CreatedAt        time.Time `json:"createdAt"`
}

type operationRecord struct {
	OperationID string      `json:"operationId"`
	ClusterName string      `json:"clusterName"`
	ServerID    string      `json:"serverId,omitempty"`
	SpecDigest  string      `json:"specDigest"`
	Stage       createStage `json:"stage,omitempty"`
	StateDir    string      `json:"stateDir"`
	Kubeconfig  string      `json:"kubeconfig,omitempty"`
	CreatedAt   time.Time   `json:"createdAt"`
	UpdatedAt   time.Time   `json:"updatedAt"`
}

type serverObservation struct {
	Exists  bool
	ID      string
	Locked  bool
	Marker  *createMarker
	StockOS bool
}

type hostObservation struct {
	Unreachable            bool
	TalosMaintenance       bool
	TalosConfigured        bool
	KubernetesReachable    bool
	KubernetesBootstrapped bool
	KubernetesMarker       *createMarker
	CozystackReachable     bool
}

type createProvider interface {
	GetServer(context.Context, providerTarget) (*serverObservation, error)
	CreateServer(context.Context, serverCreatePlan) (*serverObservation, error)
	GetJITAccess(context.Context, string) (*jitAccess, error)
	WriteMarker(context.Context, string, createMarker) error
	LockServer(context.Context, string) error
}

type providerTarget struct {
	ServerID string
	Hostname string
	Project  string
}

type serverCreatePlan struct {
	Project  string `json:"project"`
	Hostname string `json:"hostname"`
	Metro    string `json:"metro"`
	Plan     string `json:"plan"`
}

type jitAccess struct {
	Endpoint string
	Token    secretString
}

type targetObserver interface {
	Observe(context.Context, createSpec, *operationRecord, *serverObservation) (*hostObservation, error)
}

type nodeConfigurator interface {
	Render(context.Context, createSpec, operationRecord) (*nodeRender, error)
	Preflight(context.Context, nodeRender, jitAccess) (*preflightEvidence, error)
	Apply(context.Context, nodeRender, jitAccess) error
}

type nodeRender struct {
	Digest string
	Path   string
}

type preflightEvidence struct {
	RenderDigest string
	Path         string
}

type kubernetesBootstrapper interface {
	Bootstrap(context.Context, createSpec, operationRecord) (string, error)
	WriteMarker(context.Context, string, createMarker) error
}

type cozystackInstaller interface {
	Install(context.Context, createSpec, operationRecord, string) error
}

type createDeps struct {
	Provider       createProvider
	Observer       targetObserver
	Node           nodeConfigurator
	Kubernetes     kubernetesBootstrapper
	Cozystack      cozystackInstaller
	StateRoot      string
	Now            func() time.Time
	NewOperationID func() (string, error)
}

type secretString struct {
	value string
}

func newSecretString(value string) secretString {
	return secretString{value: value}
}

func (s secretString) reveal() string {
	return s.value
}

func (s secretString) String() string {
	if s.value == "" {
		return ""
	}
	return "<redacted>"
}

func (s secretString) GoString() string {
	return s.String()
}

func (s secretString) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

type retryableCreateError struct {
	subsystem string
	err       error
}

func (e retryableCreateError) Error() string {
	return fmt.Sprintf("%s: %v", e.subsystem, e.err)
}

func retryableCreate(subsystem string, err error) error {
	if err == nil {
		return nil
	}
	return retryableCreateError{subsystem: subsystem, err: err}
}

func markerMatches(marker *createMarker, clusterName, serverID, specDigest string) bool {
	return marker != nil &&
		marker.Guardian &&
		marker.ClusterName == clusterName &&
		marker.ServerID == serverID &&
		marker.SpecDigest == specDigest
}

func stageComplete(current, want createStage) bool {
	if current == "" {
		return false
	}
	cur, okCur := stageIndex(current)
	wantIdx, okWant := stageIndex(want)
	return okCur && okWant && cur >= wantIdx
}

func stageIndex(stage createStage) (int, bool) {
	for i, s := range createStageOrder {
		if s == stage {
			return i, true
		}
	}
	return 0, false
}
