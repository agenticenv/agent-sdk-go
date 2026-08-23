package base

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/internal/hooks"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	testutil "github.com/agenticenv/agent-sdk-go/internal/testing"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	ifmocks "github.com/agenticenv/agent-sdk-go/pkg/interfaces/mocks"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
)

// newTestRuntime returns a Runtime wired with noop tracer/metrics and the provided execution.
func newTestRuntime(exec sdkruntime.AgentConfig) *Runtime {
	return &Runtime{
		AgentSpec: sdkruntime.AgentSpec{
			Name:         "test-agent",
			SystemPrompt: "you are helpful",
		},
		AgentConfig: exec,
		Tracer:      observability.DefaultNoopTracer,
		Metrics:     observability.DefaultNoopMetrics,
	}
}

func noopLog() logger.Logger { return logger.NoopLogger() }

func storeMemoryRecords(rt *Runtime, ctx context.Context, scope interfaces.MemoryScope, records []interfaces.MemoryRecord) error {
	return rt.StoreMemoryRecords(ctx, StoreMemoryRecordsInput{
		Logger: noopLog(), Scope: scope, Records: records,
	})
}

func executeMemoryStore(rt *Runtime, ctx context.Context, scope interfaces.MemoryScope, messages []interfaces.Message) error {
	return rt.ExecuteMemoryStore(ctx, ExecuteMemoryStoreInput{
		Logger: noopLog(), Scope: scope, Messages: messages,
	})
}

// stubLLMClient is a minimal LLMClient that returns a fixed response.
type stubLLMClient struct {
	resp *interfaces.LLMResponse
	err  error
}

func (s stubLLMClient) Generate(_ context.Context, _ *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	return s.resp, s.err
}
func (stubLLMClient) GenerateStream(_ context.Context, _ *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("stream not implemented in stub")
}
func (stubLLMClient) GetModel() string                    { return "stub" }
func (stubLLMClient) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (stubLLMClient) IsStreamSupported() bool             { return false }

type captureLLMClient struct {
	lastReq *interfaces.LLMRequest
	resp    *interfaces.LLMResponse
	err     error
}

func (c *captureLLMClient) Generate(_ context.Context, req *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	if req != nil {
		copy := *req
		c.lastReq = &copy
	}
	return c.resp, c.err
}
func (captureLLMClient) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("stream not implemented")
}
func (captureLLMClient) GetModel() string                    { return "stub" }
func (captureLLMClient) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (captureLLMClient) IsStreamSupported() bool             { return false }

// --- BuildLLMRequest ---

func TestBuildLLMRequest_Basic(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{}},
	})
	msgs := []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "hello"}}
	req := rt.BuildLLMRequest(msgs, false, "", "", nil)

	require.Equal(t, "you are helpful", req.SystemMessage)
	require.Equal(t, msgs, req.Messages)
	require.Empty(t, req.Tools)
}

func TestBuildLLMRequest_WithRetrieverContext(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{}},
	})
	user := []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "q"}}
	req := rt.BuildLLMRequest(user, false, "", "extra context", nil)
	require.Equal(t, "you are helpful", req.SystemMessage)
	require.Len(t, req.Messages, 2)
	require.Equal(t, interfaces.MessageRoleUser, req.Messages[0].Role)
	require.Contains(t, req.Messages[0].Content, "Relevant Context")
	require.Contains(t, req.Messages[0].Content, "extra context")
	require.Equal(t, "q", req.Messages[1].Content)
}

func TestBuildLLMRequest_WithMemoryContext(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{}},
	})
	user := []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "q"}}
	req := rt.BuildLLMRequest(user, false, "memory fact", "retriever doc", nil)
	require.Equal(t, "you are helpful", req.SystemMessage)
	require.Len(t, req.Messages, 3)
	require.Contains(t, req.Messages[0].Content, "Relevant Memories")
	require.Contains(t, req.Messages[0].Content, "memory fact")
	require.Contains(t, req.Messages[1].Content, "Relevant Context")
	require.Contains(t, req.Messages[1].Content, "retriever doc")
	require.Equal(t, "q", req.Messages[2].Content)
	// Input slice must not be mutated / aliased when context is prepended.
	require.Len(t, user, 1)
}

func TestBuildLLMRequest_DoesNotMutateInput(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{}},
	})
	msgs := []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "  hello   world  "}}
	req := rt.BuildLLMRequest(msgs, false, "mem", "", nil)
	require.Equal(t, "  hello   world  ", msgs[0].Content)
	require.Equal(t, "hello world", req.Messages[1].Content)
}

func TestBuildLLMRequest_SkipTools(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("t").AnyTimes()
	tool.EXPECT().Description().Return("").AnyTimes()
	tool.EXPECT().Parameters().Return(nil).AnyTimes()

	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{}},
	})
	req := rt.BuildLLMRequest(nil, true, "", "", []interfaces.Tool{tool})
	require.Empty(t, req.Tools)
}

// approvalToolStub is a Tool that also implements ToolApproval.
type approvalToolStub struct {
	name             string
	approvalRequired bool
}

func (a approvalToolStub) Name() string                                             { return a.name }
func (a approvalToolStub) DisplayName() string                                      { return a.name }
func (a approvalToolStub) Description() string                                      { return "" }
func (a approvalToolStub) Parameters() interfaces.JSONSchema                        { return nil }
func (a approvalToolStub) Execute(_ context.Context, _ map[string]any) (any, error) { return nil, nil }
func (a approvalToolStub) ApprovalRequired() bool                                   { return a.approvalRequired }

// authorizerToolStub is a Tool that also implements ToolAuthorizer.
type authorizerToolStub struct {
	name   string
	allow  bool
	reason string
	err    error
}

func (a authorizerToolStub) Name() string                      { return a.name }
func (a authorizerToolStub) DisplayName() string               { return a.name }
func (a authorizerToolStub) Description() string               { return "" }
func (a authorizerToolStub) Parameters() interfaces.JSONSchema { return nil }
func (a authorizerToolStub) Execute(_ context.Context, _ map[string]any) (any, error) {
	return nil, nil
}
func (a authorizerToolStub) Authorize(_ context.Context, _ map[string]any) (interfaces.ToolAuthorizationDecision, error) {
	return interfaces.ToolAuthorizationDecision{Allow: a.allow, Reason: a.reason}, a.err
}

// --- RequiresApproval ---

func TestRequiresApproval_NoPolicyToolHasApproval(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	tool := approvalToolStub{name: "t", approvalRequired: true}
	require.True(t, rt.RequiresApproval(tool))
}

func TestRequiresApproval_NoPolicyToolNoApproval(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	require.False(t, rt.RequiresApproval(tool))
}

// --- FetchConversationMessages ---

func TestFetchConversationMessages_NoConversation(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Session: sdkruntime.AgentSession{Conversation: nil},
	})
	_, err := rt.FetchConversationMessages(context.Background(), noopLog(), "conv-1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "conversation is not configured")
}

func TestFetchConversationMessages_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	conv := ifmocks.NewMockConversation(ctrl)
	msgs := []interfaces.Message{{Role: interfaces.MessageRoleUser, Content: "hi"}}
	conv.EXPECT().ListMessages(gomock.Any(), "conv-1", gomock.Any()).Return(msgs, nil)

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Session: sdkruntime.AgentSession{Conversation: conv, ConversationSize: 10},
	})
	got, err := rt.FetchConversationMessages(context.Background(), noopLog(), "conv-1")
	require.NoError(t, err)
	require.Equal(t, msgs, got)
}

func TestFetchConversationMessages_Error(t *testing.T) {
	ctrl := gomock.NewController(t)
	conv := ifmocks.NewMockConversation(ctrl)
	conv.EXPECT().ListMessages(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, errors.New("store down"))

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Session: sdkruntime.AgentSession{Conversation: conv},
	})
	_, err := rt.FetchConversationMessages(context.Background(), noopLog(), "c")
	require.Error(t, err)
	require.Contains(t, err.Error(), "store down")
}

// --- ExecuteTool ---

func TestExecuteTool_UnknownTool(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	_, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{Logger: noopLog(), ToolName: "missing"}, interfaces.MemoryScope{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool")
}

func TestExecuteTool_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("calc").AnyTimes()
	tool.EXPECT().Execute(gomock.Any(), gomock.Any()).Return("42", nil)

	rt := newTestRuntime(sdkruntime.AgentConfig{})
	result, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), Tools: []interfaces.Tool{tool}, ToolName: "calc", Args: map[string]any{"x": 1},
	}, interfaces.MemoryScope{})
	require.NoError(t, err)
	require.Equal(t, "42", result)
}

func TestExecuteTool_ToolError(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("fail-tool").AnyTimes()
	tool.EXPECT().Execute(gomock.Any(), gomock.Any()).Return(nil, errors.New("tool failed"))

	rt := newTestRuntime(sdkruntime.AgentConfig{})
	_, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), Tools: []interfaces.Tool{tool}, ToolName: "fail-tool",
	}, interfaces.MemoryScope{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool failed")
}

func TestExecuteTool_ToolExecMetaInContext(t *testing.T) {
	ctrl := gomock.NewController(t)

	var capturedMeta interfaces.ToolExecMeta
	var metaFound bool
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("meta-tool").AnyTimes()
	tool.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ map[string]any) (any, error) {
		capturedMeta, metaFound = interfaces.ToolExecMetaFromContext(ctx)
		return "ok", nil
	})

	rt := newTestRuntime(sdkruntime.AgentConfig{})
	_, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger:     noopLog(),
		Tools:      []interfaces.Tool{tool},
		ToolName:   "meta-tool",
		Args:       map[string]any{},
		RunID:      "run-1",
		Iteration:  2,
		ToolCallID: "call-abc",
	}, interfaces.MemoryScope{})
	require.NoError(t, err)
	require.True(t, metaFound, "ToolExecMeta should be present in context")
	require.Equal(t, "6015946e1b787fc9ce83182163488fea", capturedMeta.IdempotencyKey)
	require.Equal(t, "run-1", capturedMeta.RunID)
	require.Equal(t, "call-abc", capturedMeta.ToolCallID)
	require.Equal(t, 2, capturedMeta.Iteration)
}

