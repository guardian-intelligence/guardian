package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type createRunInput struct {
	Config  createConfig
	Options createOptions
}

func runCreate(ctx context.Context, input createRunInput, deps createDeps) createResult {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.NewOperationID == nil {
		deps.NewOperationID = newOperationID
	}
	if deps.StateRoot == "" {
		root, err := createStateRoot()
		if err != nil {
			return retryableResult("state", err)
		}
		deps.StateRoot = root
	}
	if err := requireCreateDeps(deps); err != nil {
		return retryableResult("create", err)
	}

	spec := input.Config.Spec
	specDigest := input.Config.SpecDigest
	record, err := loadMatchingOperation(deps.StateRoot, spec.Cluster.Name, specDigest)
	if err != nil {
		return retryableResult("state", err)
	}
	mismatchedRecord, err := loadMismatchedOperation(deps.StateRoot, spec.Cluster.Name, specDigest)
	if err != nil {
		return retryableResult("state", err)
	}

	targetServerID := spec.Provider.ServerID
	if targetServerID == "" && record != nil && record.ServerID != "" {
		targetServerID = record.ServerID
	}
	target := providerTarget{
		ServerID: targetServerID,
		Hostname: createTargetHostname(spec),
		Project:  spec.Provider.Project,
	}
	server, err := deps.Provider.GetServer(ctx, target)
	if err != nil {
		return errorResult("provider", err)
	}
	if server == nil {
		server = &serverObservation{}
	}

	if server.Exists && record != nil && record.ServerID != "" && server.ID != "" && record.ServerID != server.ID {
		return refusedResult("local operation server does not match observed provider server", record)
	}

	host, err := deps.Observer.Observe(ctx, spec, record, server)
	if err != nil {
		return errorResult("host", err)
	}
	if host == nil {
		host = &hostObservation{}
	}

	if res, done := classifyCreate(input, record, server, host); done {
		return res
	}
	if host.KubernetesReachable && markerMatches(host.KubernetesMarker, spec.Cluster.Name, observedServerID(server, record), specDigest) && server.Exists && !server.Locked {
		return finishProviderMarkerAndLock(ctx, spec, specDigest, host.KubernetesMarker, server, deps)
	}
	if record == nil && mismatchedRecord != nil {
		return refusedResult("local operation for cluster has a different spec digest", mismatchedRecord)
	}

	if !server.Exists {
		if record != nil && record.ServerID != "" {
			return withOperation(retryableResult("provider", fmt.Errorf("recorded server %s was not observed", record.ServerID)), record)
		}
		if spec.Create == nil {
			return createResult{
				Outcome: createOutcomeNeedsConfig,
				Reason:  "target server or create policy required",
				Plan:    createPlanFor(spec, specDigest, record, false),
			}
		}
		if !input.Options.Approved {
			plan := createPlanFor(spec, specDigest, record, true)
			plan.RequiresApproval = true
			plan.ApprovalRerunHint = "rerun with --yes"
			return createResult{Outcome: createOutcomeNeedsApproval, Reason: "server allocation requires approval", Plan: plan, Operation: record}
		}
		if record == nil {
			record, err = createOperation(deps.StateRoot, spec.Cluster.Name, specDigest, deps.Now(), deps.NewOperationID)
			if err != nil {
				return retryableResult("state", err)
			}
			if err := writeCanonicalSpec(record, input.Config.Canonical); err != nil {
				return retryableResult("state", err)
			}
		}
		created, err := deps.Provider.CreateServer(ctx, serverCreatePlan{
			Project:  spec.Provider.Project,
			Hostname: spec.Create.Hostname,
			Metro:    spec.Create.Metro,
			Plan:     spec.Create.Plan,
		})
		if err != nil {
			return errorResult("provider", err)
		}
		if created == nil || !created.Exists || created.ID == "" {
			return retryableResult("provider", fmt.Errorf("create server returned no server id"))
		}
		server = created
		record.ServerID = created.ID
		record.Stage = createStageProvisionServer
		record.UpdatedAt = deps.Now()
		if err := writeOperationRecord(record); err != nil {
			return retryableResult("state", err)
		}
	}

	if server.StockOS && !input.Options.Approved && !stageComplete(recordStage(record), createStageProvisionServer) {
		plan := createPlanFor(spec, specDigest, record, true)
		plan.RequiresApproval = true
		plan.DestructiveReplacement = true
		plan.ApprovalRerunHint = "rerun with --yes"
		return createResult{Outcome: createOutcomeNeedsApproval, Reason: "stock provider OS replacement requires approval", Plan: plan, Operation: record}
	}

	if record == nil {
		if !input.Options.Approved {
			plan := createPlanFor(spec, specDigest, nil, true)
			plan.RequiresApproval = true
			plan.ApprovalRerunHint = "rerun with --yes"
			return createResult{Outcome: createOutcomeNeedsApproval, Reason: "existing target requires approval or a matching operation record", Plan: plan}
		}
		record, err = createOperation(deps.StateRoot, spec.Cluster.Name, specDigest, deps.Now(), deps.NewOperationID)
		if err != nil {
			return retryableResult("state", err)
		}
		if err := writeCanonicalSpec(record, input.Config.Canonical); err != nil {
			return retryableResult("state", err)
		}
	}
	if record.ServerID == "" {
		record.ServerID = server.ID
		if err := completeCreateStage(record, record.Stage, deps.Now); err != nil {
			return retryableResult("state", err)
		}
	}

	if !stageComplete(record.Stage, createStageApplyNodeConfig) {
		access, err := deps.Provider.GetJITAccess(ctx, record.ServerID)
		if err != nil {
			return withOperation(errorResult("provider", err), record)
		}
		if access == nil {
			return withOperation(retryableResult("provider", fmt.Errorf("jit access returned no access material")), record)
		}
		if err := completeCreateStage(record, createStageAcquireJITAccess, deps.Now); err != nil {
			return withOperation(retryableResult("state", err), record)
		}
		if res, done := runNodeStages(ctx, spec, record, *access, deps); done {
			return res
		}
	}

	if !stageComplete(record.Stage, createStageBootstrapKubernetes) {
		kubeconfig, err := deps.Kubernetes.Bootstrap(ctx, spec, *record)
		if err != nil {
			return withOperation(errorResult("kubernetes", err), record)
		}
		if kubeconfig == "" {
			kubeconfig = filepath.Join(record.StateDir, "kubeconfig")
		}
		record.Kubeconfig = kubeconfig
		if err := completeCreateStage(record, createStageBootstrapKubernetes, deps.Now); err != nil {
			return withOperation(retryableResult("state", err), record)
		}
	}

	if !stageComplete(record.Stage, createStageInstallCozystack) {
		if err := deps.Cozystack.Install(ctx, spec, *record, record.Kubeconfig); err != nil {
			return withOperation(errorResult("cozystack", err), record)
		}
		if err := completeCreateStage(record, createStageInstallCozystack, deps.Now); err != nil {
			return withOperation(retryableResult("state", err), record)
		}
	}

	return writeMarkerAndLock(ctx, spec, specDigest, record, deps)
}

