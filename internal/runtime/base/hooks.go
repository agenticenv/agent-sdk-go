package base

import (
	"context"

	"github.com/agenticenv/agent-sdk-go/internal/hooks"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/guardrail"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func hookRunMeta(runID string, iteration int) hooks.RunMeta {
	return hooks.RunMeta{
		RunID:     runID,
		Iteration: iteration,
	}
}

func (rt *Runtime) runBeforeLLMRequest(ctx context.Context, input ExecuteLLMInput, req *interfaces.LLMRequest) error {
	if req == nil || len(rt.AgentConfig.Hooks) == 0 {
		return nil
	}
	hooked, err := hooks.RunBeforeLLM(ctx, rt.AgentConfig.Hooks, hookRunMeta(input.RunID, input.Iteration), *req)
	if err != nil {
		return err
	}
	*req = hooked
	return nil
}

func (rt *Runtime) runAfterLLMResponse(ctx context.Context, input ExecuteLLMInput, resp *interfaces.LLMResponse) error {
	if resp == nil {
		return nil
	}
	if len(rt.AgentConfig.Hooks) > 0 {
		hooked, err := hooks.RunAfterLLM(ctx, rt.AgentConfig.Hooks, hookRunMeta(input.RunID, input.Iteration), *resp)
		if err != nil {
			return err
		}
		*resp = hooked
	}
	if !rt.HasGuardrails(guardrail.StageOutput) {
		return nil
	}
	if len(resp.ToolCalls) > 0 && resp.Content == "" {
		return nil
	}
	text, err := rt.evaluateGuardrails(ctx, input.Logger, guardrail.Input{
		Stage:     guardrail.StageOutput,
		Text:      resp.Content,
		Final:     len(resp.ToolCalls) == 0,
		AgentName: input.AgentName,
		RunID:     input.RunID,
		Iteration: input.Iteration,
	})
	if err != nil {
		return err
	}
	resp.Content = text
	return nil
}

func (rt *Runtime) toolCallForHooks(input ExecuteToolInput, tool interfaces.Tool) hooks.ToolCall {
	displayName := tool.DisplayName()
	if displayName == "" {
		displayName = input.ToolName
	}
	return hooks.ToolCall{
		ID:          input.ToolCallID,
		Name:        input.ToolName,
		DisplayName: displayName,
		Kind:        types.KindOf(tool),
		Args:        input.Args,
	}
}

func (rt *Runtime) runBeforeToolHooks(ctx context.Context, input ExecuteToolInput, tool interfaces.Tool) (hooks.ToolCall, error) {
	call := rt.toolCallForHooks(input, tool)
	if len(rt.AgentConfig.Hooks) == 0 {
		return call, nil
	}
	return hooks.RunBeforeTool(ctx, rt.AgentConfig.Hooks, hookRunMeta(input.RunID, input.Iteration), call)
}

func (rt *Runtime) runAfterToolHooks(ctx context.Context, input ExecuteToolInput, call hooks.ToolCall, content string, execErr error) (string, error, error) {
	if len(rt.AgentConfig.Hooks) == 0 {
		return content, execErr, nil
	}
	return hooks.RunAfterTool(ctx, rt.AgentConfig.Hooks, hookRunMeta(input.RunID, input.Iteration), call, content, execErr)
}

func (rt *Runtime) runBeforeRetrieveHooks(ctx context.Context, input ExecuteRetrieversInput, mode types.RetrieverMode, retrieverName string) (hooks.RetrieveCall, error) {
	call := hooks.RetrieveCall{
		Query:         input.Query,
		Mode:          mode,
		RetrieverName: retrieverName,
	}
	if len(rt.AgentConfig.Hooks) == 0 {
		return call, nil
	}
	return hooks.RunBeforeRetrieve(ctx, rt.AgentConfig.Hooks, hookRunMeta(input.RunID, input.Iteration), call)
}

func (rt *Runtime) runAfterRetrieveHooks(ctx context.Context, input ExecuteRetrieversInput, call hooks.RetrieveCall, docs []interfaces.Document) ([]interfaces.Document, error) {
	if len(rt.AgentConfig.Hooks) == 0 {
		return docs, nil
	}
	return hooks.RunAfterRetrieve(ctx, rt.AgentConfig.Hooks, hookRunMeta(input.RunID, input.Iteration), call, docs)
}

func (rt *Runtime) runBeforeMemoryLoadHooks(ctx context.Context, input ExecuteMemoryRecallInput, call hooks.MemoryLoadCall) (hooks.MemoryLoadCall, error) {
	if len(rt.AgentConfig.Hooks) == 0 {
		return call, nil
	}
	return hooks.RunBeforeMemoryLoad(ctx, rt.AgentConfig.Hooks, hookRunMeta(input.RunID, input.Iteration), call)
}

func (rt *Runtime) runAfterMemoryLoadHooks(ctx context.Context, input ExecuteMemoryRecallInput, call hooks.MemoryLoadCall, promptContext string) (string, error) {
	if len(rt.AgentConfig.Hooks) == 0 {
		return promptContext, nil
	}
	return hooks.RunAfterMemoryLoad(ctx, rt.AgentConfig.Hooks, hookRunMeta(input.RunID, input.Iteration), call, promptContext)
}

func (rt *Runtime) runBeforeMemoryStoreHooks(ctx context.Context, input StoreMemoryRecordsInput, call hooks.MemoryStoreCall) (hooks.MemoryStoreCall, error) {
	if len(rt.AgentConfig.Hooks) == 0 {
		return call, nil
	}
	return hooks.RunBeforeMemoryStore(ctx, rt.AgentConfig.Hooks, hookRunMeta(input.RunID, input.Iteration), call)
}

func (rt *Runtime) runAfterMemoryStoreHooks(ctx context.Context, input StoreMemoryRecordsInput, call hooks.MemoryStoreCall) error {
	if len(rt.AgentConfig.Hooks) == 0 {
		return nil
	}
	return hooks.RunAfterMemoryStore(ctx, rt.AgentConfig.Hooks, hookRunMeta(input.RunID, input.Iteration), call)
}
