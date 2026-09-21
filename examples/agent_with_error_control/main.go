// Example demonstrating WithErrorControl: fallback after LLM failure,
// one extra iteration past max-iter, and a per-tool circuit breaker.
//
// All scenarios use stub LLM clients and a stub tool — no provider key
// and no extra infra. AGENT_RUNTIME=temporal or restate still works via
// config.RuntimeOption.
//
// Run from examples/:
//
//	go run ./agent_with_error_control
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func main() {
	cfg := config.LoadFromEnv()

	fmt.Println("=== Scenario 1: OnLLMFailure FallbackModel ===")
	runFallback(cfg)

	fmt.Println("\n=== Scenario 2: OnMaxIterationsExceeded ExtendIterations ===")
	runExtend(cfg)

	fmt.Println("\n=== Scenario 3: Circuit breaker skip ===")
	runBreaker(cfg)
}

func runFallback(cfg *config.Config) {
	opts := []agent.Option{
		agent.WithName("error-control-fallback"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithLLMClient(failLLM{}),
		agent.WithNamedLLMClients(map[string]interfaces.LLMClient{
			"cheap": textLLM{content: "fallback model answered after rate limit"},
		}),
		agent.WithLLMExecutionConfig(agent.ExecutionConfig{MaxAttempts: 1}),
		agent.WithErrorControl(agent.ErrorControlConfig{
			FallbackLLMClient: "cheap",
			Hooks: agent.AgentErrorHooks{
				OnLLMFailure: func(_ context.Context, info agent.LLMFailureInfo) agent.ErrorControlDecision {
					var llmErr *interfaces.LLMError
					reason := interfaces.LLMReasonUnknown
					if errors.As(info.Err, &llmErr) {
						reason = llmErr.Reason
					}
					fmt.Fprintf(os.Stderr, "[error-control] OnLLMFailure: reason=%s → FallbackModel\n", reason)
					return agent.ErrorControlDecision{Action: agent.ErrorControlFallbackModel}
				},
			},
		}),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)
	runAndPrint(opts, "hi")
}

func runExtend(cfg *config.Config) {
	client := &seqLLM{
		model: "extend-stub",
		responses: []*interfaces.LLMResponse{
			{ToolCalls: []*interfaces.ToolCall{toolCall("c1", "fail_once", map[string]any{"n": 1})}},
			{Content: "finished after extra iteration"},
		},
	}
	opts := []agent.Option{
		agent.WithName("error-control-extend"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithLLMClient(client),
		agent.WithMaxIterations(1),
		agent.WithTools(failTool{name: "fail_once", err: nil}),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithErrorControl(agent.ErrorControlConfig{
			Hooks: agent.AgentErrorHooks{
				OnMaxIterationsExceeded: func(_ context.Context, info agent.MaxIterationsInfo) agent.ErrorControlDecision {
					fmt.Fprintf(os.Stderr, "[error-control] OnMaxIterationsExceeded: last=%s → ExtendIterations extra=1\n", info.LastAction)
					return agent.ErrorControlDecision{Action: agent.ErrorControlExtendIterations, ExtraIterations: 1}
				},
			},
		}),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)
	runAndPrint(opts, "go")
}

func runBreaker(cfg *config.Config) {
	same := map[string]any{"n": 1}
	client := &seqLLM{
		model: "breaker-stub",
		responses: []*interfaces.LLMResponse{
			{ToolCalls: []*interfaces.ToolCall{toolCall("c1", "boom", same)}},
			{ToolCalls: []*interfaces.ToolCall{toolCall("c2", "boom", same)}},
			{ToolCalls: []*interfaces.ToolCall{toolCall("c3", "boom", same)}},
			{Content: "stopped looping"},
		},
	}
	opts := []agent.Option{
		agent.WithName("error-control-breaker"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithLLMClient(client),
		agent.WithMaxIterations(5),
		agent.WithTools(failTool{name: "boom", err: errors.New("always")}),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithErrorControl(agent.ErrorControlConfig{
			CircuitBreaker: &agent.CircuitBreakerConfig{MaxConsecutiveSameArgs: 2},
		}),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)
	runAndPrint(opts, "go")
}

func runAndPrint(opts []agent.Option, prompt string) {
	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	agentRun, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Printf("run start error: %v", err)
		return
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		log.Printf("run error: %v", err)
		return
	}
	fmt.Printf("assistant: %s\n", result.Content)
	if result.Telemetry != nil && result.Telemetry.Tools.FailedCalls > 0 {
		fmt.Printf("tools: total=%d failed=%d\n", result.Telemetry.Tools.TotalCalls, result.Telemetry.Tools.FailedCalls)
	}
	shared.PrintRunFooters(result)
}
