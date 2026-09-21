package base

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

// CircuitBreakerSkippedMessage is the synthetic tool-role content when a tripped breaker skips a tool.
func CircuitBreakerSkippedMessage(name string) string {
	return fmt.Sprintf("Circuit breaker skipped tool %q after repeated failures.", name)
}

// HashToolArgs is a stable SHA-256 of args (encoding/json sorts map keys).
func HashToolArgs(args map[string]any) string {
	if args == nil {
		args = map[string]any{}
	}
	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// CountsTowardCircuitBreaker reports whether a tool call participates in the per-tool tracker.
func CountsTowardCircuitBreaker(unknown bool, kind types.ToolKind) bool {
	return !unknown && kind.CountsTowardToolTelemetry()
}

// CircuitBreakerState is the persisted per-run tracker (Temporal continue-as-new, Restate/durable journal).
type CircuitBreakerState struct {
	Tools map[string]CircuitBreakerToolState `json:"tools,omitempty"`
}

// CircuitBreakerToolState is one tool's consecutive / pattern / trip fields.
type CircuitBreakerToolState struct {
	LastHash          string   `json:"last_hash,omitempty"`
	Consecutive       int      `json:"consecutive,omitempty"`
	Window            []string `json:"window,omitempty"`
	Tripped           bool     `json:"tripped,omitempty"`
	TrippedAtUnixNano int64    `json:"tripped_at_unix_nano,omitempty"`
}

// CircuitBreaker is the per-run same-args / A-B-A-B tracker. Nil receiver is a no-op.
type CircuitBreaker struct {
	cfg   types.CircuitBreakerConfig
	state CircuitBreakerState
}

// NewCircuitBreaker returns a tracker when a detector is enabled (MaxConsecutiveSameArgs > 0
// or PatternWindowSize >= 4). PatternWindowSize must also be even to trip A-B-A-B; odd never trips.
// Nil config or both detectors off returns nil.
func NewCircuitBreaker(cfg *types.ErrorControlConfig) *CircuitBreaker {
	if cfg == nil || cfg.CircuitBreaker == nil {
		return nil
	}
	cb := cfg.CircuitBreaker
	if cb.MaxConsecutiveSameArgs <= 0 && cb.PatternWindowSize < 4 {
		return nil
	}
	return &CircuitBreaker{
		cfg:   *cb,
		state: CircuitBreakerState{Tools: map[string]CircuitBreakerToolState{}},
	}
}

// NewCircuitBreakerFromState restores a tracker after continue-as-new or durable replay.
func NewCircuitBreakerFromState(cfg *types.ErrorControlConfig, state CircuitBreakerState) *CircuitBreaker {
	cb := NewCircuitBreaker(cfg)
	if cb == nil {
		return nil
	}
	cb.Restore(state)
	return cb
}

func (c *CircuitBreaker) enabled() bool {
	return c != nil && (c.cfg.MaxConsecutiveSameArgs > 0 || c.cfg.PatternWindowSize >= 4)
}

// State returns a copy safe to persist.
func (c *CircuitBreaker) State() CircuitBreakerState {
	if c == nil {
		return CircuitBreakerState{}
	}
	out := CircuitBreakerState{Tools: make(map[string]CircuitBreakerToolState, len(c.state.Tools))}
	for k, v := range c.state.Tools {
		v.Window = append([]string(nil), v.Window...)
		out.Tools[k] = v
	}
	return out
}

// Restore replaces in-memory state after replay or continue-as-new.
func (c *CircuitBreaker) Restore(state CircuitBreakerState) {
	if c == nil {
		return
	}
	if state.Tools == nil {
		c.state.Tools = map[string]CircuitBreakerToolState{}
		return
	}
	c.state = CircuitBreakerState{Tools: make(map[string]CircuitBreakerToolState, len(state.Tools))}
	for k, v := range state.Tools {
		v.Window = append([]string(nil), v.Window...)
		c.state.Tools[k] = v
	}
}

// IsTripped reports whether name is currently skipped. ResetAfter may clear the trip.
func (c *CircuitBreaker) IsTripped(name string, now time.Time) bool {
	if !c.enabled() {
		return false
	}
	st, ok := c.state.Tools[name]
	if !ok || !st.Tripped {
		return false
	}
	if c.cfg.ResetAfter > 0 && !now.IsZero() && st.TrippedAtUnixNano > 0 {
		trippedAt := time.Unix(0, st.TrippedAtUnixNano)
		if now.Sub(trippedAt) >= c.cfg.ResetAfter {
			c.clear(name)
			return false
		}
	}
	return true
}

// RecordFailure updates consecutive same-args and the sliding window. Returns true if this call trips the tool.
func (c *CircuitBreaker) RecordFailure(name string, args map[string]any, now time.Time) bool {
	if !c.enabled() {
		return false
	}
	st := c.state.Tools[name]
	if st.Tripped {
		c.state.Tools[name] = st
		return true
	}
	h := HashToolArgs(args)
	if h != "" && h == st.LastHash {
		st.Consecutive++
	} else {
		st.LastHash = h
		st.Consecutive = 1
	}
	if h != "" && c.cfg.PatternWindowSize >= 4 {
		st.Window = append(st.Window, h)
		if len(st.Window) > c.cfg.PatternWindowSize {
			st.Window = append([]string(nil), st.Window[len(st.Window)-c.cfg.PatternWindowSize:]...)
		}
	}
	if c.shouldTrip(st) {
		st.Tripped = true
		st.TrippedAtUnixNano = now.UnixNano()
	}
	c.state.Tools[name] = st
	return st.Tripped
}

// RecordSuccess clears failure streaks. A trip is not cleared (only ResetAfter / end of run).
func (c *CircuitBreaker) RecordSuccess(name string) {
	if !c.enabled() {
		return
	}
	st := c.state.Tools[name]
	if st.Tripped {
		return
	}
	st.LastHash = ""
	st.Consecutive = 0
	st.Window = nil
	c.state.Tools[name] = st
}

func (c *CircuitBreaker) shouldTrip(st CircuitBreakerToolState) bool {
	if c.cfg.MaxConsecutiveSameArgs > 0 && st.Consecutive >= c.cfg.MaxConsecutiveSameArgs {
		return true
	}
	if c.cfg.PatternWindowSize >= 4 && len(st.Window) >= c.cfg.PatternWindowSize && isAlternating(st.Window) {
		return true
	}
	return false
}

func (c *CircuitBreaker) clear(name string) {
	delete(c.state.Tools, name)
}

func isAlternating(window []string) bool {
	if len(window) < 4 || len(window)%2 != 0 {
		return false
	}
	a, b := window[0], window[1]
	if a == "" || b == "" || a == b {
		return false
	}
	for i, h := range window {
		want := a
		if i%2 == 1 {
			want = b
		}
		if h != want {
			return false
		}
	}
	return true
}

// ApplyToolCircuitResult records success or failure for one telemetry-eligible tool.
func ApplyToolCircuitResult(cb *CircuitBreaker, unknown bool, kind types.ToolKind, name string, args map[string]any, failed bool, now time.Time) {
	if cb == nil || !CountsTowardCircuitBreaker(unknown, kind) {
		return
	}
	if failed {
		cb.RecordFailure(name, args, now)
		return
	}
	cb.RecordSuccess(name)
}

// NoteCircuitSkip logs a warning and increments [types.MetricToolCallCircuitOpen].
func (rt *Runtime) NoteCircuitSkip(ctx context.Context, log logger.Logger, toolName string) {
	if log != nil {
		log.Warn(ctx, "runtime: circuit breaker skipped tool",
			slog.String("scope", "runtime"), slog.String("tool", toolName))
	}
	if rt != nil && rt.Metrics != nil {
		rt.Metrics.IncrementCounter(ctx, types.MetricToolCallCircuitOpen,
			interfaces.Attribute{Key: types.MetricAttrTool, Value: toolName})
	}
}
