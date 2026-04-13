// Interactive streaming REPL for the agent_with_worker example.
//
// Usage:
//
//	go run . [initial prompt]
//
// Starts an event-workflow-backed stream so you can observe each event as it
// arrives and simulate failure scenarios (e.g. kill the worker or this process
// mid-run and restart to watch the streamingUnavailable path).
//
// At the "you>" prompt type any message.  Approval requests pause the stream
// and ask for y/n before continuing.  Type "exit" or "quit" to stop.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/agent_with_worker/opts"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	baseOpts := opts.Common(cfg.Host, cfg.Port, cfg.Namespace, cfg.TaskQueue, llmClient, config.NewLoggerFromLogConfig(cfg))
	agentOpts := append(baseOpts,
		agent.DisableLocalWorker(),
		agent.EnableRemoteWorkers(),
		agent.WithStream(true),
	)

	a, err := agent.NewAgent(agentOpts...)
	if err != nil {
		log.Fatalf("failed to create agent: %v", err)
	}
	defer a.Close()

	fmt.Println("=== agent_with_worker interactive stream ===")
	fmt.Println("Events arrive via the event workflow (UpdateWorkflow path).")
	fmt.Println("Simulate scenarios: kill the worker or this process mid-run, then restart.")
	fmt.Println("Type 'exit' or 'quit' or 'bye' to stop.")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)

	// If an initial prompt was provided on the CLI, run it first.
	initial := strings.Join(os.Args[1:], " ")
	if initial != "" {
		runStream(ctx, a, scanner, initial)
	}

	for {
		fmt.Print("you> ")
		if !scanner.Scan() {
			break // EOF or Ctrl-D
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" || line == "bye" {
			fmt.Println("Goodbye!")
			break
		}

		runStream(ctx, a, scanner, line)

		if ctx.Err() != nil {
			break // SIGINT/SIGTERM
		}
	}
}

// runStream starts one Stream call, prints events, and handles approval prompts inline.
func runStream(ctx context.Context, a *agent.Agent, scanner *bufio.Scanner, prompt string) {
	eventCh, err := a.Stream(ctx, prompt, "")
	if err != nil {
		fmt.Printf("[error] failed to start stream: %v\n\n", err)
		return
	}

	fmt.Println("--- stream start ---")
	streamed := false

	for ev := range eventCh {
		if ev == nil {
			continue
		}

		switch ev.Type {
		case agent.AgentEventContentDelta, agent.AgentEventThinkingDelta:
			streamed = true
			if ev.Content != "" {
				fmt.Print(ev.Content)
			}

		case agent.AgentEventContent:
			if ev.Content != "" {
				fmt.Printf("[content] %s\n", ev.Content)
			}

		case agent.AgentEventToolCall:
			if ev.ToolCall != nil {
				args, _ := json.Marshal(ev.ToolCall.Args)
				fmt.Printf("\n[tool_call] %s  status=%s  args=%s\n",
					ev.ToolCall.ToolName, ev.ToolCall.Status, string(args))
			}

		case agent.AgentEventToolResult:
			if ev.ToolCall != nil {
				fmt.Printf("[tool_result] %s: %v\n", ev.ToolCall.ToolName, ev.ToolCall.Result)
			}

		case agent.AgentEventApproval:
			if ev.Approval != nil {
				handleApproval(ctx, a, scanner, ev)
			}

		case agent.AgentEventError:
			fmt.Printf("\n[error] %s\n", ev.Content)

		case agent.AgentEventComplete:
			if streamed {
				fmt.Println() // newline after token stream
			}
			if ev.Content != "" && !streamed {
				fmt.Printf("[complete] %s\n", ev.Content)
			} else {
				fmt.Println("[complete]")
			}
			if ev.Usage != nil {
				u := ev.Usage
				fmt.Printf("[usage] prompt=%d completion=%d total=%d\n",
					u.PromptTokens, u.CompletionTokens, u.TotalTokens)
			}

		default:
			fmt.Printf("[%s] %+v\n", ev.Type, ev)
		}
	}

	fmt.Println("--- stream end ---")
	fmt.Println()
}

// handleApproval pauses the event loop and asks the user to approve or reject the tool call.
func handleApproval(ctx context.Context, a *agent.Agent, scanner *bufio.Scanner, ev *agent.AgentEvent) {
	ap := ev.Approval
	args, _ := json.Marshal(ap.Args)
	fmt.Printf("\n[approval] agent=%s tool=%s args=%s\n", ev.AgentName, ap.ToolName, string(args))

	for {
		fmt.Print("approve? (y/n)> ")
		if !scanner.Scan() {
			// EOF — treat as reject
			fmt.Println("EOF, rejecting.")
			_ = a.OnApproval(ctx, ap.ApprovalToken, agent.ApprovalStatusRejected)
			return
		}
		ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
		switch ans {
		case "y", "yes":
			if err := a.OnApproval(ctx, ap.ApprovalToken, agent.ApprovalStatusApproved); err != nil {
				fmt.Printf("[approval error] %v\n", err)
			} else {
				fmt.Println("[approved]")
			}
			return
		case "n", "no":
			if err := a.OnApproval(ctx, ap.ApprovalToken, agent.ApprovalStatusRejected); err != nil {
				fmt.Printf("[approval error] %v\n", err)
			} else {
				fmt.Println("[rejected]")
			}
			return
		default:
			fmt.Println("please enter y or n")
		}
	}
}
