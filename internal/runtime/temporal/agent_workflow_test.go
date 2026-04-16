package temporal

import (
	"context"
	"testing"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces/mocks"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

func testRuntimeForWorkflow(t *testing.T) *TemporalRuntime {
	t.Helper()
	return &TemporalRuntime{
		TemporalRuntimeConfig: TemporalRuntimeConfig{
			AgentSpec: sdkruntime.AgentSpec{Name: "WorkflowTestAgent"},
			AgentExecution: sdkruntime.AgentExecution{
				LLM:     sdkruntime.AgentLLM{Client: stubLLM{}},
				Limits:  sdkruntime.AgentLimits{MaxIterations: 5},
				Tools:   sdkruntime.AgentTools{Tools: nil},
				Session: sdkruntime.AgentSession{},
			},
			logger: logger.NoopLogger(),
		},
	}
}

// newActivityTestEnv returns a [testsuite.TestActivityEnvironment] for isolated activity tests.
func newActivityTestEnv(t *testing.T) *testsuite.TestActivityEnvironment {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	return suite.NewTestActivityEnvironment()
}

// streamCapableStubLLM enables the streaming branch in [AgentWorkflow] (useStreaming == true).
type streamCapableStubLLM struct{ stubLLM }

func (streamCapableStubLLM) IsStreamSupported() bool { return true }

func TestAgentWorkflow_SingleLLMNoTools(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)

	env.RegisterWorkflow(rt.AgentWorkflow)
	env.OnActivity(rt.AgentLLMActivity, mock.Anything, mock.Anything).Return(func(ctx context.Context, in AgentLLMInput) (*AgentLLMResult, error) {
		if in.Messages == nil {
			t.Error("expected messages")
		}
		return &AgentLLMResult{Content: "final answer", ToolCalls: nil}, nil
	})

	env.ExecuteWorkflow(rt.AgentWorkflow, AgentWorkflowInput{
		UserPrompt:       "hello",
		StreamingEnabled: false,
	})

	require.True(t, env.IsWorkflowCompleted())
	var out types.AgentResponse
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "final answer", out.Content)
	require.Equal(t, "WorkflowTestAgent", out.AgentName)
	require.Equal(t, "stub", out.Model)
}

func TestAgentWorkflow_StreamingPath_UsesStreamActivity(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)
	rt.AgentExecution.LLM.Client = streamCapableStubLLM{}

	env.RegisterWorkflow(rt.AgentWorkflow)
	env.OnActivity(rt.AgentLLMStreamActivity, mock.Anything, mock.Anything).Return(func(ctx context.Context, in AgentLLMStreamInput) (*AgentLLMResult, error) {
		return &AgentLLMResult{Content: "streamed", ToolCalls: nil}, nil
	})

	env.ExecuteWorkflow(rt.AgentWorkflow, AgentWorkflowInput{
		UserPrompt:       "hi",
		StreamingEnabled: true,
	})

	require.True(t, env.IsWorkflowCompleted())
	var out types.AgentResponse
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "streamed", out.Content)
}

func TestAgentWorkflow_OneToolThenFinal(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)

	var llmCalls int
	env.RegisterWorkflow(rt.AgentWorkflow)
	env.OnActivity(rt.AgentLLMActivity, mock.Anything, mock.Anything).Return(func(ctx context.Context, in AgentLLMInput) (*AgentLLMResult, error) {
		llmCalls++
		if llmCalls == 1 {
			return &AgentLLMResult{
				Content: "using tool",
				ToolCalls: []ToolCallRequest{{
					ToolCallID: "tc1",
					ToolName:   "echo",
					Args:       map[string]any{"x": 1},
				}},
			}, nil
		}
		return &AgentLLMResult{Content: "after tool", ToolCalls: nil}, nil
	})
	env.OnActivity(rt.AgentToolExecuteActivity, mock.Anything, mock.Anything).Return(func(ctx context.Context, in AgentToolExecuteInput) (string, error) {
		if in.ToolName != "echo" {
			t.Errorf("tool name = %q", in.ToolName)
		}
		return "echo ok", nil
	})
	env.OnActivity(rt.AgentToolAuthorizeActivity, mock.Anything, mock.Anything).Return(func(ctx context.Context, in AgentToolAuthorizeInput) (AgentToolAuthorizeResult, error) {
		return AgentToolAuthorizeResult{Allowed: true}, nil
	})

	env.ExecuteWorkflow(rt.AgentWorkflow, AgentWorkflowInput{
		UserPrompt: "run",
	})

	require.True(t, env.IsWorkflowCompleted())
	var out types.AgentResponse
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "after tool", out.Content)
	require.Equal(t, 2, llmCalls)
}

