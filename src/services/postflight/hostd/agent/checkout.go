package agent

import (
	"context"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/checkoutbundle"
)

// ResolveActiveAssignment implements checkoutbundle.IdentityResolver over
// immutable assignments. Token validity is exactly assignment liveness.
func (a *Agent) ResolveActiveAssignment(_ context.Context, executionID, attemptID string) (checkoutbundle.AssignmentIdentity, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, record := range a.assignments {
		record.mu.Lock()
		if record.state.Terminal() {
			record.mu.Unlock()
			continue
		}
		spec := record.spec
		if spec.ExecutionID != executionID || spec.AttemptID != attemptID {
			record.mu.Unlock()
			continue
		}
		identity := checkoutbundle.AssignmentIdentity{
			ExecutionID:        spec.ExecutionID,
			AttemptID:          spec.AttemptID,
			OrgID:              spec.OrgID,
			InstallationID:     spec.InstallationID,
			RepositoryID:       spec.RepositoryID,
			RepositoryFullName: spec.RepositoryFullName,
			RunnerClass:        spec.RunnerClass,
			RunnerName:         spec.Identity.RunnerName,
		}
		record.mu.Unlock()
		return identity, true, nil
	}
	return checkoutbundle.AssignmentIdentity{}, false, nil
}