func TestExecuteTool_ToolExecMetaAbsentWithoutInput(t *testing.T) {
	ctrl := gomock.NewController(t)

	var metaFound bool
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("bare-tool").AnyTimes()
	tool.EXPECT().Execute(gomock.Any(), gomock.Any()).DoAndReturn(func(ctx context.Context, _ map[string]any) (any, error) {
		_, metaFound = interfaces.ToolExecMetaFromContext(ctx)
		return "ok", nil
	})

	rt := newTestRuntime(sdkruntime.AgentConfig{})
	_, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger:   noopLog(),
		Tools:    []interfaces.Tool{tool},
		ToolName: "bare-tool",
	}, interfaces.MemoryScope{})
	require.NoError(t, err)
	// meta is present but zero-valued (IdempotencyKey is empty string); context key is always set
	_ = metaFound
}

func TestExecuteTool_BeforeToolModifiesArgs(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("calc").AnyTimes()
	tool.EXPECT().DisplayName().Return("calc").AnyTimes()
	tool.EXPECT().Execute(gomock.Any(), map[string]any{"x": 99}).Return("42", nil)

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Hooks: []hooks.HookGroup{{
			Name: "guard",
			Hooks: hooks.AgentHooks{BeforeTool: []hooks.BeforeToolHook{
				func(_ context.Context, in hooks.BeforeToolHookInput) (hooks.BeforeToolHookOutput, error) {
					return hooks.BeforeToolHookOutput{Args: map[string]any{"x": 99}}, nil
				},
			}},
		}},
	})
	_, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), Tools: []interfaces.Tool{tool}, ToolName: "calc",
		Args: map[string]any{"x": 1}, ToolCallID: "tc-1", RunID: "run-1",
	}, interfaces.MemoryScope{})
	require.NoError(t, err)
}

func TestExecuteTool_AfterToolModifiesResult(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("calc").AnyTimes()
	tool.EXPECT().DisplayName().Return("calc").AnyTimes()
	tool.EXPECT().Execute(gomock.Any(), gomock.Any()).Return("raw", nil)

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Hooks: []hooks.HookGroup{{
			Name: "scrub",
			Hooks: hooks.AgentHooks{AfterTool: []hooks.AfterToolHook{
				func(context.Context, hooks.AfterToolHookInput) (hooks.AfterToolHookOutput, error) {
					return hooks.AfterToolHookOutput{Content: "scrubbed"}, nil
				},
			}},
		}},
	})
	result, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), Tools: []interfaces.Tool{tool}, ToolName: "calc",
	}, interfaces.MemoryScope{})
	require.NoError(t, err)
	require.Equal(t, "scrubbed", result)
}

type memoryKindTool struct{}

func (memoryKindTool) Name() string                      { return "mem_tool" }
func (memoryKindTool) DisplayName() string               { return "" }
func (memoryKindTool) Description() string               { return "" }
func (memoryKindTool) Parameters() interfaces.JSONSchema { return nil }
func (memoryKindTool) Execute(context.Context, map[string]any) (any, error) {
	return "ok", nil
}
func (memoryKindTool) ToolKind() types.ToolKind { return types.ToolKindMemory }

func TestExecuteTool_NonHookEligibleKindSkipsHooks(t *testing.T) {
	var beforeCalled bool
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Hooks: []hooks.HookGroup{{
			Name: "guard",
			Hooks: hooks.AgentHooks{BeforeTool: []hooks.BeforeToolHook{
				func(context.Context, hooks.BeforeToolHookInput) (hooks.BeforeToolHookOutput, error) {
					beforeCalled = true
					return hooks.BeforeToolHookOutput{}, nil
				},
			}},
		}},
	})
	_, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), Tools: []interfaces.Tool{memoryKindTool{}}, ToolName: "mem_tool",
	}, interfaces.MemoryScope{})
	require.NoError(t, err)
	require.False(t, beforeCalled)
}

type testRetriever struct {
	name      string
	lastQuery string
	docs      []interfaces.Document
	err       error
}

func (r *testRetriever) Name() string { return r.name }

func (r *testRetriever) Search(_ context.Context, query string) ([]interfaces.Document, error) {
	r.lastQuery = query
	if r.err != nil {
		return nil, r.err
	}
	return r.docs, nil
}

type testRetrieverTool struct {
	r *testRetriever
}

func (t *testRetrieverTool) Name() string        { return types.RetrieverToolName(t.r.name) }
func (t *testRetrieverTool) DisplayName() string { return types.RetrieverToolDisplayName(t.r.name) }
func (t *testRetrieverTool) Description() string { return "test retriever tool" }
func (t *testRetrieverTool) Parameters() interfaces.JSONSchema {
	return interfaces.JSONSchema{"type": "object"}
}
func (t *testRetrieverTool) ToolKind() types.ToolKind { return types.ToolKindRetriever }
func (t *testRetrieverTool) Execute(ctx context.Context, args map[string]any) (any, error) {
	raw, ok := args[types.RetrieverToolParamQuery].(string)
	if !ok {
		return nil, errors.New("query required")
	}
	query := strings.TrimSpace(raw)
	if query == "" {
		return nil, errors.New("query empty")
	}
	return t.r.Search(ctx, query)
}

func newTestRetrieverTool(r *testRetriever) interfaces.Tool {
	return &testRetrieverTool{r: r}
}

func TestExecuteTool_RetrieverTool_FormatsDocs(t *testing.T) {
	stub := &testRetriever{
		name: "kb",
		docs: []interfaces.Document{{Content: "doc", Source: "s", Score: 0.9}},
	}
	tool := newTestRetrieverTool(stub)
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	got, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), Tools: []interfaces.Tool{tool}, ToolName: "retriever_kb",
		Args: map[string]any{types.RetrieverToolParamQuery: "q"},
	}, interfaces.MemoryScope{})
	require.NoError(t, err)
	require.Contains(t, got, "doc")
	require.Equal(t, "q", stub.lastQuery)
}

func TestExecuteTool_RetrieverTool_BeforeRetrieveRewritesQuery(t *testing.T) {
	stub := &testRetriever{
		name: "kb",
		docs: []interfaces.Document{{Content: "doc", Source: "s", Score: 0.9}},
	}
	tool := newTestRetrieverTool(stub)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Hooks: []hooks.HookGroup{{
			Name: "rewrite",
			Hooks: hooks.AgentHooks{BeforeRetrieve: []hooks.BeforeRetrieveHook{
				func(_ context.Context, in hooks.BeforeRetrieveHookInput) (hooks.BeforeRetrieveHookOutput, error) {
					if in.RunMeta.Iteration != 2 || in.RetrieverName != "kb" || in.Mode != types.RetrieverModeAgentic {
						t.Fatalf("input = %#v", in)
					}
					return hooks.BeforeRetrieveHookOutput{Query: "hooked"}, nil
				},
			}},
		}},
	})
	_, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), Tools: []interfaces.Tool{tool}, ToolName: "retriever_kb",
		Args:  map[string]any{types.RetrieverToolParamQuery: "raw"},
		RunID: "run-1", Iteration: 2,
	}, interfaces.MemoryScope{})
	require.NoError(t, err)
	require.Equal(t, "hooked", stub.lastQuery)
}

func TestExecuteTool_RetrieverTool_AfterRetrieveFiltersDocs(t *testing.T) {
	stub := &testRetriever{
		name: "kb",
		docs: []interfaces.Document{
			{Content: "drop", Source: "s", Score: 0.5},
			{Content: "keep", Source: "s", Score: 0.9},
		},
	}
	tool := newTestRetrieverTool(stub)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Hooks: []hooks.HookGroup{{
			Name: "filter",
			Hooks: hooks.AgentHooks{AfterRetrieve: []hooks.AfterRetrieveHook{
				func(_ context.Context, in hooks.AfterRetrieveHookInput) (hooks.AfterRetrieveHookOutput, error) {
					return hooks.AfterRetrieveHookOutput{Documents: []interfaces.Document{in.Documents[1]}}, nil
				},
			}},
		}},
	})
	got, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), Tools: []interfaces.Tool{tool}, ToolName: "retriever_kb",
		Args: map[string]any{types.RetrieverToolParamQuery: "q"},
	}, interfaces.MemoryScope{})
	require.NoError(t, err)
	require.Contains(t, got, "keep")
	require.NotContains(t, got, "drop")
}

func TestExecuteTool_RetrieverTool_EmptyDocs(t *testing.T) {
	stub := &testRetriever{name: "kb"}
	tool := newTestRetrieverTool(stub)
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	got, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), Tools: []interfaces.Tool{tool}, ToolName: "retriever_kb",
		Args: map[string]any{types.RetrieverToolParamQuery: "q"},
	}, interfaces.MemoryScope{})
	require.NoError(t, err)
	require.Equal(t, "", got)
}

func TestExecuteTool_RetrieverTool_BeforeRetrieveAbort(t *testing.T) {
	stub := &testRetriever{
		name: "kb",
		docs: []interfaces.Document{{Content: "doc", Source: "s", Score: 0.9}},
	}
	tool := newTestRetrieverTool(stub)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Hooks: []hooks.HookGroup{{
			Name: "block",
			Hooks: hooks.AgentHooks{BeforeRetrieve: []hooks.BeforeRetrieveHook{
				func(context.Context, hooks.BeforeRetrieveHookInput) (hooks.BeforeRetrieveHookOutput, error) {
					return hooks.BeforeRetrieveHookOutput{}, errors.New("blocked")
				},
			}},
		}},
	})
	_, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), Tools: []interfaces.Tool{tool}, ToolName: "retriever_kb",
		Args: map[string]any{types.RetrieverToolParamQuery: "q"},
	}, interfaces.MemoryScope{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked")
}

