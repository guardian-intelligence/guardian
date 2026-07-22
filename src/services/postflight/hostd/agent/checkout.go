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
		if record.state.Terminal() {
			continue
		}
		spec := record.spec
		if spec.ExecutionID != executionID || spec.AttemptID != attemptID {
			continue
		}
		return checkoutbundle.AssignmentIdentity{
			ExecutionID:        spec.ExecutionID,
			AttemptID:          spec.AttemptID,
			OrgID:              spec.OrgID,
			InstallationID:     spec.InstallationID,
			RepositoryID:       spec.RepositoryID,
			RepositoryFullName: spec.RepositoryFullName,
			RunnerClass:        spec.RunnerClass,
			RunnerName:         spec.Identity.RunnerName,
		}, true, nil
	}
	return checkoutbundle.AssignmentIdentity{}, false, nil
}