func classifyCreate(input createRunInput, record *operationRecord, server *serverObservation, host *hostObservation) (createResult, bool) {
	spec := input.Config.Spec
	specDigest := input.Config.SpecDigest
	if server.Exists && server.ID != "" && markerMatches(server.Marker, spec.Cluster.Name, server.ID, specDigest) && server.Locked {
		return createResult{Outcome: createOutcomeConverged, Reason: "provider marker and lock match", Operation: record}, true
	}
	if server.Exists && server.Locked && !markerMatches(server.Marker, spec.Cluster.Name, server.ID, specDigest) {
		return refusedResult("server is locked without a matching Guardian marker", record), true
	}
	if host.KubernetesReachable {
		if markerMatches(host.KubernetesMarker, spec.Cluster.Name, observedServerID(server, record), specDigest) {
			if server.Exists && !server.Locked {
				return createResult{}, false
			}
			return createResult{Outcome: createOutcomeConverged, Reason: "cluster marker matches", Operation: record}, true
		}
		if ownedPartialKubernetesBootstrap(record, server, specDigest) {
			return createResult{}, false
		}
		return refusedResult("Kubernetes is reachable without a matching Guardian marker", record), true
	}
	if host.CozystackReachable &&
		!markerMatches(host.KubernetesMarker, spec.Cluster.Name, observedServerID(server, record), specDigest) &&
		!ownedPartialKubernetesBootstrap(record, server, specDigest) {
		return refusedResult("Cozystack is reachable without a matching Guardian marker", record), true
	}
	if host.Unreachable {
		return createResult{Outcome: createOutcomeRetryable, Retryable: true, Reason: "host is unreachable", Operation: record}, true
	}
	return createResult{}, false
}

