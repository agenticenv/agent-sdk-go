// agent_with_nonblocking_run demonstrates non-blocking Agent.Run: start the run,
// poll AgentRun.Status while waiting, optionally Cancel after a few polls, wait on
// Done(), then Get. Uses WithApprovalHandler for tools that require approval.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	reg := agent.NewToolRegistry()
	if err := agent.RegisterTools(reg,
		echo.New(),
		calculator.New(),
	); err != nil {
		log.Fatalf("register tools: %v", err)
	}
	lineCh := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	opts := []agent.Option{
		agent.WithName("agent-with-nonblocking-run"),
		agent.WithDescription("Non-blocking Run demo: Status/Cancel/Done/Get, WithApprovalHandler"),
		agent.WithSystemPrompt("You are a helpful assistant. Use the echo or calculator tool when asked."),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithApprovalHandler(makeApprovalHandler(lineCh)),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.ToolApprovalOptions()...)
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What is 17 + 23?"
	}

	ctx := context.Background()

	// Run returns an AgentRun handle immediately; the work continues in the background.
	// Persist runID before waiting when crash-durability matters.
	agentRun, err := a.Run(ctx, prompt, nil)
	if err != nil {
		log.Fatalf("Run: %v", err)
	}
	runID := agentRun.ID()
	fmt.Println("user:", prompt)
	fmt.Println(shared.RunIDLine(runID))

	// Non-blocking pattern: select on Done (or other work), poll Status, optionally Cancel.
	// Cancelling ctx on Get only unblocks Get — it does not cancel the agent run
	// (use agentRun.Cancel for that).
	fmt.Println("waiting for run to finish (Done channel)...")
	polls := 0
	cancelled := false
	for {
		select {
		case <-agentRun.Done():
			result, err := agentRun.Get(ctx)
			if err != nil {
				st, _ := agentRun.Status(ctx)
				log.Printf("run finished with error (status=%s): %v", st, err)
				return
			}
			st, _ := agentRun.Status(ctx)
			fmt.Printf("agent (status=%s): %s\n", st, result.Content)
			shared.PrintRunFooters(result)
			return
		case <-time.After(5 * time.Second):
			polls++
			st, err := agentRun.Status(ctx)
			if err != nil {
				log.Printf("Status: %v", err)
				continue
			}
			fmt.Printf("still running (poll %d), status=%s\n", polls, st)

			// After several polls (~30s), demonstrate Cancel — gives time to approve at the prompt first.
			if !cancelled && polls >= 5 {
				fmt.Println("cancelling run via AgentRun.Cancel...")
				if err := agentRun.Cancel(ctx); err != nil {
					log.Printf("Cancel: %v", err)
				}
				cancelled = true
			}
		}
	}
}

func makeApprovalHandler(lineCh <-chan string) agent.ApprovalHandler {
	return func(ctx context.Context, req *agent.ApprovalRequest) {
		v, err := agent.ParseToolApproval(req)
		if err != nil {
			log.Printf("approval handler: %v", err)
			return
		}
		args := v.Args
		if args == nil {
			args = map[string]any{}
		}
		argsJSON, _ := json.MarshalIndent(args, "", "  ")
		fmt.Printf("\n--- Tool approval required ---\nTool: %s\nArgs:\n%s\nApprove? (y/n): ", v.ToolName, string(argsJSON))
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
