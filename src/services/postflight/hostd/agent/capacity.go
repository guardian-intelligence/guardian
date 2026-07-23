package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/guardian-intelligence/guardian/src/services/postflight/hostd/zvol"
)

type storageAdmission struct {
	Admitted       bool
	AvailableBytes int64
	RequiredBytes  int64
	Reason         string
}

func (a *Agent) storageAdmission(ctx context.Context) storageAdmission {
	minimum := a.cfg.StorageMinimumAvailableBytes
	if minimum == 0 {
		return storageAdmission{Admitted: true}
	}
	started := time.Now()
	source, ok := a.zvols.(zvol.CapacitySource)
	if !ok {
		result := storageAdmission{RequiredBytes: minimum, Reason: "durable-volume driver does not report capacity"}
		a.logStorageAdmission(result, time.Since(started))
		return result
	}
	capacity, err := source.Capacity(ctx)
	if err != nil {
		result := storageAdmission{RequiredBytes: minimum, Reason: "query durable-volume capacity: " + err.Error()}
		a.logStorageAdmission(result, time.Since(started))
		return result
	}
	result := storageAdmission{
		Admitted: capacity.AvailableBytes >= minimum, AvailableBytes: capacity.AvailableBytes,
		RequiredBytes: minimum,
	}
	if !result.Admitted {
		result.Reason = fmt.Sprintf("durable-volume capacity below safety floor: available=%d required=%d", result.AvailableBytes, result.RequiredBytes)
	}
	a.logStorageAdmission(result, time.Since(started))
	return result
}

func (a *Agent) logStorageAdmission(result storageAdmission, elapsed time.Duration) {
	a.logger.Info("postflight.hostd.storage.admission",
		"admitted", result.Admitted, "available_bytes", result.AvailableBytes,
		"required_bytes", result.RequiredBytes, "duration_ns", elapsed.Nanoseconds(), "reason", result.Reason)
}