func runNodeStages(ctx context.Context, spec createSpec, record *operationRecord, access jitAccess, deps createDeps) (createResult, bool) {
	if stageComplete(record.Stage, createStageApplyNodeConfig) {
		return createResult{}, false
	}
	render, err := deps.Node.Render(ctx, spec, *record)
	if err != nil {
		return withOperation(errorResult("talos", err), record), true
	}
	if render == nil || render.Digest == "" {
		return withOperation(retryableResult("talos", fmt.Errorf("node render returned no digest")), record), true
	}
	if err := completeCreateStage(record, createStageEnsureTalos, deps.Now); err != nil {
		return withOperation(retryableResult("state", err), record), true
	}
	evidence, err := deps.Node.Preflight(ctx, *render, access)
	if err != nil {
		return withOperation(errorResult("talos", err), record), true
	}
	if evidence == nil || evidence.RenderDigest != render.Digest {
		return refusedResult("node preflight evidence does not match render digest", record), true
	}
	if err := completeCreateStage(record, createStagePreflightNodeConfig, deps.Now); err != nil {
		return withOperation(retryableResult("state", err), record), true
	}
	if err := deps.Node.Apply(ctx, *render, access); err != nil {
		return withOperation(errorResult("talos", err), record), true
	}
	if err := completeCreateStage(record, createStageApplyNodeConfig, deps.Now); err != nil {
		return withOperation(retryableResult("state", err), record), true
	}
	return createResult{}, false
}

func finishProviderMarkerAndLock(ctx context.Context, spec createSpec, specDigest string, marker *createMarker, server *serverObservation, deps createDeps) createResult {
	if marker == nil {
		return refusedResult("cluster marker disappeared before provider lock", nil)
	}
	if err := deps.Provider.WriteMarker(ctx, server.ID, *marker); err != nil {
		return errorResult("provider", err)
	}
	if err := deps.Provider.LockServer(ctx, server.ID); err != nil {
		return errorResult("provider", err)
	}
	return createResult{
		Outcome: createOutcomeConverged,
		Reason:  "cluster marker matched; provider marker and lock completed",
		Operation: &operationRecord{
			OperationID: marker.OperationID,
			ClusterName: spec.Cluster.Name,
			ServerID:    server.ID,
			SpecDigest:  specDigest,
		},
	}
}

func writeMarkerAndLock(ctx context.Context, spec createSpec, specDigest string, record *operationRecord, deps createDeps) createResult {
	if !stageComplete(record.Stage, createStageWriteMarkerAndLock) {
		marker := createMarker{
			Guardian:         true,
			ClusterName:      spec.Cluster.Name,
			ServerID:         record.ServerID,
			SpecDigest:       specDigest,
			OperationID:      record.OperationID,
			GuardianVersion:  "dev",
			CozystackVersion: spec.Cozystack.Version,
			CreatedAt:        deps.Now(),
		}
		if err := deps.Kubernetes.WriteMarker(ctx, record.Kubeconfig, marker); err != nil {
			return withOperation(errorResult("kubernetes", err), record)
		}
		if err := deps.Provider.WriteMarker(ctx, record.ServerID, marker); err != nil {
			return withOperation(errorResult("provider", err), record)
		}
		if err := deps.Provider.LockServer(ctx, record.ServerID); err != nil {
			return withOperation(errorResult("provider", err), record)
		}
		if err := completeCreateStage(record, createStageWriteMarkerAndLock, deps.Now); err != nil {
			return retryableResult("state", err)
		}
	}
	return createResult{
		Outcome:    createOutcomeConverged,
		Reason:     "created Cozystack cluster",
		Operation:  record,
		Kubeconfig: record.Kubeconfig,
	}
}