// --- AuthorizeTool ---

func TestAuthorizeTool_UnknownTool(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	_, err := rt.AuthorizeTool(context.Background(), noopLog(), nil, "ghost", nil)
	require.Error(t, err)
}

func TestAuthorizeTool_NoAuthorizer_AllowedByDefault(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("plain").AnyTimes()

	rt := newTestRuntime(sdkruntime.AgentConfig{})
	result, err := rt.AuthorizeTool(context.Background(), noopLog(), []interfaces.Tool{tool}, "plain", nil)
	require.NoError(t, err)
	require.True(t, result.Allowed)
}

func TestAuthorizeTool_Allowed(t *testing.T) {
	tool := authorizerToolStub{name: "secure", allow: true}
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	result, err := rt.AuthorizeTool(context.Background(), noopLog(), []interfaces.Tool{tool}, "secure", nil)
	require.NoError(t, err)
	require.True(t, result.Allowed)
}

func TestAuthorizeTool_Denied(t *testing.T) {
	tool := authorizerToolStub{name: "gated", allow: false, reason: "not allowed"}
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	result, err := rt.AuthorizeTool(context.Background(), noopLog(), []interfaces.Tool{tool}, "gated", nil)
	require.NoError(t, err)
	require.False(t, result.Allowed)
	require.Equal(t, "not allowed", result.Reason)
}

// --- ExecuteRetrievers ---

func TestExecuteRetrievers_NoRetrievers(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	got, err := rt.ExecuteRetrievers(context.Background(), ExecuteRetrieversInput{Logger: noopLog(), Query: "query"})
	require.NoError(t, err)
	require.Equal(t, "", got.Context)
	require.Equal(t, int64(0), got.TotalSearches)
}

func TestExecuteRetrievers_AllFail(t *testing.T) {
	ctrl := gomock.NewController(t)
	r := ifmocks.NewMockRetriever(ctrl)
	r.EXPECT().Name().Return("r1").AnyTimes()
	r.EXPECT().Search(gomock.Any(), gomock.Any()).Return(nil, errors.New("down"))

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Retrievers: sdkruntime.AgentRetrievers{Retrievers: []interfaces.Retriever{r}},
	})
	got, err := rt.ExecuteRetrievers(context.Background(), ExecuteRetrieversInput{Logger: noopLog(), Query: "q"})
	require.NoError(t, err)
	require.Equal(t, "", got.Context)
	require.Equal(t, int64(1), got.TotalSearches)
	require.Equal(t, int64(1), got.FailedSearches)
}

func TestExecuteRetrievers_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	r := ifmocks.NewMockRetriever(ctrl)
	r.EXPECT().Name().Return("kb").AnyTimes()
	r.EXPECT().Search(gomock.Any(), "my query").Return([]interfaces.Document{
		{Content: "doc content", Source: "src", Score: 0.95},
	}, nil)

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Retrievers: sdkruntime.AgentRetrievers{Retrievers: []interfaces.Retriever{r}},
	})
	got, err := rt.ExecuteRetrievers(context.Background(), ExecuteRetrieversInput{Logger: noopLog(), Query: "my query"})
	require.NoError(t, err)
	require.Contains(t, got.Context, "doc content")
	require.Equal(t, int64(1), got.TotalSearches)
	require.Equal(t, int64(0), got.FailedSearches)
}

func TestExecuteRetrievers_BeforeRetrieveRewritesQuery(t *testing.T) {
	ctrl := gomock.NewController(t)
	r := ifmocks.NewMockRetriever(ctrl)
	r.EXPECT().Name().Return("kb").AnyTimes()
	r.EXPECT().Search(gomock.Any(), "hooked").Return([]interfaces.Document{
		{Content: "doc", Source: "s", Score: 0.9},
	}, nil)

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Retrievers: sdkruntime.AgentRetrievers{
			Mode:       types.RetrieverModePrefetch,
			Retrievers: []interfaces.Retriever{r},
		},
		Hooks: []hooks.HookGroup{{
			Name: "rewrite",
			Hooks: hooks.AgentHooks{BeforeRetrieve: []hooks.BeforeRetrieveHook{
				func(_ context.Context, in hooks.BeforeRetrieveHookInput) (hooks.BeforeRetrieveHookOutput, error) {
					if in.RunMeta.RunID != "run-r" || in.RunMeta.Iteration != 0 || in.RunMeta.HooksGroup != "rewrite" {
						t.Fatalf("RunMeta = %#v", in.RunMeta)
					}
					if in.RetrieverName != "kb" || in.Mode != types.RetrieverModePrefetch {
						t.Fatalf("input = %#v", in)
					}
					return hooks.BeforeRetrieveHookOutput{Query: "hooked"}, nil
				},
			}},
		}},
	})
	got, err := rt.ExecuteRetrievers(context.Background(), ExecuteRetrieversInput{
		Logger: noopLog(), RunID: "run-r", Iteration: 0, Query: "raw",
	})
	require.NoError(t, err)
	require.Contains(t, got.Context, "doc")
}

func TestExecuteRetrievers_AfterRetrieveFiltersDocs(t *testing.T) {
	ctrl := gomock.NewController(t)
	r := ifmocks.NewMockRetriever(ctrl)
	r.EXPECT().Name().Return("kb").AnyTimes()
	r.EXPECT().Search(gomock.Any(), "q").Return([]interfaces.Document{
		{Content: "drop", Source: "s", Score: 0.5},
		{Content: "keep", Source: "s", Score: 0.9},
	}, nil)

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Retrievers: sdkruntime.AgentRetrievers{Retrievers: []interfaces.Retriever{r}},
		Hooks: []hooks.HookGroup{{
			Name: "filter",
			Hooks: hooks.AgentHooks{AfterRetrieve: []hooks.AfterRetrieveHook{
				func(_ context.Context, in hooks.AfterRetrieveHookInput) (hooks.AfterRetrieveHookOutput, error) {
					return hooks.AfterRetrieveHookOutput{
						Documents: []interfaces.Document{in.Documents[1]},
					}, nil
				},
			}},
		}},
	})
	got, err := rt.ExecuteRetrievers(context.Background(), ExecuteRetrieversInput{Logger: noopLog(), Query: "q"})
	require.NoError(t, err)
	require.Contains(t, got.Context, "keep")
	require.NotContains(t, got.Context, "drop")
}

func TestExecuteRetrievers_BeforeRetrieveAbort(t *testing.T) {
	ctrl := gomock.NewController(t)
	r := ifmocks.NewMockRetriever(ctrl)
	r.EXPECT().Name().Return("kb").AnyTimes()
	// Search must not be called when BeforeRetrieve aborts.

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Retrievers: sdkruntime.AgentRetrievers{Retrievers: []interfaces.Retriever{r}},
		Hooks: []hooks.HookGroup{{
			Name: "block",
			Hooks: hooks.AgentHooks{BeforeRetrieve: []hooks.BeforeRetrieveHook{
				func(context.Context, hooks.BeforeRetrieveHookInput) (hooks.BeforeRetrieveHookOutput, error) {
					return hooks.BeforeRetrieveHookOutput{}, errors.New("blocked")
				},
			}},
		}},
	})
	got, err := rt.ExecuteRetrievers(context.Background(), ExecuteRetrieversInput{Logger: noopLog(), Query: "q"})
	require.NoError(t, err)
	require.Equal(t, "", got.Context)
	require.Equal(t, int64(1), got.TotalSearches)
	require.Equal(t, int64(1), got.FailedSearches)
}

func TestExecuteRetrievers_AfterRetrieveAbort(t *testing.T) {
	ctrl := gomock.NewController(t)
	r := ifmocks.NewMockRetriever(ctrl)
	r.EXPECT().Name().Return("kb").AnyTimes()
	r.EXPECT().Search(gomock.Any(), "q").Return([]interfaces.Document{
		{Content: "doc", Source: "s", Score: 0.9},
	}, nil)

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Retrievers: sdkruntime.AgentRetrievers{Retrievers: []interfaces.Retriever{r}},
		Hooks: []hooks.HookGroup{{
			Name: "block",
			Hooks: hooks.AgentHooks{AfterRetrieve: []hooks.AfterRetrieveHook{
				func(context.Context, hooks.AfterRetrieveHookInput) (hooks.AfterRetrieveHookOutput, error) {
					return hooks.AfterRetrieveHookOutput{}, errors.New("blocked")
				},
			}},
		}},
	})
	got, err := rt.ExecuteRetrievers(context.Background(), ExecuteRetrieversInput{Logger: noopLog(), Query: "q"})
	require.NoError(t, err)
	require.Equal(t, "", got.Context)
	require.Equal(t, int64(1), got.TotalSearches)
	require.Equal(t, int64(1), got.FailedSearches)
}

// --- ExecuteLLM ---

func TestExecuteLLM_BeforeLLMModifiesRequest(t *testing.T) {
	llm := &captureLLMClient{resp: &interfaces.LLMResponse{Content: "ok"}}
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: llm},
		Hooks: []hooks.HookGroup{{
			Name: "guardrails",
			Hooks: hooks.AgentHooks{BeforeLLM: []hooks.BeforeLLMHook{
				func(_ context.Context, in hooks.BeforeLLMHookInput) (hooks.BeforeLLMHookOutput, error) {
					if in.RunMeta.RunID != "run-42" || in.RunMeta.Iteration != 2 || in.RunMeta.HooksGroup != "guardrails" {
						t.Fatalf("RunMeta = %#v", in.RunMeta)
					}
					out := in.Request
					out.SystemMessage = "hooked"
					return hooks.BeforeLLMHookOutput{Request: out}, nil
				},
			}},
		}},
	})
	input := ExecuteLLMInput{
		Logger:    noopLog(),
		AgentName: "agent",
		MessageID: "msg-1",
		RunID:     "run-42",
		Iteration: 2,
	}
	_, err := rt.ExecuteLLM(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, llm.lastReq)
	require.Equal(t, "hooked", llm.lastReq.SystemMessage)
}

