package guestd

import "errors"

// ErrLinuxRuntime reports an attempted use of guestd's privileged production
// substrate outside Linux. Portable policy and orchestration tests use fakes;
// the real runtime never degrades to a host-specific approximation.
var ErrLinuxRuntime = errors.New("guestd: privileged runtime requires Linux")
