package local

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"sync/atomic"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/actionpolicy"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/stretchr/testify/require"
)

type countingTool struct {
	stubTool
	calls atomic.Int32
	args  map[string]any
}

func (t *countingTool) Execute(_ context.Context, args map[string]any) (any, error) {
	t.calls.Add(1)
	t.args = args
	return t.result, t.execErr
}

func testPolicy(t *testing.T, def actionpolicy.Result, rules ...actionpolicy.Rule) (*actionpolicy.Config, ed25519.PublicKey, *actionpolicy.MemorySink) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer, err := actionpolicy.NewEd25519Signer("k1", priv)
	require.NoError(t, err)
	policy, err := actionpolicy.NewRuleSet("payments", "1", def, rules...)
	require.NoError(t, err)
	sink := actionpolicy.NewMemorySink()
	return &actionpolicy.Config{Policy: policy, AgentID: "agent-1", Signer: signer, Sink: sink}, pub, sink
}

func newPolicyRT(t *testing.T, client interfaces.LLMClient, cfg *actionpolicy.Config) *LocalRuntime {
	t.Helper()
	rt, err := NewLocalRuntime(
		testNoDurability(),
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(runtime.AgentSpec{Name: "loop-agent", SystemPrompt: "sys"}),
		WithAgentConfig(runtime.AgentConfig{
			LLM:          runtime.AgentLLM{Client: client},
			Limits:       runtime.AgentLimits{MaxIterations: 5, Timeout: 10 * time.Second},
			ActionPolicy: cfg,
		}),
	)
	require.NoError(t, err)
	return rt
}

func TestActionPolicy_DenyDoesNotExecute(t *testing.T) {
	cfg, pub, sink := testPolicy(t, actionpolicy.ResultAllow, actionpolicy.Rule{
		ID: "no-shell", Class: actionpolicy.ClassForbidden, Match: actionpolicy.Tool("shell"),
		Decision: actionpolicy.Deny("not allowed"),
	})
	tool := &countingTool{stubTool: stubTool{name: "shell", result: "ran"}}
	client := &seqLLMClient{responses: []*interfaces.LLMResponse{
		{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "shell"}}},
		{Content: "stopped"},
	}}
	rt := newPolicyRT(t, client, cfg)
	result, err := runLoop(context.Background(), rt, []interfaces.Tool{tool}, AgentLoopInput{UserPrompt: "run it", RunID: "run-1"})
	require.NoError(t, err)
	require.Equal(t, "stopped", result.Content)
	require.Equal(t, int32(0), tool.calls.Load())
	require.NotNil(t, result.Actions)
	require.Equal(t, 1, result.Actions.Count)
	require.Equal(t, 1, result.Actions.Denied)
	_, err = actionpolicy.VerifyLedger(sink.Receipts(), actionpolicy.StaticKeys(map[string]crypto.PublicKey{"k1": pub}))
	require.NoError(t, err)
}

func TestActionPolicy_ModifyExecutesReplacementArgs(t *testing.T) {
	cfg, _, _ := testPolicy(t, actionpolicy.ResultAllow, actionpolicy.Rule{
		ID: "cap", Class: actionpolicy.ClassStandard, Priority: 1, Match: actionpolicy.Tool("pay"),
		Decision: actionpolicy.Modify(map[string]any{"amount": 1}, "capped"),
	})
	tool := &countingTool{stubTool: stubTool{name: "pay", result: "ok"}}
	client := &seqLLMClient{responses: []*interfaces.LLMResponse{
		{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "pay", Args: map[string]any{"amount": 100}}}},
		{Content: "done"},
	}}
	rt := newPolicyRT(t, client, cfg)
	_, err := runLoop(context.Background(), rt, []interfaces.Tool{tool}, AgentLoopInput{UserPrompt: "pay", RunID: "run-2"})
	require.NoError(t, err)
	require.Equal(t, int32(1), tool.calls.Load())
	require.Equal(t, 1, tool.args["amount"])
}

func TestActionPolicy_SinkFailureStopsTheRun(t *testing.T) {
	cfg, _, _ := testPolicy(t, actionpolicy.ResultAllow)
	cfg.Sink = errSink{}
	tool := &countingTool{stubTool: stubTool{name: "add", result: "7"}}
	client := &seqLLMClient{responses: []*interfaces.LLMResponse{
		{ToolCalls: []*interfaces.ToolCall{{ToolCallID: "c1", ToolName: "add"}}},
	}}
	rt := newPolicyRT(t, client, cfg)
	_, err := runLoop(context.Background(), rt, []interfaces.Tool{tool}, AgentLoopInput{UserPrompt: "add", RunID: "run-3"})
	require.ErrorIs(t, err, types.ErrActionPolicyUnavailable)
	require.Equal(t, int32(0), tool.calls.Load())
}

type errSink struct{}

func (errSink) Write(context.Context, actionpolicy.SignedReceipt) error {
	return context.DeadlineExceeded
}