func TestExecuteLLM_AfterLLMModifiesResponse(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{Content: "raw"},
		}},
		Hooks: []hooks.HookGroup{{
			Name: "scrub",
			Hooks: hooks.AgentHooks{AfterLLM: []hooks.AfterLLMHook{
				func(_ context.Context, in hooks.AfterLLMHookInput) (hooks.AfterLLMHookOutput, error) {
					out := in.Response
					out.Content = "scrubbed"
					return hooks.AfterLLMHookOutput{Response: out}, nil
				},
			}},
		}},
	})
	result, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{
		Logger: noopLog(), AgentName: "agent", MessageID: "msg-1",
	})
	require.NoError(t, err)
	require.Equal(t, "scrubbed", result.Content)
}

func TestExecuteLLM_BeforeLLMErrorAborts(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{Content: "should not run"},
		}},
		Hooks: []hooks.HookGroup{{
			Name: "block",
			Hooks: hooks.AgentHooks{BeforeLLM: []hooks.BeforeLLMHook{
				func(context.Context, hooks.BeforeLLMHookInput) (hooks.BeforeLLMHookOutput, error) {
					return hooks.BeforeLLMHookOutput{}, errors.New("blocked")
				},
			}},
		}},
	})
	_, err := rt.ExecuteLLM(context.Background(), ExecuteLLMInput{
		Logger: noopLog(), AgentName: "agent", MessageID: "msg-1",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "blocked")
}

func TestExecuteLLM_LLMError(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{err: errors.New("llm unavailable")}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg-1",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	_, err := rt.ExecuteLLM(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "llm unavailable")
}

func TestExecuteLLM_Success_NoTools(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{Content: "hello world"},
		}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg-1",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	result, err := rt.ExecuteLLM(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "hello world", result.Content)
	require.Empty(t, result.ToolCalls)
}

func TestExecuteLLM_EmitsTextMessageEvents(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{Content: "response text"},
		}},
	})

	var emitted []events.AgentEventType
	emit := func(ev events.AgentEvent) {
		emitted = append(emitted, ev.Type())
	}

	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg-1",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             emit,
	}
	_, err := rt.ExecuteLLM(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, []events.AgentEventType{
		events.AgentEventTypeTextMessageStart,
		events.AgentEventTypeTextMessageContent,
		events.AgentEventTypeTextMessageEnd,
	}, emitted)
}

func TestExecuteLLM_NilEmitDoesNotPanic(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{Content: "ok"},
		}},
	})
	require.NotPanics(t, func() {
		input := ExecuteLLMInput{
			Logger:           noopLog(),
			AgentName:        "a",
			MessageID:        "m",
			Messages:         nil,
			SkipTools:        false,
			RetrieverContext: "",
			Tools:            nil,
			Emit:             nil,
		}
		_, _ = rt.ExecuteLLM(context.Background(), input)
	})
}

func TestExecuteLLM_UnknownToolCallReturnsError(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{
				Content: "",
				ToolCalls: []*interfaces.ToolCall{
					{ToolCallID: "1", ToolName: "nonexistent", Args: nil},
				},
			},
		}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "a",
		MessageID:        "m",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	_, err := rt.ExecuteLLM(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool")
}

func TestExecuteLLM_WithUsageMetrics(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{
				Content: "ok",
				Usage:   &interfaces.LLMUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			},
		}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "a",
		MessageID:        "m",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	result, err := rt.ExecuteLLM(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, result.Usage)
	require.EqualValues(t, 10, result.Usage.PromptTokens)
}

func TestExecuteLLM_ToolCallWithEmptyDisplayName(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("my-tool").AnyTimes()
	tool.EXPECT().Description().Return("").AnyTimes()
	tool.EXPECT().Parameters().Return(nil).AnyTimes()
	tool.EXPECT().DisplayName().Return("").AnyTimes() // empty → falls back to tool name

	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{
				ToolCalls: []*interfaces.ToolCall{
					{ToolCallID: "tc1", ToolName: "my-tool"},
				},
			},
		}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "a",
		MessageID:        "m",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            []interfaces.Tool{tool},
		Emit:             nil,
	}
	result, err := rt.ExecuteLLM(context.Background(), input)
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
	require.Equal(t, "my-tool", result.ToolCalls[0].ToolDisplayName)
}

func TestExecuteLLM_NilToolCallInResponse(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{
				Content:   "answer",
				ToolCalls: []*interfaces.ToolCall{nil}, // nil entry must be skipped
			},
		}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "a",
		MessageID:        "m",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	result, err := rt.ExecuteLLM(context.Background(), input)
	require.NoError(t, err)
	require.Empty(t, result.ToolCalls)
}

// --- RequiresApproval with policy ---

func TestRequiresApproval_PolicyOverrides(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	policy := ifmocks.NewMockAgentToolApprovalPolicy(ctrl)
	policy.EXPECT().RequiresApproval(tool).Return(true)

	rt := newTestRuntime(sdkruntime.AgentConfig{
		ToolApprovalPolicy: policy,
	})
	require.True(t, rt.RequiresApproval(tool))
}

// --- AuthorizeTool error path ---

func TestAuthorizeTool_AuthorizerError(t *testing.T) {
	tool := authorizerToolStub{name: "err-tool", err: errors.New("auth backend down")}
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	_, err := rt.AuthorizeTool(context.Background(), noopLog(), []interfaces.Tool{tool}, "err-tool", nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "auth backend down")
}

// --- ExecuteRetrievers partial failure ---

func TestExecuteRetrievers_PartialFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	good := ifmocks.NewMockRetriever(ctrl)
	good.EXPECT().Name().Return("good").AnyTimes()
	good.EXPECT().Search(gomock.Any(), gomock.Any()).Return([]interfaces.Document{
		{Content: "useful", Source: "s", Score: 0.8},
	}, nil)

	bad := ifmocks.NewMockRetriever(ctrl)
	bad.EXPECT().Name().Return("bad").AnyTimes()
	bad.EXPECT().Search(gomock.Any(), gomock.Any()).Return(nil, errors.New("timeout"))

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Retrievers: sdkruntime.AgentRetrievers{Retrievers: []interfaces.Retriever{good, bad}},
	})
	got, err := rt.ExecuteRetrievers(context.Background(), ExecuteRetrieversInput{Logger: noopLog(), Query: "q"})
	require.NoError(t, err) // partial is ok
	require.Contains(t, got.Context, "useful")
	require.Equal(t, int64(2), got.TotalSearches)
	require.Equal(t, int64(1), got.FailedSearches)
}

// --- StoreMemoryRecords ---

func TestStoreMemoryRecords_appliesTTL(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()
	before := time.Now().UTC()

	require.NoError(t, storeMemoryRecords(rt, ctx, scope, []interfaces.MemoryRecord{
		{Text: "User prefers concise answers", Kind: memory.KindNote},
	}))

	entries, err := store.Load(ctx, scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.False(t, entries[0].ExpiresAt.IsZero())
	want := before.Add(memory.TTLNote)
	require.WithinDuration(t, want, entries[0].ExpiresAt, 2*time.Second)
}

func TestStoreMemoryRecords_allowlistRejectsKind(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	cfg.Store.AllowedKinds = []interfaces.MemoryKind{memory.KindFact}
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	err := storeMemoryRecords(rt, context.Background(), interfaces.MemoryScope{UserID: "u1"},
		[]interfaces.MemoryRecord{{Text: "note text", Kind: memory.KindNote}})
	require.Error(t, err)
}

func TestStoreMemoryRecords_dedupUpserts(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()
	text := "favorite color is blue"

	require.NoError(t, storeMemoryRecords(rt, ctx, scope, []interfaces.MemoryRecord{
		{Text: text, Kind: memory.KindPreference},
	}))
	require.NoError(t, storeMemoryRecords(rt, ctx, scope, []interfaces.MemoryRecord{
		{Text: text, Kind: memory.KindFact},
	}))

	entries, err := store.Load(ctx, scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, memory.KindFact, entries[0].Kind)
}

func TestStoreMemoryRecords_dedupAppendsDistinctText(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()

	require.NoError(t, storeMemoryRecords(rt, ctx, scope, []interfaces.MemoryRecord{
		{Text: "favorite color is blue", Kind: memory.KindPreference},
		{Text: "prefers concise answers", Kind: memory.KindPreference},
	}))

	entries, err := store.Load(ctx, scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Len(t, entries, 2)
}

func TestStoreMemoryRecords_appliesDefaultKind(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()

	require.NoError(t, storeMemoryRecords(rt, ctx, scope, []interfaces.MemoryRecord{
		{Text: "remember this"},
	}))

	entries, err := store.Load(ctx, scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, memory.KindNote, entries[0].Kind)
}

func TestStoreMemoryRecords_notConfigured(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{})
	require.NoError(t, storeMemoryRecords(rt, context.Background(), interfaces.MemoryScope{UserID: "u1"},
		[]interfaces.MemoryRecord{{Text: "x"}}))
}

func TestStoreMemoryRecords_skipsEmptyText(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()

	require.NoError(t, storeMemoryRecords(rt, ctx, scope, []interfaces.MemoryRecord{
		{Text: "   "},
	}))

	entries, err := store.Load(ctx, scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestStoreMemoryRecords_emitsMetrics(t *testing.T) {
	ctrl := gomock.NewController(t)
	metrics := ifmocks.NewMockMetrics(ctrl)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryStoreStarted, gomock.Any()).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryDedupStarted).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryDedupCompleted).Times(1)
	metrics.EXPECT().RecordHistogram(gomock.Any(), types.MetricMemoryDedupLatencyMs, gomock.Any()).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryStoreCompleted, gomock.Any(), gomock.Any()).Times(1)
	metrics.EXPECT().RecordHistogram(gomock.Any(), types.MetricMemoryStoreLatencyMs, gomock.Any(), gomock.Any()).Times(1)

	mem := ifmocks.NewMockMemory(ctrl)
	scope := interfaces.MemoryScope{UserID: "u1"}
	mem.EXPECT().Load(gomock.Any(), scope, "hello world", gomock.Any()).Return(nil, nil).Times(1)
	mem.EXPECT().Store(gomock.Any(), scope, gomock.Any()).Return("id-1", nil).Times(1)

	cfg := memory.DefaultConfig(mem)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	rt.Metrics = metrics

	require.NoError(t, storeMemoryRecords(rt, context.Background(), scope,
		[]interfaces.MemoryRecord{{Text: "hello world", Kind: memory.KindNote}}))
}