func TestAgentWorkflow_ToolAuthorizationDenied_SkipsExecute(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)

	var llmCalls int
	env.RegisterWorkflow(rt.AgentWorkflow)
	env.OnActivity(rt.AgentLLMActivity, mock.Anything, mock.Anything).Return(func(ctx context.Context, in AgentLLMInput) (*AgentLLMResult, error) {
		llmCalls++
		if llmCalls == 1 {
			return &AgentLLMResult{
				Content: "using tool",
				ToolCalls: []ToolCallRequest{{
					ToolCallID: "tc-auth-deny",
					ToolName:   "echo",
					Args:       map[string]any{"x": 1},
				}},
			}, nil
		}
		return &AgentLLMResult{Content: "after deny", ToolCalls: nil}, nil
	})
	env.OnActivity(rt.AgentToolAuthorizeActivity, mock.Anything, mock.Anything).Return(func(ctx context.Context, in AgentToolAuthorizeInput) (AgentToolAuthorizeResult, error) {
		return AgentToolAuthorizeResult{Allowed: false, Reason: "missing scope"}, nil
	})

	env.ExecuteWorkflow(rt.AgentWorkflow, AgentWorkflowInput{
		UserPrompt: "run",
	})

	require.True(t, env.IsWorkflowCompleted())
	var out types.AgentResponse
	require.NoError(t, env.GetWorkflowResult(&out))
	require.Equal(t, "after deny", out.Content)
	require.Equal(t, 2, llmCalls)
}

func TestAgentLLMActivity_MockLLM_TextOnly(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockLLM := mocks.NewMockLLMClient(ctrl)
	mockLLM.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(&interfaces.LLMResponse{
		Content: "final",
	}, nil)

	rt := &TemporalRuntime{
		TemporalRuntimeConfig: TemporalRuntimeConfig{
			AgentSpec: sdkruntime.AgentSpec{Name: "ActTest"},
			AgentExecution: sdkruntime.AgentExecution{
				LLM: sdkruntime.AgentLLM{Client: mockLLM},
			},
			logger: logger.NoopLogger(),
		},
	}

	actEnv := newActivityTestEnv(t)
	actEnv.RegisterActivity(rt.AgentLLMActivity)
	val, err := actEnv.ExecuteActivity(rt.AgentLLMActivity, AgentLLMInput{
		Messages: []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "hi"}},
	})
	require.NoError(t, err)

	var got AgentLLMResult
	require.NoError(t, val.Get(&got))
	require.Equal(t, "final", got.Content)
	require.Empty(t, got.ToolCalls)
}

func TestAgentLLMActivity_MockLLM_ToolCalls(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockTool := mocks.NewMockTool(ctrl)
	mockTool.EXPECT().Name().Return("echo").AnyTimes()
	mockTool.EXPECT().Description().Return("d").AnyTimes()
	mockTool.EXPECT().Parameters().Return(interfaces.JSONSchema{}).AnyTimes()

	policy := mocks.NewMockAgentToolApprovalPolicy(ctrl)
	policy.EXPECT().RequiresApproval(gomock.Any()).Return(false).AnyTimes()

	mockLLM := mocks.NewMockLLMClient(ctrl)
	mockLLM.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(&interfaces.LLMResponse{
		Content: "call tool",
		ToolCalls: []*interfaces.ToolCall{{
			ToolCallID: "tc1",
			ToolName:   "echo",
			Args:       map[string]any{"x": 1.0},
		}},
	}, nil)

	rt := &TemporalRuntime{
		TemporalRuntimeConfig: TemporalRuntimeConfig{
			AgentSpec: sdkruntime.AgentSpec{Name: "ActTest"},
			AgentExecution: sdkruntime.AgentExecution{
				LLM:   sdkruntime.AgentLLM{Client: mockLLM},
				Tools: sdkruntime.AgentTools{Tools: []interfaces.Tool{mockTool}, ApprovalPolicy: policy},
			},
			logger: logger.NoopLogger(),
		},
	}

	actEnv := newActivityTestEnv(t)
	actEnv.RegisterActivity(rt.AgentLLMActivity)
	val, err := actEnv.ExecuteActivity(rt.AgentLLMActivity, AgentLLMInput{
		Messages: []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "run"}},
	})
	require.NoError(t, err)

	var got AgentLLMResult
	require.NoError(t, val.Get(&got))
	require.Len(t, got.ToolCalls, 1)
	require.Equal(t, "echo", got.ToolCalls[0].ToolName)
	require.Equal(t, "tc1", got.ToolCalls[0].ToolCallID)
	require.False(t, got.ToolCalls[0].NeedsApproval)
}

