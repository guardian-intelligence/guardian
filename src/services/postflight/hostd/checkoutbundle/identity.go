package checkoutbundle

import "context"

// LeaseIdentity is what a resolved (execution, attempt) pair is entitled to.
// The field set mirrors what the lease-claim response carries from the
// control plane; the checkout endpoint only consumes the repository fields,
// the rest travel into logs for correlation.
type LeaseIdentity struct {
	ExecutionID        string `json:"execution_id"`
	AttemptID          string `json:"attempt_id"`
	OrgID              string `json:"org_id"`
	InstallationID     int64  `json:"installation_id"`
	RepositoryID       int64  `json:"repository_id"`
	RepositoryFullName string `json:"repository_full_name"`
	RunnerClass        string `json:"runner_class"`
	RunnerName         string `json:"runner_name"`
}

// IdentityResolver resolves an execution/attempt pair to an active lease.
// A false return means "no active lease": expired, terminal, or never issued
// — the caller cannot distinguish, deliberately. hostd backs this with its
// live lease table.
type IdentityResolver interface {
	ResolveActiveLease(ctx context.Context, executionID, attemptID string) (LeaseIdentity, bool, error)
}

// StaticResolver serves a fixed lease set for tests.
type StaticResolver struct {
	Leases []LeaseIdentity
}

// ResolveActiveLease matches on exact execution and attempt IDs.
func (r *StaticResolver) ResolveActiveLease(_ context.Context, executionID, attemptID string) (LeaseIdentity, bool, error) {
	for _, lease := range r.Leases {
		if lease.ExecutionID == executionID && lease.AttemptID == attemptID {
			return lease, true, nil
		}
	}
	return LeaseIdentity{}, false, nil
}