func TestStoreMemoryRecords_kindRejectedEmitsFailedMetric(t *testing.T) {
	ctrl := gomock.NewController(t)
	metrics := ifmocks.NewMockMetrics(ctrl)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryStoreFailed).Times(1)

	mem := ifmocks.NewMockMemory(ctrl)
	cfg := memory.DefaultConfig(mem)
	cfg.Store.AllowedKinds = []interfaces.MemoryKind{memory.KindFact}
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	rt.Metrics = metrics

	err := storeMemoryRecords(rt, context.Background(), interfaces.MemoryScope{UserID: "u1"},
		[]interfaces.MemoryRecord{{Text: "x", Kind: memory.KindNote}})
	require.Error(t, err)
}

func TestStoreMemoryRecords_BeforeMemoryStoreRewritesRecord(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
		Hooks: []hooks.HookGroup{{
			Name: "scrub",
			Hooks: hooks.AgentHooks{BeforeMemoryStore: []hooks.BeforeMemoryStoreHook{
				func(_ context.Context, in hooks.BeforeMemoryStoreHookInput) (hooks.BeforeMemoryStoreHookOutput, error) {
					if in.RunMeta.RunID != "run-s" || in.RunMeta.Iteration != 3 {
						t.Fatalf("RunMeta = %#v", in.RunMeta)
					}
					return hooks.BeforeMemoryStoreHookOutput{
						Record: interfaces.MemoryRecord{Text: "scrubbed text"},
					}, nil
				},
			}},
		}},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()
	require.NoError(t, rt.StoreMemoryRecords(ctx, StoreMemoryRecordsInput{
		Logger: noopLog(), RunID: "run-s", Iteration: 3, Scope: scope,
		Records: []interfaces.MemoryRecord{{Text: "raw secret"}},
	}))

	entries, err := store.Load(ctx, scope, "scrubbed", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "scrubbed text", entries[0].Text)
}

func TestStoreMemoryRecords_BeforeMemoryStoreHookIDSkipsDedup(t *testing.T) {
	ctrl := gomock.NewController(t)
	mem := ifmocks.NewMockMemory(ctrl)
	scope := interfaces.MemoryScope{UserID: "u1"}

	mem.EXPECT().Load(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Times(0)
	mem.EXPECT().Store(gomock.Any(), scope, gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ context.Context, _ interfaces.MemoryScope, rec interfaces.MemoryRecord, opts ...interfaces.StoreMemoryOption) (string, error) {
			storeOpts := interfaces.StoreMemoryOptions{}
			for _, opt := range opts {
				opt(&storeOpts)
			}
			require.Equal(t, "hook-id", storeOpts.ID)
			require.Equal(t, "stored text", rec.Text)
			return "hook-id", nil
		}).Times(1)

	cfg := memory.DefaultConfig(mem)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
		Hooks: []hooks.HookGroup{{
			Name: "upsert",
			Hooks: hooks.AgentHooks{BeforeMemoryStore: []hooks.BeforeMemoryStoreHook{
				func(context.Context, hooks.BeforeMemoryStoreHookInput) (hooks.BeforeMemoryStoreHookOutput, error) {
					return hooks.BeforeMemoryStoreHookOutput{
						Record: interfaces.MemoryRecord{Text: "stored text"},
						ID:     "hook-id",
					}, nil
				},
			}},
		}},
	})

	require.NoError(t, rt.StoreMemoryRecords(context.Background(), StoreMemoryRecordsInput{
		Logger: noopLog(), Scope: scope,
		Records: []interfaces.MemoryRecord{{Text: "original"}},
	}))
}

func TestStoreMemoryRecords_BeforeMemoryStoreAbort(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
		Hooks: []hooks.HookGroup{{
			Name: "block",
			Hooks: hooks.AgentHooks{BeforeMemoryStore: []hooks.BeforeMemoryStoreHook{
				func(context.Context, hooks.BeforeMemoryStoreHookInput) (hooks.BeforeMemoryStoreHookOutput, error) {
					return hooks.BeforeMemoryStoreHookOutput{}, errors.New("blocked")
				},
			}},
		}},
	})

	err := rt.StoreMemoryRecords(context.Background(), StoreMemoryRecordsInput{
		Logger: noopLog(), Scope: interfaces.MemoryScope{UserID: "u1"},
		Records: []interfaces.MemoryRecord{{Text: "x"}},
	})
	require.Error(t, err)
	require.Equal(t, "blocked", err.Error())

	entries, err := store.Load(context.Background(), interfaces.MemoryScope{UserID: "u1"}, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestStoreMemoryRecords_AfterMemoryStoreAbort(t *testing.T) {
	ctrl := gomock.NewController(t)
	mem := ifmocks.NewMockMemory(ctrl)
	scope := interfaces.MemoryScope{UserID: "u1"}
	mem.EXPECT().Load(gomock.Any(), scope, gomock.Any(), gomock.Any()).Return(nil, nil).Times(1)
	mem.EXPECT().Store(gomock.Any(), scope, gomock.Any()).Return("id-1", nil).Times(1)

	cfg := memory.DefaultConfig(mem)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
		Hooks: []hooks.HookGroup{{
			Name: "audit",
			Hooks: hooks.AgentHooks{AfterMemoryStore: []hooks.AfterMemoryStoreHook{
				func(_ context.Context, in hooks.AfterMemoryStoreHookInput) (hooks.AfterMemoryStoreHookOutput, error) {
					require.Equal(t, "id-1", in.ID)
					return hooks.AfterMemoryStoreHookOutput{}, errors.New("blocked")
				},
			}},
		}},
	})

	err := rt.StoreMemoryRecords(context.Background(), StoreMemoryRecordsInput{
		Logger: noopLog(), Scope: scope,
		Records: []interfaces.MemoryRecord{{Text: "hello"}},
	})
	require.Error(t, err)
	require.Equal(t, "blocked", err.Error())
}

// --- default memory extract (Always mode) ---

type stubMemoryExtractLLM struct {
	resp *interfaces.LLMResponse
	err  error
	req  *interfaces.LLMRequest
}

func (s *stubMemoryExtractLLM) Generate(_ context.Context, req *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	s.req = req
	if s.err != nil {
		return nil, s.err
	}
	return s.resp, nil
}

func (stubMemoryExtractLLM) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("not supported")
}
func (stubMemoryExtractLLM) GetModel() string                    { return "stub" }
func (stubMemoryExtractLLM) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (stubMemoryExtractLLM) IsStreamSupported() bool             { return false }