func completeCreateStage(record *operationRecord, stage createStage, now func() time.Time) error {
	if record == nil {
		return nil
	}
	if stage != "" && !stageComplete(record.Stage, stage) {
		record.Stage = stage
	}
	record.UpdatedAt = now()
	return writeOperationRecord(record)
}

func createPlanFor(spec createSpec, specDigest string, record *operationRecord, mutating bool) *createPlan {
	plan := &createPlan{
		ClusterName: spec.Cluster.Name,
		ServerID:    spec.Provider.ServerID,
		Provider:    spec.Provider.Name,
		SpecDigest:  specDigest,
	}
	if record != nil {
		plan.OperationID = record.OperationID
		if record.ServerID != "" {
			plan.ServerID = record.ServerID
		}
	}
	if spec.Create != nil {
		plan.Create = &serverCreatePlan{
			Project:  spec.Provider.Project,
			Hostname: spec.Create.Hostname,
			Metro:    spec.Create.Metro,
			Plan:     spec.Create.Plan,
		}
	}
	if mutating {
		if spec.Provider.ServerID == "" && spec.Create != nil {
			plan.Mutations = append(plan.Mutations, "create Latitude server")
		}
		plan.Mutations = append(plan.Mutations,
			"acquire JIT access",
			"preflight node config",
			"apply Talos node config",
			"bootstrap Kubernetes",
			"install Cozystack substrate",
			"write Guardian markers",
			"lock Latitude server",
		)
	}
	return plan
}

func requireCreateDeps(deps createDeps) error {
	switch {
	case deps.Provider == nil:
		return errors.New("missing provider")
	case deps.Observer == nil:
		return errors.New("missing observer")
	case deps.Node == nil:
		return errors.New("missing node configurator")
	case deps.Kubernetes == nil:
		return errors.New("missing kubernetes bootstrapper")
	case deps.Cozystack == nil:
		return errors.New("missing cozystack installer")
	}
	return nil
}

func createTargetHostname(spec createSpec) string {
	if spec.Create != nil && spec.Create.Hostname != "" {
		return spec.Create.Hostname
	}
	return spec.Host.Hostname
}

func observedServerID(server *serverObservation, record *operationRecord) string {
	if server != nil && server.ID != "" {
		return server.ID
	}
	if record != nil {
		return record.ServerID
	}
	return ""
}

func ownedPartialKubernetesBootstrap(record *operationRecord, server *serverObservation, specDigest string) bool {
	return record != nil &&
		server != nil &&
		server.Exists &&
		record.ServerID != "" &&
		server.ID == record.ServerID &&
		record.SpecDigest == specDigest &&
		stageComplete(record.Stage, createStageBootstrapKubernetes) &&
		!stageComplete(record.Stage, createStageWriteMarkerAndLock)
}

func recordStage(record *operationRecord) createStage {
	if record == nil {
		return ""
	}
	return record.Stage
}

func errorResult(subsystem string, err error) createResult {
	var retry retryableCreateError
	if errors.As(err, &retry) {
		return createResult{
			Outcome:     createOutcomeRetryable,
			Retryable:   true,
			Reason:      retry.err.Error(),
			Diagnostics: []createDiagnostic{{Subsystem: retry.subsystem, Message: retry.err.Error()}},
		}
	}
	return refusedResult(fmt.Sprintf("%s: %v", subsystem, err), nil)
}

func retryableResult(subsystem string, err error) createResult {
	return createResult{
		Outcome:     createOutcomeRetryable,
		Retryable:   true,
		Reason:      err.Error(),
		Diagnostics: []createDiagnostic{{Subsystem: subsystem, Message: err.Error()}},
	}
}

