package main

import (
	"context"
	"fmt"
	"log"
	"sort"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// Three ways to pick a model for one agent. Stub clients, no provider key.
//
//	go run ./agent_with_model_selection
func main() {
	cfg := config.LoadFromEnv()

	fmt.Println("=== Scenario 1: this run uses the local client ===")
	perRunOverride(cfg)

	fmt.Println("\n=== Scenario 2: sensitive prompts stay on the local client ===")
	sensitivePerRun(cfg)

	fmt.Println("\n=== Scenario 3: cheap client for tool rounds, primary for the answer ===")
	cheapPerCall(cfg)
}

func perRunOverride(cfg *config.Config) {
	opts := baseOpts(cfg, "model-selection-override",
		textLLM{model: "primary-stub", content: "answered on the primary model"},
	)
	opts = append(opts,
		agent.WithNamedLLMClients(map[string]interfaces.LLMClient{
			"local": textLLM{model: "local-stub", content: "answered on the local model"},
		}),
	)
	opts = append(opts, config.RuntimeOption(cfg)...)
	runAndPrint(opts, "hello", &agent.AgentRunOptions{LLMClient: "local"})
}

func sensitivePerRun(cfg *config.Config) {
	opts := baseOpts(cfg, "model-selection-sensitive",
		textLLM{model: "primary-stub", content: "answered on the primary model"},
	)
	opts = append(opts,
		agent.WithNamedLLMClients(map[string]interfaces.LLMClient{
			"local": textLLM{model: "local-stub", content: "handled on the local model"},
		}),
		agent.WithLLMSelector(agent.LLMSelectorConfig{
			Select: func(_ context.Context, info agent.LLMSelectInfo) (string, error) {
				if requestHasSensitive(info.Request) {
					return "local", nil
				}
				return "", nil
			},
		}),
	)
	opts = append(opts, config.RuntimeOption(cfg)...)
	runAndPrint(opts, "my ssn is 123-45-6789 — what is the status?", nil)
}

func cheapPerCall(cfg *config.Config) {
	opts := baseOpts(cfg, "model-selection-per-call",
		textLLM{model: "primary-stub", content: "answered on the primary model"},
	)
	opts = append(opts,
		agent.WithTools(lookupTool{}),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithNamedLLMClients(map[string]interfaces.LLMClient{
			"cheap": &toolThenTextLLM{model: "cheap-stub", content: "answered on the cheap model"},
		}),
		agent.WithLLMSelector(agent.LLMSelectorConfig{
			Mode: agent.LLMSelectPerCall,
			Select: func(_ context.Context, info agent.LLMSelectInfo) (string, error) {
				if info.FinalCall || requestHasToolResult(info.Request) {
					return "", nil
				}
				return "cheap", nil
			},
		}),
	)
	opts = append(opts, config.RuntimeOption(cfg)...)
	runAndPrint(opts, "check the status", nil)
}

func baseOpts(cfg *config.Config, name string, primary interfaces.LLMClient) []agent.Option {
	return []agent.Option{
		agent.WithName(name),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithLLMClient(primary),
		agent.WithMaxIterations(4),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
}

func runAndPrint(opts []agent.Option, prompt string, runOpts *agent.AgentRunOptions) {
	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	agentRun, err := a.Run(context.Background(), prompt, runOpts)
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
	printClients(result)
	shared.PrintRunFooters(result)
}

func printClients(result *agent.AgentRunResult) {
	if result == nil || result.Telemetry == nil || len(result.Telemetry.Run.LLMCallsByClient) == 0 {
		return
	}
	names := make([]string, 0, len(result.Telemetry.Run.LLMCallsByClient))
	for name := range result.Telemetry.Run.LLMCallsByClient {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Print("llm calls:")
	for _, name := range names {
		label := name
		if label == "" {
			label = "primary"
		}
		fmt.Printf(" %s=%d", label, result.Telemetry.Run.LLMCallsByClient[name])
	}
	fmt.Println()
}