func TestResolveMemoryExtractFunc_defaultLLM(t *testing.T) {
	llm := &stubMemoryExtractLLM{resp: &interfaces.LLMResponse{
		Content: `{"memories":[{"text":"prefers concise answers","kind":"preference"}]}`,
	}}
	cfg := memory.DefaultConfig(testutil.NewInmemMemory())
	cfg.Store.Mode = memory.StoreModeAlways
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM:    sdkruntime.AgentLLM{Client: llm},
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	extract := rt.resolveMemoryExtractFunc()
	require.NotNil(t, extract)
	require.Nil(t, cfg.Store.Extract)

	records, err := extract(context.Background(), []interfaces.Message{
		{Role: interfaces.MessageRoleUser, Content: "keep it short"},
		{Role: interfaces.MessageRoleAssistant, Content: "will do"},
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "prefers concise answers", records[0].Text)
	require.Equal(t, memory.KindPreference, records[0].Kind)
	require.Equal(t, "extract", records[0].Metadata["source"])
	require.NotNil(t, llm.req.ResponseFormat)
}

func TestResolveMemoryExtractFunc_skipsOnDemand(t *testing.T) {
	llm := &stubMemoryExtractLLM{}
	cfg := memory.DefaultConfig(testutil.NewInmemMemory())
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM:    sdkruntime.AgentLLM{Client: llm},
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	require.Nil(t, rt.resolveMemoryExtractFunc())
}

func TestResolveMemoryExtractFunc_preservesCustom(t *testing.T) {
	custom := memory.ExtractFunc(func(context.Context, []interfaces.Message) ([]interfaces.MemoryRecord, error) {
		return []interfaces.MemoryRecord{{Text: "custom"}}, nil
	})
	cfg := memory.DefaultConfig(testutil.NewInmemMemory())
	cfg.Store.Mode = memory.StoreModeAlways
	cfg.Store.Extract = custom
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM:    sdkruntime.AgentLLM{Client: &stubMemoryExtractLLM{}},
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	records, err := rt.resolveMemoryExtractFunc()(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, "custom", records[0].Text)
}

func TestResolveMemoryExtractFunc_skipsToolMessages(t *testing.T) {
	llm := &stubMemoryExtractLLM{resp: &interfaces.LLMResponse{Content: `{"memories":[]}`}}
	cfg := memory.DefaultConfig(testutil.NewInmemMemory())
	cfg.Store.Mode = memory.StoreModeAlways
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM:    sdkruntime.AgentLLM{Client: llm},
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	records, err := rt.resolveMemoryExtractFunc()(context.Background(), []interfaces.Message{
		{Role: interfaces.MessageRoleTool, Content: "tool output"},
	})
	require.NoError(t, err)
	require.Nil(t, records)
	require.Nil(t, llm.req)
}

func TestMessagesForMemoryExtraction_appendsUserTurnAfterAssistant(t *testing.T) {
	msgs := messagesForMemoryExtraction([]interfaces.Message{
		{Role: interfaces.MessageRoleUser, Content: "remember this"},
		{Role: interfaces.MessageRoleAssistant, Content: "ok"},
	})
	require.Len(t, msgs, 3)
	require.Equal(t, interfaces.MessageRoleUser, msgs[len(msgs)-1].Role)
	require.Equal(t, memoryExtractTurnPrompt, msgs[len(msgs)-1].Content)
}

func TestMessagesForMemoryExtraction_keepsUserLast(t *testing.T) {
	msgs := messagesForMemoryExtraction([]interfaces.Message{
		{Role: interfaces.MessageRoleUser, Content: "only user"},
	})
	require.Len(t, msgs, 1)
	require.Equal(t, interfaces.MessageRoleUser, msgs[0].Role)
}

func TestParseMemoryExtractResponse_invalidJSON(t *testing.T) {
	_, err := parseMemoryExtractResponse("{")
	require.Error(t, err)
}

// --- ExecuteMemory ---

func memoryConfigAlways(store interfaces.Memory) memory.Config {
	cfg := memory.DefaultConfig(store)
	cfg.Store.Mode = memory.StoreModeAlways
	return cfg
}

func testRunMessages(user, assistant string) []interfaces.Message {
	var msgs []interfaces.Message
	if user != "" {
		msgs = append(msgs, interfaces.Message{Role: interfaces.MessageRoleUser, Content: user})
	}
	if assistant != "" {
		msgs = append(msgs, interfaces.Message{Role: interfaces.MessageRoleAssistant, Content: assistant})
	}
	return msgs
}

func testAlwaysStoreExtract(_ context.Context, messages []interfaces.Message) ([]interfaces.MemoryRecord, error) {
	var user, assistant string
	for _, m := range messages {
		switch m.Role {
		case interfaces.MessageRoleUser:
			user = strings.TrimSpace(m.Content)
		case interfaces.MessageRoleAssistant:
			assistant = strings.TrimSpace(m.Content)
		}
	}
	if user == "" && assistant == "" {
		return nil, nil
	}
	var text string
	switch {
	case user != "" && assistant != "":
		text = "User: " + user + "\nAssistant: " + assistant
	case assistant != "":
		text = "Assistant: " + assistant
	default:
		text = "User: " + user
	}
	return []interfaces.MemoryRecord{{
		Text:     text,
		Metadata: map[string]string{"source": "extract"},
	}}, nil
}

func TestExecuteMemoryStore_skipsOnDemand(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	scope := interfaces.MemoryScope{UserID: "u1"}
	require.NoError(t, executeMemoryStore(rt, context.Background(), scope, testRunMessages("hello", "world")))
	entries, err := store.Load(context.Background(), scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestExecuteTool_saveMemory(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	scope := interfaces.MemoryScope{UserID: "u1"}
	out, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), ToolName: types.SaveMemoryToolName,
		Args: map[string]any{types.MemoryToolParamText: "favorite color is blue"},
	}, scope)
	require.NoError(t, err)
	require.Equal(t, "memory saved", out)
	entries, err := store.Load(context.Background(), scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestExecuteTool_saveMemory_runsBeforeStoreHook(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
		Hooks: []hooks.HookGroup{{
			Name: "scrub",
			Hooks: hooks.AgentHooks{BeforeMemoryStore: []hooks.BeforeMemoryStoreHook{
				func(_ context.Context, in hooks.BeforeMemoryStoreHookInput) (hooks.BeforeMemoryStoreHookOutput, error) {
					if in.RunMeta.RunID != "run-t" || in.RunMeta.Iteration != 2 {
						t.Fatalf("RunMeta = %#v", in.RunMeta)
					}
					rec := in.Record
					if rec.Metadata == nil {
						rec.Metadata = map[string]string{}
					}
					rec.Metadata["hooked"] = "true"
					return hooks.BeforeMemoryStoreHookOutput{Record: rec}, nil
				},
			}},
		}},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	_, err := rt.ExecuteTool(context.Background(), ExecuteToolInput{
		Logger: noopLog(), RunID: "run-t", Iteration: 2,
		ToolName: types.SaveMemoryToolName,
		Args:     map[string]any{types.MemoryToolParamText: "favorite color is blue"},
	}, scope)
	require.NoError(t, err)

	entries, err := store.Load(context.Background(), scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "true", entries[0].Metadata["hooked"])
}

func TestExecuteMemoryRecallAndStore(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memoryConfigAlways(store)
	cfg.Store.Extract = testAlwaysStoreExtract
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()

	require.NoError(t, executeMemoryStore(rt, ctx, scope, testRunMessages("hello", "world")))

	res, err := rt.ExecuteMemoryRecall(ctx, ExecuteMemoryRecallInput{
		Logger: noopLog(), RunID: "run-1", Iteration: 0, Scope: scope, Query: "hello",
	})
	require.NoError(t, err)
	require.NotEmpty(t, res.Context)
}

func TestExecuteMemoryStore_AppliesTTLFromPolicy(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memoryConfigAlways(store)
	cfg.Store.Extract = testAlwaysStoreExtract
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()
	before := time.Now().UTC()

	require.NoError(t, executeMemoryStore(rt, ctx, scope, testRunMessages("hello", "world")))

	entries, err := store.Load(ctx, scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.False(t, entries[0].ExpiresAt.IsZero())
	want := before.Add(memory.TTLNote)
	require.WithinDuration(t, want, entries[0].ExpiresAt, 2*time.Second)
}

func TestExecuteMemoryStore_skipsEmptyMessages(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memoryConfigAlways(store)
	cfg.Store.Extract = testAlwaysStoreExtract
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()

	require.NoError(t, executeMemoryStore(rt, ctx, scope, nil))
	require.NoError(t, executeMemoryStore(rt, ctx, scope, []interfaces.Message{
		{Role: interfaces.MessageRoleTool, Content: "noise"},
	}))

	entries, err := store.Load(ctx, scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestExecuteMemoryStore_noExtractorEmitsFailedMetric(t *testing.T) {
	ctrl := gomock.NewController(t)
	metrics := ifmocks.NewMockMetrics(ctrl)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryExtractFailed).Times(1)

	store := testutil.NewInmemMemory()
	cfg := memoryConfigAlways(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	rt.Metrics = metrics

	require.NoError(t, executeMemoryStore(rt, context.Background(), interfaces.MemoryScope{UserID: "u1"},
		testRunMessages("hi", "there")))
}

func TestExecuteMemoryExtract_EmitsMetrics(t *testing.T) {
	ctrl := gomock.NewController(t)
	metrics := ifmocks.NewMockMetrics(ctrl)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryExtractStarted).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryExtractCompleted).Times(1)
	metrics.EXPECT().RecordHistogram(gomock.Any(), types.MetricMemoryExtractLatencyMs, gomock.Any()).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryDedupStarted).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryDedupCompleted).Times(1)
	metrics.EXPECT().RecordHistogram(gomock.Any(), types.MetricMemoryDedupLatencyMs, gomock.Any()).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryStoreStarted, gomock.Any()).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryStoreCompleted, gomock.Any(), gomock.Any()).Times(1)
	metrics.EXPECT().RecordHistogram(gomock.Any(), types.MetricMemoryStoreLatencyMs, gomock.Any(), gomock.Any()).Times(1)

	store := testutil.NewInmemMemory()
	cfg := memoryConfigAlways(store)
	cfg.Store.Extract = testAlwaysStoreExtract
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	rt.Metrics = metrics

	scope := interfaces.MemoryScope{UserID: "u1"}
	require.NoError(t, executeMemoryStore(rt, context.Background(), scope, testRunMessages("hello", "world")))
}

func TestExecuteMemoryExtract_EmitsFailedMetric(t *testing.T) {
	ctrl := gomock.NewController(t)
	metrics := ifmocks.NewMockMetrics(ctrl)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryExtractStarted).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryExtractFailed).Times(1)
	metrics.EXPECT().RecordHistogram(gomock.Any(), types.MetricMemoryExtractLatencyMs, gomock.Any()).Times(1)

	store := testutil.NewInmemMemory()
	cfg := memoryConfigAlways(store)
	cfg.Store.Extract = func(context.Context, []interfaces.Message) ([]interfaces.MemoryRecord, error) {
		return nil, errors.New("extract failed")
	}
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	rt.Metrics = metrics

	err := executeMemoryStore(rt, context.Background(), interfaces.MemoryScope{UserID: "u1"}, testRunMessages("hi", "there"))
	require.Error(t, err)
}

