package base

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"testing"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/pkg/actionpolicy"
	"github.com/stretchr/testify/require"
)

func TestGateActions_DenyAndModify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer, err := actionpolicy.NewEd25519Signer("k1", priv)
	require.NoError(t, err)
	policy, err := actionpolicy.NewRuleSet("p", "1", actionpolicy.ResultAllow,
		actionpolicy.Rule{
			ID: "no-shell", Class: actionpolicy.ClassForbidden, Match: actionpolicy.Tool("shell"),
			Decision: actionpolicy.Deny("blocked"),
		},
		actionpolicy.Rule{
			ID: "cap", Class: actionpolicy.ClassStandard, Priority: 1, Match: actionpolicy.Tool("pay"),
			Decision: actionpolicy.Modify(map[string]any{"amount": 1}, "capped"),
		},
	)
	require.NoError(t, err)
	sink := actionpolicy.NewMemorySink()
	rt := newTestRuntime(sdkruntime.AgentConfig{ActionPolicy: &actionpolicy.Config{
		Policy: policy, AgentID: "agent", Signer: signer, Sink: sink,
	}})
	out, err := rt.GateActions(context.Background(), ActionGateInput{
		RunID: "run",
		Calls: []ActionCall{
			{ID: "a", Tool: "shell", Kind: "native"},
			{ID: "b", Tool: "pay", Kind: "native", Args: map[string]any{"amount": 9}},
			{ID: "c", Tool: "missing", Kind: "native", Unknown: true},
		},
	})
	require.NoError(t, err)
	require.Equal(t, actionpolicy.ResultDeny, out.Verdicts[0].Result)
	require.Equal(t, actionpolicy.CausePolicyDenied, out.Verdicts[0].Cause)
	require.Equal(t, actionpolicy.ResultModify, out.Verdicts[1].Result)
	require.Equal(t, 1, out.Verdicts[1].Args["amount"])
	require.Equal(t, actionpolicy.CauseUnknownTool, out.Verdicts[2].Cause)
	recorded, err := rt.RecordActions(context.Background(), ActionRecordInput{
		RunID:    "run",
		Verdicts: out.Verdicts,
		Completed: []ActionCompletion{
			{ToolCallID: "a", Status: actionpolicy.OutcomeNotExecuted, Cause: out.Verdicts[0].Cause},
			{ToolCallID: "b", Status: actionpolicy.OutcomeExecuted},
			{ToolCallID: "c", Status: actionpolicy.OutcomeNotExecuted, Cause: out.Verdicts[2].Cause},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 3, recorded.Summary.Count)
	require.Equal(t, 2, recorded.Summary.Denied)
	_, err = actionpolicy.VerifyLedger(sink.Receipts(), actionpolicy.StaticKeys(map[string]crypto.PublicKey{"k1": pub}))
	require.NoError(t, err)
}
