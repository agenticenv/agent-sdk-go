// agent_with_run_async demonstrates RunAsync: result and approval channels without
// WithApprovalHandler or RunStream. Complete each approval with req.Respond.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/tools"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
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

	lineCh := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	opts := []agent.Option{
		agent.WithName("agent-with-run-async"),
		agent.WithDescription("RunAsync demo: approvals on approvalCh, outcome on resultCh"),
		agent.WithSystemPrompt("You are a helpful assistant. Use the echo or calculator tool when asked."),
		agent.WithTemporalConfig(&agent.TemporalConfig{
			Host:      cfg.Host,
			Port:      cfg.Port,
			Namespace: cfg.Namespace,
			TaskQueue: cfg.TaskQueue,
		}),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
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

	ctx := context.Background()
	resultCh, approvalCh, err := a.RunAsync(ctx, prompt, "")
	if err != nil {
		log.Fatalf("RunAsync: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for req := range approvalCh {
			argsJSON, _ := json.MarshalIndent(req.Args, "", "  ")
			fmt.Printf("\n--- Tool approval required ---\nTool: %s\nArgs:\n%s\nApprove? (y/n): ", req.ToolName, string(argsJSON))
			line, ok := <-lineCh
			if ok && strings.TrimSpace(strings.ToLower(line)) == "y" {
				if err := req.Respond(agent.ApprovalStatusApproved); err != nil {
					log.Printf("respond approved: %v", err)
				}
			} else if ok {
				if err := req.Respond(agent.ApprovalStatusRejected); err != nil {
					log.Printf("respond rejected: %v", err)
				}
			}
		}
	}()

	fmt.Println("user:", prompt)
	res := <-resultCh
	wg.Wait()

	if res.Err != nil {
		log.Printf("run failed: %v", res.Err)
		return
	}
	fmt.Println("agent:", res.Response.Content)
}