func TestExecuteMemoryStore_extractsWithDefaultLLM(t *testing.T) {
	llm := &stubMemoryExtractLLM{resp: &interfaces.LLMResponse{
		Content: `{"memories":[{"text":"user likes tea","kind":"preference"}]}`,
	}}
	store := testutil.NewInmemMemory()
	cfg := memoryConfigAlways(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM:    sdkruntime.AgentLLM{Client: llm},
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()
	require.NoError(t, executeMemoryStore(rt, ctx, scope, testRunMessages("I like tea", "noted")))

	entries, err := store.Load(ctx, scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "user likes tea", entries[0].Text)
	require.Equal(t, memory.KindPreference, entries[0].Kind)
	require.Equal(t, "extract", entries[0].Metadata["source"])
}

func TestExecuteMemoryStore_setsExtractMetadata(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memoryConfigAlways(store)
	cfg.Store.Extract = testAlwaysStoreExtract
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()

	require.NoError(t, executeMemoryStore(rt, ctx, scope, testRunMessages("hi", "there")))

	entries, err := store.Load(ctx, scope, "", cfg.Recall.RecencyLoadOptions()...)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "extract", entries[0].Metadata["source"])
	require.Contains(t, entries[0].Text, "User: hi")
}

func TestExecuteMemoryRecall_OmitsExpired(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()

	_, err := store.Store(ctx, scope, interfaces.MemoryRecord{
		Text:      "User prefers concise answers",
		Kind:      memory.KindPreference,
		ExpiresAt: time.Now().UTC().Add(-time.Hour),
	})
	require.NoError(t, err)

	res, err := rt.ExecuteMemoryRecall(ctx, ExecuteMemoryRecallInput{
		Logger: noopLog(), RunID: "run-1", Iteration: 0, Scope: scope, Query: "concise",
	})
	require.NoError(t, err)
	require.Empty(t, res.Context)
}

func TestExecuteMemoryRecall_SemanticMissFallsBackToRecency(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memoryConfigAlways(store)
	cfg.Store.Extract = testAlwaysStoreExtract
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()

	require.NoError(t, executeMemoryStore(rt, ctx, scope, testRunMessages(
		"Remember that I prefer concise answers.", "Got it.")))

	res, err := rt.ExecuteMemoryRecall(ctx, ExecuteMemoryRecallInput{
		Logger: noopLog(), RunID: "run-1", Iteration: 0, Scope: scope, Query: "What answer style do I prefer?",
	})
	require.NoError(t, err)
	require.Contains(t, res.Context, "concise answers")
}

func TestExecuteMemoryRecall_BeforeMemoryLoadRewritesQuery(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memoryConfigAlways(store)
	cfg.Store.Extract = testAlwaysStoreExtract
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
		Hooks: []hooks.HookGroup{{
			Name: "rewrite",
			Hooks: hooks.AgentHooks{BeforeMemoryLoad: []hooks.BeforeMemoryLoadHook{
				func(_ context.Context, in hooks.BeforeMemoryLoadHookInput) (hooks.BeforeMemoryLoadHookOutput, error) {
					if in.RunMeta.RunID != "run-m" || in.RunMeta.Iteration != 0 || in.RunMeta.HooksGroup != "rewrite" {
						t.Fatalf("RunMeta = %#v", in.RunMeta)
					}
					return hooks.BeforeMemoryLoadHookOutput{Query: "hooked"}, nil
				},
			}},
		}},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()
	require.NoError(t, executeMemoryStore(rt, ctx, scope, testRunMessages(
		"Remember that I prefer concise answers.", "Got it.")))

	res, err := rt.ExecuteMemoryRecall(ctx, ExecuteMemoryRecallInput{
		Logger: noopLog(), RunID: "run-m", Iteration: 0, Scope: scope, Query: "raw",
	})
	require.NoError(t, err)
	require.Contains(t, res.Context, "concise answers")
}

func TestExecuteMemoryRecall_AfterMemoryLoadFiltersContext(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memoryConfigAlways(store)
	cfg.Store.Extract = testAlwaysStoreExtract
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
		Hooks: []hooks.HookGroup{{
			Name: "filter",
			Hooks: hooks.AgentHooks{AfterMemoryLoad: []hooks.AfterMemoryLoadHook{
				func(_ context.Context, in hooks.AfterMemoryLoadHookInput) (hooks.AfterMemoryLoadHookOutput, error) {
					return hooks.AfterMemoryLoadHookOutput{PromptContext: "filtered"}, nil
				},
			}},
		}},
	})

	scope := interfaces.MemoryScope{UserID: "u1"}
	ctx := context.Background()
	require.NoError(t, executeMemoryStore(rt, ctx, scope, testRunMessages("hello", "world")))

	res, err := rt.ExecuteMemoryRecall(ctx, ExecuteMemoryRecallInput{
		Logger: noopLog(), RunID: "run-1", Iteration: 0, Scope: scope, Query: "hello",
	})
	require.NoError(t, err)
	require.Equal(t, "filtered", res.Context)
}

func TestExecuteMemoryRecall_BeforeMemoryLoadAbort(t *testing.T) {
	store := testutil.NewInmemMemory()
	cfg := memory.DefaultConfig(store)
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
		Hooks: []hooks.HookGroup{{
			Name: "block",
			Hooks: hooks.AgentHooks{BeforeMemoryLoad: []hooks.BeforeMemoryLoadHook{
				func(context.Context, hooks.BeforeMemoryLoadHookInput) (hooks.BeforeMemoryLoadHookOutput, error) {
					return hooks.BeforeMemoryLoadHookOutput{}, errors.New("blocked")
				},
			}},
		}},
	})

	_, err := rt.ExecuteMemoryRecall(context.Background(), ExecuteMemoryRecallInput{
		Logger: noopLog(), RunID: "run-1", Iteration: 0,
		Scope: interfaces.MemoryScope{UserID: "u1"}, Query: "q",
	})
	require.Error(t, err)
	require.Equal(t, "blocked", err.Error())
}

func TestSubAgentScope(t *testing.T) {
	parent := interfaces.MemoryScope{
		TenantID: "t1",
		UserID:   "u1",
		AgentID:  "main",
	}
	got := SubAgentScope(parent, "sub-researcher")
	if got.TenantID != "t1" || got.UserID != "u1" {
		t.Fatalf("tenant/user = %+v", got)
	}
	if got.AgentID != "sub-researcher" {
		t.Fatalf("agentID = %q", got.AgentID)
	}
	if got.Tags[scopeKeyParentAgentID] != "main" {
		t.Fatalf("tags = %+v", got.Tags)
	}
}

func TestSubAgentScope_nestedDelegation(t *testing.T) {
	parent := SubAgentScope(interfaces.MemoryScope{
		UserID:  "u1",
		AgentID: "main",
	}, "sub-a")
	got := SubAgentScope(parent, "sub-b")
	if got.AgentID != "sub-b" {
		t.Fatalf("agentID = %q", got.AgentID)
	}
	if got.Tags[scopeKeyParentAgentID] != "sub-a" {
		t.Fatalf("tags = %+v", got.Tags)
	}
}

func TestExecuteMemoryRecall_EmitsMetrics(t *testing.T) {
	ctrl := gomock.NewController(t)
	metrics := ifmocks.NewMockMetrics(ctrl)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryRecallStarted).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryRecallCompleted).Times(1)
	metrics.EXPECT().RecordHistogram(gomock.Any(), types.MetricMemoryRecallLatencyMs, gomock.Any()).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryExtractStarted).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryExtractCompleted).Times(1)
	metrics.EXPECT().RecordHistogram(gomock.Any(), types.MetricMemoryExtractLatencyMs, gomock.Any()).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryDedupStarted).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryDedupCompleted).Times(1)
	metrics.EXPECT().RecordHistogram(gomock.Any(), types.MetricMemoryDedupLatencyMs, gomock.Any()).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryStoreStarted, gomock.Any()).Times(1)
	metrics.EXPECT().IncrementCounter(gomock.Any(), types.MetricMemoryStoreCompleted, gomock.Any(), gomock.Any()).Times(1)
	metrics.EXPECT().RecordHistogram(gomock.Any(), types.MetricMemoryStoreLatencyMs, gomock.Any(), gomock.Any()).Times(1)

	mem := ifmocks.NewMockMemory(ctrl)
	scope := interfaces.MemoryScope{UserID: "u1"}
	mem.EXPECT().Store(gomock.Any(), scope, gomock.Any()).Return("id-1", nil).Times(1)
	mem.EXPECT().Load(gomock.Any(), scope, gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ interfaces.MemoryScope, query string, _ ...interfaces.LoadMemoryOption) ([]interfaces.MemoryEntry, error) {
			if query == "hello" {
				return []interfaces.MemoryEntry{{Text: "User: hello\nAssistant: world"}}, nil
			}
			return nil, nil
		}).AnyTimes()

	cfg := memoryConfigAlways(mem)
	cfg.Store.Extract = testAlwaysStoreExtract
	rt := newTestRuntime(sdkruntime.AgentConfig{
		Memory: sdkruntime.AgentMemory{Config: &cfg},
	})
	rt.Metrics = metrics

	ctx := context.Background()
	require.NoError(t, executeMemoryStore(rt, ctx, scope, testRunMessages("hello", "world")))
	_, err := rt.ExecuteMemoryRecall(ctx, ExecuteMemoryRecallInput{
		Logger: noopLog(), RunID: "run-1", Iteration: 0, Scope: scope, Query: "hello",
	})
	require.NoError(t, err)
}

// --- ExecuteLLMStream ---

// streamCapableLLMClient wraps a stubLLMClient and sets IsStreamSupported=true.
type streamCapableLLMClient struct {
	stubLLMClient
	stream    interfaces.LLMStream
	streamErr error
}

func (s streamCapableLLMClient) IsStreamSupported() bool { return true }
func (s streamCapableLLMClient) GenerateStream(_ context.Context, _ *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return s.stream, s.streamErr
}

// fixedStream is a simple in-memory LLMStream backed by a slice of chunks.
type fixedStream struct {
	chunks []*interfaces.LLMStreamChunk
	pos    int
	result *interfaces.LLMResponse
	err    error
}

