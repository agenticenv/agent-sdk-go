package actionpolicy

import "context"

// Requester is the caller that started the run. The agent copies it onto every
// decision and carries it into delegated runs.
type Requester struct {
	Principal string   `json:"principal,omitempty"`
	Service   string   `json:"service,omitempty"`
	Roles     []string `json:"roles,omitempty"`
	Scopes    []string `json:"scopes,omitempty"`
	Verified  bool     `json:"verified"`
}

type requesterContextKey struct{}

// WithRequester attaches the caller to ctx. Agent.Run and Agent.Stream read it
// once, before the run starts.
func WithRequester(ctx context.Context, r Requester) context.Context {
	return context.WithValue(ctx, requesterContextKey{}, r)
}

// RequesterFromContext returns the caller stored by [WithRequester].
func RequesterFromContext(ctx context.Context) (Requester, bool) {
	if ctx == nil {
		return Requester{}, false
	}
	r, ok := ctx.Value(requesterContextKey{}).(Requester)
	return r, ok
}

// IdentityVerifier checks the caller before a run starts. The requester it returns,
// including roles and [Requester.Verified], is the one the run uses.
// Delegated runs do not call the verifier again.
type IdentityVerifier interface {
	Verify(ctx context.Context, r Requester) (Requester, error)
}