func TestAgentLLMActivity_MockLLM_UnknownToolError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockLLM := mocks.NewMockLLMClient(ctrl)
	mockLLM.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(&interfaces.LLMResponse{
		Content: "x",
		ToolCalls: []*interfaces.ToolCall{{
			ToolCallID: "1",
			ToolName:   "not_registered",
			Args:       nil,
		}},
	}, nil)

	rt := &TemporalRuntime{
		TemporalRuntimeConfig: TemporalRuntimeConfig{
			AgentExecution: sdkruntime.AgentExecution{
				LLM:   sdkruntime.AgentLLM{Client: mockLLM},
				Tools: sdkruntime.AgentTools{Tools: []interfaces.Tool{}},
			},
			logger: logger.NoopLogger(),
		},
	}

	actEnv := newActivityTestEnv(t)
	actEnv.RegisterActivity(rt.AgentLLMActivity)
	_, err := actEnv.ExecuteActivity(rt.AgentLLMActivity, AgentLLMInput{
		Messages: []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "q"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool")
}

func TestAgentLLMActivity_MockConversationAndLLM(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockConv := mocks.NewMockConversation(ctrl)
	mockConv.EXPECT().ListMessages(gomock.Any(), "conv-1", gomock.Any()).Return(
		[]interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "prior"}},
		nil,
	)

	mockLLM := mocks.NewMockLLMClient(ctrl)
	mockLLM.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(&interfaces.LLMResponse{Content: "answer"}, nil)

	rt := &TemporalRuntime{
		TemporalRuntimeConfig: TemporalRuntimeConfig{
			AgentExecution: sdkruntime.AgentExecution{
				LLM: sdkruntime.AgentLLM{Client: mockLLM},
				Session: sdkruntime.AgentSession{
					Conversation:     mockConv,
					ConversationSize: 10,
				},
			},
			logger: logger.NoopLogger(),
		},
	}

	actEnv := newActivityTestEnv(t)
	actEnv.RegisterActivity(rt.AgentLLMActivity)
	val, err := actEnv.ExecuteActivity(rt.AgentLLMActivity, AgentLLMInput{
		ConversationID: "conv-1",
		Messages:       []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "new"}},
	})
	require.NoError(t, err)

	var got AgentLLMResult
	require.NoError(t, val.Get(&got))
	require.Equal(t, "answer", got.Content)
}

func TestAgentLLMActivity_ConversationNotConfigured(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockLLM := mocks.NewMockLLMClient(ctrl)

	rt := &TemporalRuntime{
		TemporalRuntimeConfig: TemporalRuntimeConfig{
			AgentExecution: sdkruntime.AgentExecution{
				LLM:     sdkruntime.AgentLLM{Client: mockLLM},
				Session: sdkruntime.AgentSession{Conversation: nil},
			},
			logger: logger.NoopLogger(),
		},
	}

	actEnv := newActivityTestEnv(t)
	actEnv.RegisterActivity(rt.AgentLLMActivity)
	_, err := actEnv.ExecuteActivity(rt.AgentLLMActivity, AgentLLMInput{
		ConversationID: "any",
		Messages:       []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "x"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "conversation is not configured")
}

func TestAgentLLMStreamActivity_MockLLM_FallbackToGenerate(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockLLM := mocks.NewMockLLMClient(ctrl)
	mockLLM.EXPECT().IsStreamSupported().Return(false)
	mockLLM.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(&interfaces.LLMResponse{Content: "gen"}, nil)

	rt := &TemporalRuntime{
		TemporalRuntimeConfig: TemporalRuntimeConfig{
			AgentSpec: sdkruntime.AgentSpec{Name: "StreamAct"},
			AgentExecution: sdkruntime.AgentExecution{
				LLM: sdkruntime.AgentLLM{Client: mockLLM},
			},
			logger: logger.NoopLogger(),
		},
	}

	actEnv := newActivityTestEnv(t)
	actEnv.RegisterActivity(rt.AgentLLMStreamActivity)
	val, err := actEnv.ExecuteActivity(rt.AgentLLMStreamActivity, AgentLLMStreamInput{
		AgentName:        "StreamAct",
		Messages:         []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "s"}},
		LocalChannelName: "ch",
	})
	require.NoError(t, err)

	var got AgentLLMResult
	require.NoError(t, val.Get(&got))
	require.Equal(t, "gen", got.Content)
}

func TestMergeLLMUsage(t *testing.T) {
	a := &interfaces.LLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}
	b := &interfaces.LLMUsage{PromptTokens: 3, CompletionTokens: 7, TotalTokens: 10, CachedPromptTokens: 2, ReasoningTokens: 1}

	got := mergeLLMUsage(a, b)
	if got.PromptTokens != 13 || got.CompletionTokens != 12 || got.TotalTokens != 25 {
		t.Fatalf("mergeLLMUsage: got %+v", got)
	}
	if got.CachedPromptTokens != 2 || got.ReasoningTokens != 1 {
		t.Fatalf("mergeLLMUsage optional fields: got %+v", got)
	}

	if mergeLLMUsage(nil, nil) != nil {
		t.Fatal("nil + nil should be nil")
	}
	if x := mergeLLMUsage(nil, b); x.PromptTokens != b.PromptTokens {
		t.Fatal("nil + b should copy b")
	}
}
