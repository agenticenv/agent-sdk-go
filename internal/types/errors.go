package types

import "errors"

// ErrTemporalDialTimeout is returned when the Temporal server connection cannot be established
// within the configured dial timeout.
var ErrTemporalDialTimeout = errors.New("temporal: dial timeout — could not connect to Temporal server")

// ErrTemporalNamespaceCheckTimeout is returned when the namespace existence check does not
// complete within the configured timeout after connecting to the Temporal server.
var ErrTemporalNamespaceCheckTimeout = errors.New("temporal: namespace check timeout — namespace may not exist or server is overloaded")

// ErrApprovalAlreadyResolved is returned by [StreamHandle.Approve] (and the deprecated
// Runtime.OnApproval) when the approval token refers to an activity task that has already been
// completed (approved or rejected). This happens when a reconnecting subscriber replays a CUSTOM
// approval event that was resolved while the subscriber was disconnected. Callers should treat
// this as informational: the run is already advancing.
var ErrApprovalAlreadyResolved = errors.New("runtime: approval already resolved")

// ErrRunNotFound is returned when a runID is not recognised by the runtime
// (e.g. the workflow was never started or has already been purged from history),
// or when the runtime cannot recover a handle for that runID (e.g. LocalRuntime
// [Runtime.GetRunHandle] — no durable run tracking across process restarts).
var ErrRunNotFound = errors.New("runtime: run not found (unknown runID)")

// ErrStreamNotFound is returned when a stream runID is not recognised by the runtime,
// or when the runtime cannot recover a [StreamHandle] for that runID (e.g. LocalRuntime
// [Runtime.GetStreamHandle] — no durable stream tracking across process restarts).
var ErrStreamNotFound = errors.New("runtime: stream not found (unknown runID)")

// ErrStreamOffsetNotSupported is returned when the runtime cannot replay at a non-zero
// fromOffset (e.g. LocalRuntime — no durable event log today).
var ErrStreamOffsetNotSupported = errors.New("runtime: stream offset not supported by this runtime")

// ErrStreamAlreadyConsumed is returned by [StreamHandle.Events] when this handle has already
// handed out its in-process event channel (LocalRuntime: one Events call per stream handle).
var ErrStreamAlreadyConsumed = errors.New("runtime: stream events already consumed on this handle")

// ErrRunAlreadyCompleted is returned when the target run has already finished (completed, failed,
// timed out, or cancelled). Callers should read conversation history from the memory/conversation
// store instead of reconnecting. Also returned by [RunHandle.Cancel] / [StreamHandle.Cancel]
// when the run is already terminal (nothing left to cancel).
var ErrRunAlreadyCompleted = errors.New("runtime: run already completed, stream unavailable")
