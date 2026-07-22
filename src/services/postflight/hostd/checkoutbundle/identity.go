package checkoutbundle

import "context"

// AssignmentIdentity is what a resolved (execution, attempt) pair is entitled
// to. The checkout endpoint consumes the repository fields and carries the
// remainder into logs for correlation.
type AssignmentIdentity struct {
	ExecutionID        string `json:"execution_id"`
	AttemptID          string `json:"attempt_id"`
	OrgID              string `json:"org_id"`
	InstallationID     int64  `json:"installation_id"`
	RepositoryID       int64  `json:"repository_id"`
	RepositoryFullName string `json:"repository_full_name"`
	RunnerClass        string `json:"runner_class"`
	RunnerName         string `json:"runner_name"`
}

// IdentityResolver resolves an execution/attempt pair to an active assignment.
// A false return means terminal or unknown; the caller cannot distinguish.
type IdentityResolver interface {
	ResolveActiveAssignment(ctx context.Context, executionID, attemptID string) (AssignmentIdentity, bool, error)
}

// StaticResolver serves a fixed assignment set for tests.
type StaticResolver struct {
	Assignments []AssignmentIdentity
}

// ResolveActiveAssignment matches on exact execution and attempt IDs.
func (r *StaticResolver) ResolveActiveAssignment(_ context.Context, executionID, attemptID string) (AssignmentIdentity, bool, error) {
	for _, assignment := range r.Assignments {
		if assignment.ExecutionID == executionID && assignment.AttemptID == attemptID {
			return assignment, true, nil
		}
	}
	return AssignmentIdentity{}, false, nil
}
