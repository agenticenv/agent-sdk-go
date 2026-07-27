// Interactive streaming REPL for the agent_with_worker example.
//
// Usage:
//
//	go run . [initial prompt]
//
// Streams events delivered via Temporal Workflow Streams, so each run has a
// durable event log hosted in the Temporal server. Kill the worker or this
// process mid-run, then restart to observe workflow replay and recovery.
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
	"github.com/agenticenv/agent-sdk-go/examples/shared"
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
	)

	a, err := agent.NewAgent(agentOpts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	fmt.Println("=== agent_with_worker interactive stream ===")
	fmt.Println("Events are delivered via Temporal Workflow Streams (durable, replayable).")
	fmt.Println("Simulate scenarios: kill the worker or this process mid-run, then restart.")
	fmt.Println("Type 'exit' or 'quit' or 'bye' to stop.")
	fmt.Println()

	scanner := bufio.NewScanner(os.Stdin)

	initial := strings.Join(os.Args[1:], " ")
	if initial != "" {
		runStream(ctx, a, scanner, initial)
	}

	for {
		fmt.Print("you> ")
		if !scanner.Scan() {
			break
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
			break
		}
	}
}

func runStream(ctx context.Context, a *agent.Agent, scanner *bufio.Scanner, prompt string) {
	// runID is available synchronously before any event arrives.
	// Persist it when crash-durability matters; use it with Agent.GetAgentStream to resume the stream.
	agentStream, err := a.Stream(ctx, prompt, nil)
	if err != nil {
		fmt.Printf("[error] failed to start stream: %v\n\n", err)
		return
	}
	runID := agentStream.ID()
	eventCh, err := agentStream.Events(ctx)
	if err != nil {
		log.Printf("stream events: %v", err)
		return
	}
	fmt.Println(shared.RunIDLine(runID))

	fmt.Println("--- stream start ---")
	streamed := false

	for ev := range eventCh {
		if ev == nil {
			continue
		}

		switch ev.Type() {
		case agent.AgentEventTypeTextMessageContent, agent.AgentEventTypeReasoningMessageContent:
			streamed = true
			if t, ok := ev.(*agent.AgentTextMessageContentEvent); ok && t.Delta != "" {
				fmt.Print(t.Delta)
			} else if r, ok := ev.(*agent.AgentReasoningMessageContentEvent); ok && r.Delta != "" {
				fmt.Print(r.Delta)
			}

		case agent.AgentEventTypeToolCallStart:
			if t, ok := ev.(*agent.AgentToolCallStartEvent); ok {
				fmt.Printf("\n[tool_call] %s  (id=%s)\n", t.ToolCallName, t.ToolCallID)
			}

		case agent.AgentEventTypeToolCallArgs:
			if t, ok := ev.(*agent.AgentToolCallArgsEvent); ok && t.Delta != "" {
				fmt.Printf("[tool_args] %s\n", t.Delta)
			}

		case agent.AgentEventTypeToolCallResult:
			if t, ok := ev.(*agent.AgentToolCallResultEvent); ok {
				fmt.Printf("[tool_result] %s: %s\n", t.ToolCallID, t.Content)
			}

		case agent.AgentEventTypeCustom:
			if v, ok := shared.ToolApprovalValueFromEvent(ev); ok {
				args, _ := json.Marshal(v.Args)
				fmt.Printf("\n[approval] agent=%s kind=tool target=%s args=%s\n", v.AgentName, v.ToolName, string(args))
				handleApprovalTokenPrompt(ctx, agentStream, scanner, v.ApprovalToken)
			} else if v, ok := shared.DelegationApprovalValueFromEvent(ev); ok {
				args, _ := json.Marshal(v.Args)
				fmt.Printf("\n[approval] agent=%s kind=delegation target=delegate:%s args=%s\n", v.AgentName, v.SubAgentName, string(args))
				handleApprovalTokenPrompt(ctx, agentStream, scanner, v.ApprovalToken)
			}

		case agent.AgentEventTypeRunError:
			if re, ok := ev.(*agent.AgentRunErrorEvent); ok {
				fmt.Printf("\n[error] %s\n", re.Message)
			}

		case agent.AgentEventTypeRunFinished:
			res := shared.RunResultFromFinishedEvent(ev)
			if streamed {
				fmt.Println()
			} else if res != nil && res.Content != "" {
				fmt.Println(res.Content)
			}
			shared.PrintRunFooters(res)

		default:
			//fmt.Printf("[%s] %+v\n", ev.Type(), ev)
			continue
		}
	}

	fmt.Println("--- stream end ---")
	fmt.Println()
}

func handleApprovalTokenPrompt(ctx context.Context, agentStream agent.AgentStream, scanner *bufio.Scanner, token string) {
	for {
		fmt.Print("approve? (y/n)> ")
		if !scanner.Scan() {
			fmt.Println("EOF, rejecting.")
			_ = agentStream.Approve(ctx, token, agent.ApprovalStatusRejected)
			return
		}
		ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
		switch ans {
		case "y", "yes":
			if err := agentStream.Approve(ctx, token, agent.ApprovalStatusApproved); err != nil {
				fmt.Printf("[approval error] %v\n", err)
			} else {
				fmt.Println("[approved]")
			}
			return
		case "n", "no":
			if err := agentStream.Approve(ctx, token, agent.ApprovalStatusRejected); err != nil {
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