func refusedResult(reason string, record *operationRecord) createResult {
	return createResult{Outcome: createOutcomeRefused, Reason: reason, Operation: record}
}

func withOperation(result createResult, record *operationRecord) createResult {
	result.Operation = record
	return result
}

type unavailableCreateProvider struct {
	token secretString
}

func (p unavailableCreateProvider) GetServer(context.Context, providerTarget) (*serverObservation, error) {
	return nil, retryableCreate("provider", fmt.Errorf("Latitude provider integration is not wired yet"))
}

func (p unavailableCreateProvider) CreateServer(context.Context, serverCreatePlan) (*serverObservation, error) {
	return nil, retryableCreate("provider", fmt.Errorf("Latitude provider integration is not wired yet"))
}

func (p unavailableCreateProvider) GetJITAccess(context.Context, string) (*jitAccess, error) {
	return nil, retryableCreate("provider", fmt.Errorf("Latitude JIT access integration is not wired yet"))
}

func (p unavailableCreateProvider) WriteMarker(context.Context, string, createMarker) error {
	return retryableCreate("provider", fmt.Errorf("Latitude marker integration is not wired yet"))
}

func (p unavailableCreateProvider) LockServer(context.Context, string) error {
	return retryableCreate("provider", fmt.Errorf("Latitude lock integration is not wired yet"))
}

type unavailableCreateObserver struct{}

func (unavailableCreateObserver) Observe(context.Context, createSpec, *operationRecord, *serverObservation) (*hostObservation, error) {
	return &hostObservation{}, nil
}

type unavailableNodeConfigurator struct{}

func (unavailableNodeConfigurator) Render(context.Context, createSpec, operationRecord) (*nodeRender, error) {
	return nil, retryableCreate("talos", fmt.Errorf("Talos/Talm integration is not wired yet"))
}

func (unavailableNodeConfigurator) Preflight(context.Context, nodeRender, jitAccess) (*preflightEvidence, error) {
	return nil, retryableCreate("talos", fmt.Errorf("Talos/Talm preflight integration is not wired yet"))
}

func (unavailableNodeConfigurator) Apply(context.Context, nodeRender, jitAccess) error {
	return retryableCreate("talos", fmt.Errorf("Talos/Talm apply integration is not wired yet"))
}

type unavailableKubernetesBootstrapper struct{}

func (unavailableKubernetesBootstrapper) Bootstrap(context.Context, createSpec, operationRecord) (string, error) {
	return "", retryableCreate("kubernetes", fmt.Errorf("Kubernetes bootstrap integration is not wired yet"))
}

func (unavailableKubernetesBootstrapper) WriteMarker(context.Context, string, createMarker) error {
	return retryableCreate("kubernetes", fmt.Errorf("Kubernetes marker integration is not wired yet"))
}

type unavailableCozystackInstaller struct{}

func (unavailableCozystackInstaller) Install(context.Context, createSpec, operationRecord, string) error {
	return retryableCreate("cozystack", fmt.Errorf("Cozystack install integration is not wired yet"))
}

func createDepsForCLI(token secretString) (createDeps, error) {
	root, err := createStateRoot()
	if err != nil {
		return createDeps{}, err
	}
	return createDeps{
		Provider:       unavailableCreateProvider{token: token},
		Observer:       unavailableCreateObserver{},
		Node:           unavailableNodeConfigurator{},
		Kubernetes:     unavailableKubernetesBootstrapper{},
		Cozystack:      unavailableCozystackInstaller{},
		StateRoot:      root,
		Now:            time.Now,
		NewOperationID: newOperationID,
	}, nil
}

func kubeconfigPathForOperation(record operationRecord) string {
	if record.Kubeconfig != "" {
		return record.Kubeconfig
	}
	return filepath.Join(record.StateDir, "kubeconfig")
}

func writeFakeKubeconfigForTest(record operationRecord) (string, error) {
	path := kubeconfigPathForOperation(record)
	if err := os.WriteFile(path, []byte("apiVersion: v1\nkind: Config\n"), 0o600); err != nil {
		return "", err
	}
	return path, os.Chmod(path, 0o600)
}
