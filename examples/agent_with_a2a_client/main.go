package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	a2aclient "github.com/agenticenv/agent-sdk-go/pkg/a2a/client"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func main() {
	cfg := config.LoadFromEnv()
	a2aCfg, err := config.A2ABuildAgentConfig(cfg)
	if err != nil {
		log.Fatal(err)
	}
	serverName := config.A2ADefaultServerName(cfg)

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	logCfg := config.NewLoggerFromLogConfig(cfg)
	a2aOpts := []a2aclient.Option{
		a2aclient.WithLogger(logCfg),
		a2aclient.WithLogLevel(cfg.LogLevel),
		a2aclient.WithSkillFilter(a2aCfg.SkillFilter),
	}
	if a2aCfg.Timeout > 0 {
		a2aOpts = append(a2aOpts, a2aclient.WithTimeout(a2aCfg.Timeout))
	}
	if a2aCfg.Token != "" {
		a2aOpts = append(a2aOpts, a2aclient.WithToken(a2aCfg.Token))
	}
	if len(a2aCfg.Headers) > 0 {
		a2aOpts = append(a2aOpts, a2aclient.WithHeaders(a2aCfg.Headers))
	}
	if a2aCfg.SkipTLSVerify {
		a2aOpts = append(a2aOpts, a2aclient.WithSkipTLSVerify(true))
	}

	cl, err := a2aclient.NewClient(serverName, a2aCfg.URL, a2aOpts...)
	if err != nil {
		log.Fatalf("failed to create A2A client: %v", err)
	}

	opts := []agent.Option{
		agent.WithName("agent-with-a2a-client"),
		agent.WithDescription("Agent with A2A from env (WithA2AClients)"),
		agent.WithSystemPrompt("You are a helpful assistant. Use A2A tools from your tool list when they help answer the user."),
		agent.WithLLMClient(llmClient),
		agent.WithA2AClients(cl),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(logCfg),
		agent.WithLogLevel(cfg.LogLevel),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What tools do you have available? and how do you use them?"
	}

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
