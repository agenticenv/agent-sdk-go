package actionpolicy

import (
	"errors"
	"fmt"
	"strings"
)

// ErrContextMissing tells a rule set that a matcher does not have the context it needs.
// The evaluation returns [ResultDefer]. Any other matcher error returns [ResultDeny].
var ErrContextMissing = errors.New("actionpolicy: context missing")

// ErrSessionConflict is returned by [SessionStore.Save] when another run advanced
// the same conversation. The agent run fails. Start the turn again.
var ErrSessionConflict = errors.New("actionpolicy: session conflict")

// RestoreSessionConflict re-wraps [ErrSessionConflict] when the error chain was dropped
// by a runtime that only preserved the error text.
func RestoreSessionConflict(err error) error {
	if err == nil || errors.Is(err, ErrSessionConflict) {
		return err
	}
	if strings.Contains(err.Error(), ErrSessionConflict.Error()) {
		return fmt.Errorf("%w: %v", ErrSessionConflict, err)
	}
	return err
}
