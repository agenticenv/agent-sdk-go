// Example agent using a Weaviate vector retriever.
//
// Run from the repository root (or examples/):
//
//	go run ./examples/agent_with_retriever/weaviate "What do you know about our docs?"
//
// See ../README.md for setup and env vars.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	examplecfg "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/agent_with_retriever/common"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	weaviate "github.com/agenticenv/agent-sdk-go/pkg/retriever/weaviate"
)

func main() {
	cfg := examplecfg.LoadFromEnv()
	retrieverCfg, err := common.LoadSettings()
	if err != nil {
		log.Fatalf("retriever config: %v", err)
	}

	llmClient, err := examplecfg.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}
	logr := examplecfg.NewLoggerFromLogConfig(cfg)

	wOpts := []weaviate.Option{
		weaviate.WithHost(retrieverCfg.WeaviateHost),
		weaviate.WithScheme(retrieverCfg.WeaviateScheme),
		weaviate.WithClassName(retrieverCfg.WeaviateClass),
		weaviate.WithContentField(retrieverCfg.WeaviateContentField),
		weaviate.WithSourceField(retrieverCfg.WeaviateSourceField),
		weaviate.WithLogger(logr),
	}
	if retrieverCfg.WeaviateTopK > 0 {
		wOpts = append(wOpts, weaviate.WithTopK(retrieverCfg.WeaviateTopK))
	}
	if retrieverCfg.WeaviateMinScore > 0 {
		wOpts = append(wOpts, weaviate.WithMinScore(retrieverCfg.WeaviateMinScore))
	}

	retriever, err := weaviate.NewRetriever(retrieverCfg.WeaviateRetrieverName, wOpts...)
	if err != nil {
		log.Fatalf("weaviate retriever: %v", err)
	}

	opts := common.AgentOptions(cfg, llmClient, logr, retrieverCfg, "weaviate")
	opts = append(opts, agent.WithRetrievers(retriever))

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(examplecfg.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What is the return policy according to the knowledge base?"
	}

	fmt.Printf("backend: weaviate  mode: %s  retriever: %s\n", retrieverCfg.RetrieverMode, retriever.Name())
	fmt.Printf("hint: %s\n", common.ModeHint(retrieverCfg.RetrieverMode))
	fmt.Println("user:", prompt)

	agentRun, err := a.Run(context.Background(), prompt, nil)
	if err != nil {
		log.Printf("run failed: %v", err)
		return
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		return
	}
	fmt.Println("assistant:", result.Content)
	shared.PrintRunFooters(result)
}
