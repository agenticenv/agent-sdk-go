// agent_with_durable_engine demonstrates passing a caller-owned durable-go engine to
// the local runtime. The caller owns the engine lifecycle: create it, pass it as
// LocalConfig.Engine, and close it when done. Use this pattern for payload encryption,
// journal MAC, or a step-token key that survives restart — options not set on the
// SDK-built default engine. Same split as temporal.WithTemporalClient.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	agentlocal "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/local"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	durable "github.com/agenticenv/durable-go"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	dataDir := os.Getenv("DURABLE_DATA_DIR")
	if dataDir == "" {
		dataDir, err = os.MkdirTemp("", "agent-durable-engine-")
		if err != nil {
			log.Fatalf("temp dataDir: %v", err)
		}
		defer func() { _ = os.RemoveAll(dataDir) }()
	}

	codec, err := durable.NewAESGCMCodec(keyFromEnvOrRandom("DURABLE_PAYLOAD_KEY", 32))
	if err != nil {
		log.Fatalf("payload codec: %v", err)
	}
	logr := config.NewLoggerFromLogConfig(cfg)
	engineOpts := []durable.Option{
		durable.WithPayloadCodec(codec),
		durable.WithJournalMACKey(keyFromEnvOrRandom("DURABLE_JOURNAL_MAC_KEY", 32)),
		durable.WithStepTokenKey(keyFromEnvOrRandom("DURABLE_STEP_TOKEN_KEY", 32)),
		durable.WithAutoPurge(7 * 24 * time.Hour),
	}
	if sl, ok := logr.(*logger.SlogLogger); ok {
		if sg := sl.Slog(); sg != nil {
			engineOpts = append(engineOpts, durable.WithLogger(sg))
		}
	}

	engine, err := durable.NewEngine(context.Background(), dataDir, engineOpts...)
	if err != nil {
		log.Fatalf("failed to create durable engine: %v", err)
	}
	defer func() { _ = engine.Close() }()

	a, err := agent.NewAgent(
		agent.WithName("durable-engine-agent"),
		agent.WithDescription("Agent using caller-owned durable-go engine"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agentlocal.WithLocalConfig(&agentlocal.LocalConfig{Engine: engine}),
		agent.WithLLMClient(llmClient),
		agent.WithLogger(logr),
	)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "Hello, what can you do?"
	}
	fmt.Println("user:", prompt)
	agentRun, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Printf("agent run failed: %v", err)
		return
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		return
	}
	fmt.Println("assistant:", result.Content)
	shared.PrintRunFooters(result)
}

func keyFromEnvOrRandom(env string, n int) []byte {
	if s := strings.TrimSpace(os.Getenv(env)); s != "" {
		b, err := hex.DecodeString(s)
		if err != nil {
			log.Fatalf("%s: hex decode: %v", env, err)
		}
		return b
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		log.Fatalf("%s: generate key: %v", env, err)
	}
	return b
}
