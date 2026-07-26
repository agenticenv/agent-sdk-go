// OTLP via [agent.WithObservabilityConfig] — the SDK constructs tracer, metrics, and logs (OTLP log
// export for the default SDK logger via the slog bridge; internal default batching/export timing).
//
// Run from repo root:
//
//	go run ./examples/agent_with_observability/config/
//
// Env: OTEL_EXPORTER_OTLP_ENDPOINT (required), optional OTLP_PROTOCOL=grpc|http, OTLP_INSECURE=true.
package main

import (
	"context"
	"fmt"
	"log"

	excfg "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/agent_with_observability/setup"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func main() {
	cfg := excfg.LoadFromEnv()
	otlp := setup.MustParseOTLP()

	llmClient, err := excfg.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	opts := setup.BaseAgentOptions(cfg, llmClient)
	opts = append(opts,
		agent.WithObservabilityConfig(&agent.ObservabilityConfig{
			Endpoint: otlp.Endpoint,
			Protocol: otlp.AgentProto,
			Insecure: otlp.Insecure,
		}),
	)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(excfg.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := setup.UserPrompt()
	fmt.Printf("entry=config (WithObservabilityConfig: OTLP traces, metrics, logs)\nuser: %s\n", prompt)
	agentRun, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Printf("run failed: %v", err)
		return
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		return
	}
	fmt.Printf("assistant: %s\n", result.Content)
	shared.PrintRunFooters(result)
}
