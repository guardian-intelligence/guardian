package generation

import (
	"errors"
	"fmt"
)

// RestoreFailureClass determines whether a process snapshot may degrade to a
// cold capsule. The classification is part of the generation contract, not a
// string match over CRIU diagnostics.
type RestoreFailureClass string

const (
	// RestoreIncompatible means the authenticated process image cannot be
	// recreated on this otherwise healthy guest. The process component is
	// invalidated and the job may continue cold after cleanup is proven.
	RestoreIncompatible RestoreFailureClass = "incompatible"
	// RestoreIntegrity means the artifact, scope, rollback floor, attestation,
	// or key binding did not authenticate. The guest must be recycled.
	RestoreIntegrity RestoreFailureClass = "integrity"
	// RestoreCleanup means a partial restore could not be proven absent. The
	// guest must be recycled even when the original error was incompatible.
	RestoreCleanup RestoreFailureClass = "cleanup"
)

// RestoreFailure carries a stable machine-readable code while preserving a
// redacted diagnostic cause for operators.
type RestoreFailure struct {
	Class RestoreFailureClass
	Code  string
	Err   error
}

func (e *RestoreFailure) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return fmt.Sprintf("generation: restore %s (%s)", e.Class, e.Code)
	}
	return fmt.Sprintf("generation: restore %s (%s): %v", e.Class, e.Code, e.Err)
}

func (e *RestoreFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewRestoreFailure(class RestoreFailureClass, code string, err error) error {
	if class != RestoreIncompatible && class != RestoreIntegrity && class != RestoreCleanup {
		class = RestoreCleanup
	}
	if code == "" {
		code = "unclassified"
	}
	return &RestoreFailure{Class: class, Code: code, Err: err}
}

func RestoreFailureDetails(err error) (RestoreFailureClass, string) {
	var failure *RestoreFailure
	if errors.As(err, &failure) {
		return failure.Class, failure.Code
	}
	return RestoreCleanup, "unclassified"
}

func IsColdFallback(err error) bool {
	class, _ := RestoreFailureDetails(err)
	return class == RestoreIncompatible
}