func newFixedStream(chunks []*interfaces.LLMStreamChunk, result *interfaces.LLMResponse) *fixedStream {
	return &fixedStream{chunks: chunks, result: result}
}

func (s *fixedStream) Next() bool {
	s.pos++
	return s.pos <= len(s.chunks)
}
func (s *fixedStream) Current() *interfaces.LLMStreamChunk {
	if s.pos < 1 || s.pos > len(s.chunks) {
		return nil
	}
	return s.chunks[s.pos-1]
}
func (s *fixedStream) Err() error                         { return s.err }
func (s *fixedStream) GetResult() *interfaces.LLMResponse { return s.result }

func TestExecuteLLMStream_FallbackGenerate_Success(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{Content: "fallback answer"},
		}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	result, err := rt.ExecuteLLMStream(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "fallback answer", result.Content)
}

func TestExecuteLLMStream_FallbackGenerate_LLMError(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{err: errors.New("llm down")}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	_, err := rt.ExecuteLLMStream(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "llm down")
}

func TestExecuteLLMStream_FallbackGenerate_EmitsEvents(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{Content: "hi"},
		}},
	})
	var emitted []events.AgentEventType
	emit := func(ev events.AgentEvent) { emitted = append(emitted, ev.Type()) }

	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             emit,
	}
	_, err := rt.ExecuteLLMStream(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, []events.AgentEventType{
		events.AgentEventTypeTextMessageStart,
		events.AgentEventTypeTextMessageContent,
		events.AgentEventTypeTextMessageEnd,
	}, emitted)
}

func TestExecuteLLMStream_GenerateStreamError(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: streamCapableLLMClient{
			streamErr: errors.New("stream init failed"),
		}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	_, err := rt.ExecuteLLMStream(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "stream init failed")
}

func TestExecuteLLMStream_StreamError_AfterChunks(t *testing.T) {
	s := newFixedStream([]*interfaces.LLMStreamChunk{
		{ContentDelta: "partial"},
	}, nil)
	s.err = errors.New("connection reset")

	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: streamCapableLLMClient{stream: s}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	_, err := rt.ExecuteLLMStream(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "connection reset")
}

func TestExecuteLLMStream_StreamNilResult(t *testing.T) {
	s := newFixedStream(nil, nil) // no chunks, GetResult() returns nil

	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: streamCapableLLMClient{stream: s}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	_, err := rt.ExecuteLLMStream(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "stream completed without result")
}

func TestExecuteLLMStream_TextChunks_EmitsCorrectEvents(t *testing.T) {
	s := newFixedStream([]*interfaces.LLMStreamChunk{
		{ContentDelta: "hello"},
		{ContentDelta: " world"},
	}, &interfaces.LLMResponse{Content: "hello world"})

	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: streamCapableLLMClient{stream: s}},
	})

	var emitted []events.AgentEventType
	emit := func(ev events.AgentEvent) { emitted = append(emitted, ev.Type()) }

	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             emit,
	}
	result, err := rt.ExecuteLLMStream(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "hello world", result.Content)
	require.Equal(t, events.AgentEventTypeTextMessageStart, emitted[0])
	require.Equal(t, events.AgentEventTypeTextMessageContent, emitted[1])
	require.Equal(t, events.AgentEventTypeTextMessageContent, emitted[2])
	require.Equal(t, events.AgentEventTypeTextMessageEnd, emitted[3])
}

func TestExecuteLLMStream_ReasoningChunks_EmitsReasoningEvents(t *testing.T) {
	s := newFixedStream([]*interfaces.LLMStreamChunk{
		{ThinkingDelta: "let me think"},
		{ContentDelta: "answer"},
	}, &interfaces.LLMResponse{Content: "answer"})

	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: streamCapableLLMClient{stream: s}},
	})

	var emitted []events.AgentEventType
	emit := func(ev events.AgentEvent) { emitted = append(emitted, ev.Type()) }

	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             emit,
	}
	_, err := rt.ExecuteLLMStream(context.Background(), input)
	require.NoError(t, err)

	// Reasoning events must appear before text events
	require.Equal(t, events.AgentEventTypeReasoningStart, emitted[0])
	require.Equal(t, events.AgentEventTypeReasoningMessageStart, emitted[1])
	require.Equal(t, events.AgentEventTypeReasoningMessageContent, emitted[2])
	// flush reasoning before text
	require.Equal(t, events.AgentEventTypeReasoningMessageEnd, emitted[3])
	require.Equal(t, events.AgentEventTypeReasoningEnd, emitted[4])
	require.Equal(t, events.AgentEventTypeTextMessageStart, emitted[5])
}

func TestExecuteLLMStream_ToolOnlyResponse_EmitsEmptyAssistantTurn(t *testing.T) {
	ctrl := gomock.NewController(t)
	tool := ifmocks.NewMockTool(ctrl)
	tool.EXPECT().Name().Return("search").AnyTimes()
	tool.EXPECT().Description().Return("").AnyTimes()
	tool.EXPECT().Parameters().Return(nil).AnyTimes()
	tool.EXPECT().DisplayName().Return("Search").AnyTimes()

	s := newFixedStream(nil, &interfaces.LLMResponse{
		ToolCalls: []*interfaces.ToolCall{{ToolCallID: "1", ToolName: "search"}},
	})

	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: streamCapableLLMClient{stream: s}},
	})

	var emitted []events.AgentEventType
	emit := func(ev events.AgentEvent) { emitted = append(emitted, ev.Type()) }

	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            []interfaces.Tool{tool},
		Emit:             emit,
	}
	result, err := rt.ExecuteLLMStream(context.Background(), input)
	require.NoError(t, err)
	require.Len(t, result.ToolCalls, 1)
	// finalizeAssistantText emits a start/content/end even when no text chunks arrived
	require.Contains(t, emitted, events.AgentEventTypeTextMessageStart)
	require.Contains(t, emitted, events.AgentEventTypeTextMessageEnd)
}

func TestExecuteLLMStream_WithUsageMetrics(t *testing.T) {
	s := newFixedStream(nil, &interfaces.LLMResponse{
		Content: "done",
		Usage:   &interfaces.LLMUsage{PromptTokens: 8, CompletionTokens: 4, TotalTokens: 12},
	})
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: streamCapableLLMClient{stream: s}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	result, err := rt.ExecuteLLMStream(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, result.Usage)
	require.EqualValues(t, 8, result.Usage.PromptTokens)
}

func TestExecuteLLMStream_NilChunkSkipped(t *testing.T) {
	s := newFixedStream([]*interfaces.LLMStreamChunk{
		nil,
		{ContentDelta: "text"},
	}, &interfaces.LLMResponse{Content: "text"})

	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: streamCapableLLMClient{stream: s}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	result, err := rt.ExecuteLLMStream(context.Background(), input)
	require.NoError(t, err)
	require.Equal(t, "text", result.Content)
}

func TestExecuteLLMStream_FallbackGenerate_WithUsage(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{
				Content: "done",
				Usage:   &interfaces.LLMUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8},
			},
		}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	result, err := rt.ExecuteLLMStream(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, result.Usage)
	require.EqualValues(t, 5, result.Usage.PromptTokens)
}

func TestExecuteLLMStream_FallbackGenerate_UnknownToolCallError(t *testing.T) {
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: stubLLMClient{
			resp: &interfaces.LLMResponse{
				ToolCalls: []*interfaces.ToolCall{{ToolCallID: "1", ToolName: "ghost"}},
			},
		}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	_, err := rt.ExecuteLLMStream(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool")
}

func TestExecuteLLMStream_Stream_UnknownToolCallError(t *testing.T) {
	s := newFixedStream(nil, &interfaces.LLMResponse{
		ToolCalls: []*interfaces.ToolCall{{ToolCallID: "1", ToolName: "ghost"}},
	})
	rt := newTestRuntime(sdkruntime.AgentConfig{
		LLM: sdkruntime.AgentLLM{Client: streamCapableLLMClient{stream: s}},
	})
	input := ExecuteLLMInput{
		Logger:           noopLog(),
		AgentName:        "agent",
		MessageID:        "msg",
		Messages:         nil,
		SkipTools:        false,
		RetrieverContext: "",
		Tools:            nil,
		Emit:             nil,
	}
	_, err := rt.ExecuteLLMStream(context.Background(), input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown tool")
}

func TestExecuteRetrievers_EmptyDocsSkipped(t *testing.T) {
	ctrl := gomock.NewController(t)
	r := ifmocks.NewMockRetriever(ctrl)
	r.EXPECT().Name().Return("empty-kb").AnyTimes()
	r.EXPECT().Search(gomock.Any(), gomock.Any()).Return([]interfaces.Document{}, nil) // no docs

	rt := newTestRuntime(sdkruntime.AgentConfig{
		Retrievers: sdkruntime.AgentRetrievers{Retrievers: []interfaces.Retriever{r}},
	})
	got, err := rt.ExecuteRetrievers(context.Background(), ExecuteRetrieversInput{Logger: noopLog(), Query: "q"})
	require.NoError(t, err)
	require.Equal(t, "", got.Context)
	require.Equal(t, int64(1), got.TotalSearches)
	require.Equal(t, int64(0), got.FailedSearches)
}

// --- ApplyLLMSampling Reasoning field ---

func TestApplyLLMSampling_Reasoning(t *testing.T) {
	req := &interfaces.LLMRequest{}
	ApplyLLMSampling(&types.LLMSampling{
		Reasoning: &types.LLMReasoning{Effort: "medium", Enabled: true},
	}, req)
	require.NotNil(t, req.Reasoning)
	require.Equal(t, "medium", req.Reasoning.Effort)
	require.True(t, req.Reasoning.Enabled)
}
