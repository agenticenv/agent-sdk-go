// OTLP via pre-built [observability.NewTracer], [observability.NewMetrics], [observability.NewLogs],
// then [agent.WithTracer], [agent.WithMetrics], [agent.WithLogs]. With no [agent.WithLogger], the SDK
// wires the default logger to the OTLP log provider from [WithLogs] (same idea as logs under
// [agent.WithObservabilityConfig]). Do not combine this wiring with [agent.WithObservabilityConfig] on the same agent.
//
// Run from repo root:
//
//	go run ./examples/agent_with_observability/objects/
//
// Env: same as config/ (OTEL_EXPORTER_OTLP_ENDPOINT, OTLP_PROTOCOL, OTLP_INSECURE).
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	excfg "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/agent_with_observability/setup"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
)

func main() {
	cfg := excfg.LoadFromEnv()
	otlp := setup.MustParseOTLP()

	baseObs := []observability.Option{
		observability.WithEndpoint(otlp.Endpoint),
		observability.WithProtocol(otlp.ObsProto),
		observability.WithInsecure(otlp.Insecure),
	}

	tr, err := observability.NewTracer(append(baseObs,
		observability.WithName("observability-example-tracer"),
	)...)
	if err != nil {
		log.Fatalf("NewTracer: %v", err)
	}
	mt, err := observability.NewMetrics(append(baseObs,
		observability.WithName("observability-example-metrics"),
	)...)
	if err != nil {
		_ = tr.Shutdown(context.Background())
		log.Fatalf("NewMetrics: %v", err)
	}
	lg, err := observability.NewLogs(append(baseObs,
		observability.WithName("observability-example-logs"),
	)...)
	if err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tr.Shutdown(ctx)
		_ = mt.Shutdown(ctx)
		log.Fatalf("NewLogs: %v", err)
	}

	shutdownOTLP := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tr.Shutdown(ctx)
		_ = mt.Shutdown(ctx)
		_ = lg.Shutdown(ctx)
	}

	llmClient, err := excfg.NewLLMClientFromConfig(cfg)
	if err != nil {
		shutdownOTLP()
		log.Fatalf("failed to create LLM client: %v", err)
	}

	opts := setup.BaseAgentOptions(cfg, llmClient)
	opts = append(opts,
		agent.WithTracer(tr),
		agent.WithMetrics(mt),
		agent.WithLogs(lg),
	)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		shutdownOTLP()
		log.Fatal(excfg.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := setup.UserPrompt()
	fmt.Printf("entry=objects (WithTracer / WithMetrics / WithLogs; default logger bridged to OTLP)\nuser: %s\n", prompt)
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
