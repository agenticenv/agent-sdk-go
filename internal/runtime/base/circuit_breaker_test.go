package base

import (
	"context"
	"testing"
	"time"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	ifmocks "github.com/agenticenv/agent-sdk-go/pkg/interfaces/mocks"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

func testBreaker(t *testing.T, consecutive, window int, reset time.Duration) *CircuitBreaker {
	t.Helper()
	return NewCircuitBreaker(&types.ErrorControlConfig{
		CircuitBreaker: &types.CircuitBreakerConfig{
			MaxConsecutiveSameArgs: consecutive,
			PatternWindowSize:      window,
			ResetAfter:             reset,
		},
	})
}

func TestHashToolArgs_KeyOrderStable(t *testing.T) {
	a := HashToolArgs(map[string]any{"b": 2, "a": 1})
	b := HashToolArgs(map[string]any{"a": 1, "b": 2})
	require.Equal(t, a, b)
	require.NotEmpty(t, a)
	require.NotEqual(t, a, HashToolArgs(map[string]any{"a": 1}))
}

func TestCircuitBreaker_NilAndDisabled(t *testing.T) {
	require.Nil(t, NewCircuitBreaker(nil))
	require.Nil(t, NewCircuitBreaker(&types.ErrorControlConfig{}))
	require.Nil(t, testBreaker(t, 0, 0, 0))
	require.False(t, (*CircuitBreaker)(nil).IsTripped("x", time.Now()))
	require.False(t, (*CircuitBreaker)(nil).RecordFailure("x", map[string]any{"q": 1}, time.Now()))
}

func TestCircuitBreaker_ConsecutiveSameArgs(t *testing.T) {
	cb := testBreaker(t, 3, 0, 0)
	now := time.Unix(100, 0)
	args := map[string]any{"n": 1}
	require.False(t, cb.RecordFailure("echo", args, now))
	require.False(t, cb.RecordFailure("echo", args, now))
	require.False(t, cb.IsTripped("echo", now))
	require.True(t, cb.RecordFailure("echo", args, now))
	require.True(t, cb.IsTripped("echo", now))
}

func TestCircuitBreaker_DifferentArgsResetConsecutive(t *testing.T) {
	cb := testBreaker(t, 2, 0, 0)
	now := time.Unix(100, 0)
	require.False(t, cb.RecordFailure("echo", map[string]any{"n": 1}, now))
	require.False(t, cb.RecordFailure("echo", map[string]any{"n": 2}, now))
	require.False(t, cb.IsTripped("echo", now))
}

func TestCircuitBreaker_SuccessClearsStreak(t *testing.T) {
	cb := testBreaker(t, 2, 0, 0)
	now := time.Unix(100, 0)
	args := map[string]any{"n": 1}
	require.False(t, cb.RecordFailure("echo", args, now))
	cb.RecordSuccess("echo")
	require.False(t, cb.RecordFailure("echo", args, now))
	require.False(t, cb.IsTripped("echo", now))
}

func TestCircuitBreaker_AlternatingPattern(t *testing.T) {
	cb := testBreaker(t, 0, 4, 0)
	now := time.Unix(100, 0)
	a := map[string]any{"q": "a"}
	b := map[string]any{"q": "b"}
	require.False(t, cb.RecordFailure("search", a, now))
	require.False(t, cb.RecordFailure("search", b, now))
	require.False(t, cb.RecordFailure("search", a, now))
	require.False(t, cb.IsTripped("search", now))
	require.True(t, cb.RecordFailure("search", b, now))
	require.True(t, cb.IsTripped("search", now))
}

func TestCircuitBreaker_ResetAfter(t *testing.T) {
	cb := testBreaker(t, 2, 0, time.Second)
	t0 := time.Unix(100, 0)
	args := map[string]any{"n": 1}
	require.False(t, cb.RecordFailure("echo", args, t0))
	require.True(t, cb.RecordFailure("echo", args, t0))
	require.True(t, cb.IsTripped("echo", t0.Add(500*time.Millisecond)))
	require.False(t, cb.IsTripped("echo", t0.Add(time.Second)))
}

func TestCircuitBreaker_StateRoundTrip(t *testing.T) {
	cb := testBreaker(t, 2, 0, 0)
	now := time.Unix(100, 0)
	args := map[string]any{"n": 1}
	cb.RecordFailure("echo", args, now)
	cb.RecordFailure("echo", args, now)
	restored := NewCircuitBreakerFromState(&types.ErrorControlConfig{
		CircuitBreaker: &types.CircuitBreakerConfig{MaxConsecutiveSameArgs: 2},
	}, cb.State())
	require.True(t, restored.IsTripped("echo", now))
}

func TestCountsTowardCircuitBreaker(t *testing.T) {
	require.True(t, CountsTowardCircuitBreaker(false, types.ToolKindNative))
	require.False(t, CountsTowardCircuitBreaker(true, types.ToolKindNative))
	require.False(t, CountsTowardCircuitBreaker(false, types.ToolKindSubAgent))
}

func TestNoteCircuitSkip_IncrementsMetric(t *testing.T) {
	ctrl := gomock.NewController(t)
	metrics := ifmocks.NewMockMetrics(ctrl)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricToolCallCircuitOpen, gomock.Any()).Times(1)
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	rt.Metrics = metrics
	rt.NoteCircuitSkip(context.Background(), logger.NoopLogger(), "boom")
}
