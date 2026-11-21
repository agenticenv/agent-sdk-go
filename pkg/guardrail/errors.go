package guardrail

import (
	"errors"
	"fmt"

	"github.com/agenticenv/agent-sdk-go/internal/types"
)

// ErrBlocked is returned when a guardrail denies text.
// It is the same sentinel as [types.ErrGuardrailBlocked] and agent.ErrGuardrailBlocked.
// On Temporal and Restate, errors.Is against this sentinel still works.
// errors.As to [*Error] works on the local runtime only.
var ErrBlocked = types.ErrGuardrailBlocked

// errNilEvaluate is returned by [New] when Evaluate is called with a nil function.
// The runtime wraps it as an ordinary step error, not [ErrBlocked].
var errNilEvaluate = errors.New("guardrail: nil evaluate function")

// Error is a guardrail deny. Unwrap returns [ErrBlocked].
type Error struct {
	Guardrail string
	Stage     Stage
	Reason    string
}

func (e *Error) Error() string {
	if e == nil {
		return ErrBlocked.Error()
	}
	return fmt.Sprintf("agent: guardrail blocked: %q (%s): %s", e.Guardrail, e.Stage, e.Reason)
}

func (e *Error) Unwrap() error { return ErrBlocked }
