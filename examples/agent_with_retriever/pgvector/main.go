// Example agent using a PostgreSQL pgvector retriever.
//
// Run from the repository root (or examples/):
//
//	go run ./examples/agent_with_retriever/pgvector "What do you know about our docs?"
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
	pgretriever "github.com/agenticenv/agent-sdk-go/pkg/retriever/pgvector"
)

func main() {
	cfg := examplecfg.LoadFromEnv()
	retrieverCfg, err := common.LoadSettings()
	if err != nil {
		log.Fatalf("retriever config: %v", err)
	}
	if retrieverCfg.PGDSN == "" {
		log.Fatal("PGVECTOR_DSN is required for the pgvector example; see ../README.md")
	}
	if err := common.ValidateEmbeddingConfig(retrieverCfg); err != nil {
		log.Fatalf("embedding config: %v", err)
	}

	llmClient, err := examplecfg.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}
	logr := examplecfg.NewLoggerFromLogConfig(cfg)

	embed, err := common.OpenAIEmbedFunc(retrieverCfg)
	if err != nil {
		log.Fatalf("embed func: %v", err)
	}

	pOpts := []pgretriever.Option{
		pgretriever.WithDSN(retrieverCfg.PGDSN),
		pgretriever.WithTable(retrieverCfg.PGTable),
		pgretriever.WithContentCol(retrieverCfg.PGContentCol),
		pgretriever.WithSourceCol(retrieverCfg.PGSourceCol),
		pgretriever.WithEmbeddingCol(retrieverCfg.PGEmbeddingCol),
		pgretriever.WithLogger(logr),
	}
	if retrieverCfg.PGTopK > 0 {
		pOpts = append(pOpts, pgretriever.WithTopK(retrieverCfg.PGTopK))
	}
	pOpts = append(pOpts, pgretriever.WithMinScore(retrieverCfg.PGMinScore))

	retriever, err := pgretriever.NewRetriever(retrieverCfg.PGRetrieverName, embed, pOpts...)
	if err != nil {
		log.Fatalf("pgvector retriever: %v", err)
	}

	opts := common.AgentOptions(cfg, llmClient, logr, retrieverCfg, "pgvector")
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

	fmt.Printf("backend: pgvector  mode: %s  retriever: %s  table: %s  minScore: %.2f\n",
		retrieverCfg.RetrieverMode, retriever.Name(), retrieverCfg.PGTable, retrieverCfg.PGMinScore)
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
