package types

import "errors"

// ErrTemporalDialTimeout is returned when the Temporal runtime cannot establish a gRPC connection
// before the internal deadline (see internal/runtime/temporal newTemporalClient).
var ErrTemporalDialTimeout = errors.New("temporal dial timeout")

// ErrTemporalNamespaceCheckTimeout is returned when the Temporal namespace cannot be verified in time.
var ErrTemporalNamespaceCheckTimeout = errors.New("temporal namespace check timeout")
