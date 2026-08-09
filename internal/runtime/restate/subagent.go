package restate

import (
	"fmt"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/base"
	"github.com/google/uuid"
	restatesdk "github.com/restatedev/sdk-go"
)

// SubAgentRoute is the JSON-safe delegation metadata passed in AgentLoopRequest.
// ServiceName is the child's Restate AgentLoop service (AgentLoop_<name>), analogous
// to Temporal's child task queue — each sub-agent is an independent Restate agent.
type SubAgentRoute struct {
	Name        string                   `json:"name"`
	ToolName    string                   `json:"tool_name"`
	ServiceName string                   `json:"service_name,omitempty"`
	ChildRoutes map[string]SubAgentRoute `json:"child_routes,omitempty"`
}

// buildSubAgentRoutes converts a SubAgentSpec tree into the JSON-serializable SubAgentRoute map.
// Skips specs whose Runtime is not a *RestateRuntime.
func buildSubAgentRoutes(specs []*sdkruntime.SubAgentSpec) map[string]SubAgentRoute {
	if len(specs) == 0 {
		return nil
	}
	out := make(map[string]SubAgentRoute, len(specs))
	for _, spec := range specs {
		if spec == nil {
			continue
		}
		subRT, ok := spec.Runtime.(*RestateRuntime)
		if !ok {
			continue
		}
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		out[spec.ToolName] = SubAgentRoute{
			Name:        name,
			ToolName:    spec.ToolName,
			ServiceName: subRT.agentLoopServiceName,
			ChildRoutes: buildSubAgentRoutes(spec.Children),
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// delegateToSubAgent invokes the child's AgentLoop service and returns its text result.
// Stream ownership is passed like Temporal's RootWorkflowID: EventTopic + EventLogService
// point at the parent/root AgentEventLog so the parent's Stream subscriber sees child events.
// Respects subAgentExecutionPolicy for timeout and retry.
func (rt *RestateRuntime) delegateToSubAgent(
	ctx restatesdk.Context,
	input AgentLoopInput,
	tc base.ToolCallRequest,
	route SubAgentRoute,
	emit func(events.AgentEvent),
) (string, error) {
	childName := strings.TrimSpace(route.Name)
	if childName == "" {
		childName = tc.ToolName
	}
	serviceName := strings.TrimSpace(route.ServiceName)
	if serviceName == "" {
		return "Sub-agent delegation failed: sub-agent AgentLoop service is not configured.", nil
	}
	if input.SubAgentDepth >= input.MaxSubAgentDepth {
		return fmt.Sprintf("Sub-agent delegation refused: maximum nesting depth (%d) reached.", input.MaxSubAgentDepth), nil
	}

	query := base.SubAgentQuery(tc.Args)
	emit(events.NewAgentStepStartedEvent(childName))
	defer emit(events.NewAgentStepFinishedEvent(childName))

	eventTopic := strings.TrimSpace(input.EventTopic)
	if eventTopic == "" {
		eventTopic = input.RunID
	}
	// Root of this stream: use own event log. Nested: keep the root's EventLogService.
	eventLogService := strings.TrimSpace(input.EventLogService)
	if eventLogService == "" {
		eventLogService = strings.TrimSpace(rt.eventLogServiceName)
	}

	handler := agentLoopRunHandler
	if input.IsStreamHandler {
		handler = agentLoopStreamHandler
	}

	policy := rt.subAgentExecutionPolicy()
	attempts := policy.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	backoff := policy.Retry.InitialInterval

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		childRunID, idErr := executeWithPolicy(ctx, fmt.Sprintf("subagent-run-id-%s-%d", tc.ToolCallID, attempt),
			sdkruntime.ExecutionPolicy{MaxAttempts: 1},
			func(restatesdk.RunContext) (string, error) { return uuid.New().String(), nil })
		if idErr != nil {
			return "", idErr
		}

		childReq := AgentLoopRequest{
			agentLoopCore: agentLoopCore{
				RunID:            childRunID,
				UserPrompt:       query,
				EventTopic:       eventTopic,
				EventLogService:  eventLogService,
				EventTypes:       input.EventTypes,
				SubAgentDepth:    input.SubAgentDepth + 1,
				MaxSubAgentDepth: input.MaxSubAgentDepth,
				SubAgentRoutes:   route.ChildRoutes,
				MemoryScope:      base.SubAgentScope(input.MemoryScope, childName),
			},
			AgentName:        childName,
			LLMStreamEnabled: input.StreamingEnabled,
			StreamHandler:    input.IsStreamHandler,
		}

		resp, err := rt.invokeSubAgentHandler(ctx, serviceName, handler, childReq, policy.Timeout)
		if err == nil {
			if resp == nil || resp.Result == nil {
				return "", nil
			}
			return resp.Result.Content, nil
		}
		lastErr = err
		if attempt >= attempts {
			break
		}
		if backoff > 0 {
			if sleepErr := restatesdk.Sleep(ctx, backoff); sleepErr != nil {
				return "Sub-agent execution failed: " + sleepErr.Error(), nil
			}
			next := time.Duration(float64(backoff) * policy.Retry.BackoffCoefficient)
			if policy.Retry.MaximumInterval > 0 && next > policy.Retry.MaximumInterval {
				backoff = policy.Retry.MaximumInterval
			} else if next > 0 {
				backoff = next
			}
		}
	}
	if lastErr == nil {
		return "", nil
	}
	return "Sub-agent execution failed: " + lastErr.Error(), nil
}

// invokeSubAgentHandler calls the child's AgentLoop service synchronously, optionally
// racing against a timeout (matching Temporal child workflow execution timeout semantics).
// On timeout, the child invocation is cancelled.
func (rt *RestateRuntime) invokeSubAgentHandler(
	ctx restatesdk.Context,
	serviceName string,
	handler string,
	childReq AgentLoopRequest,
	timeout time.Duration,
) (*AgentLoopResponse, error) {
	client := restatesdk.Service[*AgentLoopResponse](ctx, serviceName, handler)
	opts := []restatesdk.RequestOption{restatesdk.WithIdempotencyKey(childReq.RunID)}
	if timeout <= 0 {
		return client.Request(childReq, opts...)
	}
	fut := client.RequestFuture(childReq, opts...)
	timer := restatesdk.After(ctx, timeout)
	first, waitErr := restatesdk.WaitFirst(ctx, fut, timer)
	if waitErr != nil {
		return nil, waitErr
	}
	if first == timer {
		if err := timer.Done(); err != nil {
			return nil, err
		}
		if id := fut.GetInvocationId(); id != "" {
			restatesdk.CancelInvocation(ctx, id)
		}
		return nil, fmt.Errorf("sub-agent timed out after %s", timeout)
	}
	return fut.Response()
}
