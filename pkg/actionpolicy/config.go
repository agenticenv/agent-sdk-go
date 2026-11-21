package actionpolicy

import (
	"errors"
	"fmt"
	"time"
)

const (
	// DefaultEvaluateTimeout is the time allowed for one Policy.Evaluate call.
	DefaultEvaluateTimeout = 10 * time.Second
	// DefaultMaxDeferrals is the number of deferrals one run may pause on.
	DefaultMaxDeferrals = 8
	// DefaultContextWindow is the number of recent actions a policy sees.
	DefaultContextWindow = 50
)

// Config is the action policy for one agent. Pass it to agent.WithActionPolicy.
// Policy, AgentID, Signer, and Sink are required.
type Config struct {
	// Policy decides each tool call. ID, Version, and Hash must be non-empty.
	Policy Policy
	// AgentID is the stable identity recorded on receipts.
	AgentID string
	// Signer signs every decision and outcome. The private key stays in this process.
	Signer Signer
	// Sink stores signed receipts. A write error fails the run.
	Sink ReceiptSink
	// Classifier labels calls and results. Nil adds no labels.
	Classifier Classifier
	// SessionStore continues the ledger across runs of one conversation.
	// It is used only when the call passes a conversation id.
	SessionStore SessionStore
	// IdentityVerifier checks the caller before the run starts.
	// The requester it returns is the one recorded on receipts.
	IdentityVerifier IdentityVerifier
	// RequireVerifiedIdentity fails the run before it starts when the caller is
	// missing or [Requester.Verified] is false.
	RequireVerifiedIdentity bool
	// RedactArgs replaces arguments stored in receipts. The argument digest still
	// covers the arguments the decision applied to. Nil stores the arguments unchanged.
	RedactArgs func(tool string, args map[string]any) map[string]any
	// DeferTimeout is how long a deferred call waits. Zero uses the agent approval timeout.
	DeferTimeout time.Duration
	// MaxDeferrals is the per-run cap. A further DEFER is recorded as DENY.
	// Zero means [DefaultMaxDeferrals]. The session total is tracked separately.
	MaxDeferrals int
	// ContextWindow is how many recent actions are shown to the policy.
	// Zero means [DefaultContextWindow].
	ContextWindow int
	// EvaluateTimeout is the deadline for one policy evaluation.
	// Zero means [DefaultEvaluateTimeout]. When the deadline fires and the run is
	// still active, the call is denied and the decision is still signed.
	EvaluateTimeout time.Duration
}

// Resolved returns a copy with zero limits replaced by their defaults.
func (c Config) Resolved() Config {
	if c.EvaluateTimeout <= 0 {
		c.EvaluateTimeout = DefaultEvaluateTimeout
	}
	if c.MaxDeferrals <= 0 {
		c.MaxDeferrals = DefaultMaxDeferrals
	}
	if c.ContextWindow <= 0 {
		c.ContextWindow = DefaultContextWindow
	}
	return c
}

// Validate reports a missing required field. Limits are not required; zero uses the default.
func (c Config) Validate() error {
	if c.Policy == nil {
		return errors.New("actionpolicy: policy is required")
	}
	if c.Policy.ID() == "" || c.Policy.Version() == "" || c.Policy.Hash() == "" {
		return errors.New("actionpolicy: policy id, version, and hash are required")
	}
	if c.AgentID == "" {
		return errors.New("actionpolicy: agent id is required")
	}
	if c.Signer == nil || c.Signer.KeyID() == "" || c.Signer.Algorithm() == "" {
		return errors.New("actionpolicy: signer and key id are required")
	}
	if c.Sink == nil {
		return errors.New("actionpolicy: receipt sink is required")
	}
	if c.Classifier != nil && c.Classifier.ID() == "" {
		return errors.New("actionpolicy: classifier id is required")
	}
	if c.EvaluateTimeout < 0 || c.DeferTimeout < 0 {
		return errors.New("actionpolicy: timeouts must not be negative")
	}
	if c.MaxDeferrals < 0 || c.ContextWindow < 0 {
		return fmt.Errorf("actionpolicy: limits must not be negative")
	}
	return nil
}
