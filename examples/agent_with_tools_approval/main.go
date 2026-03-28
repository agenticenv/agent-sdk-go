package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	config "github.com/vvsynapse/agent-sdk-go/examples"
	"github.com/vvsynapse/agent-sdk-go/pkg/agent"
	"github.com/vvsynapse/agent-sdk-go/pkg/tools"
	"github.com/vvsynapse/agent-sdk-go/pkg/tools/calculator"
	"github.com/vvsynapse/agent-sdk-go/pkg/tools/echo"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	reg := tools.NewRegistry()
	reg.Register(echo.New())
	reg.Register(calculator.New())

	// Single stdin reader: same pattern as cmd for consistency and timeout handling
	lineCh := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	opts := []agent.Option{
		agent.WithName("agent-with-tools-approval"),
		agent.WithDescription("Agent with tools that require user approval before execution"),
		agent.WithSystemPrompt("You are a helpful assistant. Use the echo or calculator tool when asked."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: cfg.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithApprovalHandler(makeApprovalHandler(lineCh)),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What is 17 + 23?"
	}

	fmt.Println("user:", prompt)
	response, err := a.Run(context.Background(), prompt, "")
	if err != nil {
		log.Printf("run failed: %v", err)
		return
	}
	fmt.Println("agent:", response.Content)
}

func makeApprovalHandler(lineCh <-chan string) agent.ApprovalHandler {
	return func(ctx context.Context, req *agent.ApprovalRequest) {
		argsJSON, _ := json.MarshalIndent(req.Args, "", "  ")
		fmt.Printf("\n--- Tool approval required ---\nTool: %s\nArgs:\n%s\nApprove? (y/n): ", req.ToolName, string(argsJSON))
		select {
		case <-ctx.Done():
			return
		case line, ok := <-lineCh:
			if ok && strings.TrimSpace(strings.ToLower(line)) == "y" {
				_ = req.Respond(agent.ApprovalStatusApproved)
			} else if ok {
				_ = req.Respond(agent.ApprovalStatusRejected)
			}
		}
	}
}
