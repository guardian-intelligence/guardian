package agent

import (
	"context"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/checkoutbundle"
)

// ResolveActiveLease implements checkoutbundle.IdentityResolver over the
// live lease table: a checkout token is valid exactly while its lease is
// active on this host. Terminal leases stop resolving immediately — token
// validity ≡ lease liveness, with no separate revocation machinery.
func (a *Agent) ResolveActiveLease(_ context.Context, executionID, attemptID string) (checkoutbundle.LeaseIdentity, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, record := range a.leases {
		if record.state.Terminal() {
			continue
		}
		spec := record.spec
		if spec.ExecutionID != executionID || spec.AttemptID != attemptID {
			continue
		}
		return checkoutbundle.LeaseIdentity{
			ExecutionID:        spec.ExecutionID,
			AttemptID:          spec.AttemptID,
			OrgID:              spec.OrgID,
			InstallationID:     spec.InstallationID,
			RepositoryID:       spec.RepositoryID,
			RepositoryFullName: spec.RepositoryFullName,
			RunnerClass:        spec.RunnerClass,
			RunnerName:         spec.LeaseID,
		}, true, nil
	}
	return checkoutbundle.LeaseIdentity{}, false, nil
}
