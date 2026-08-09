// Interactive streaming REPL for the durable_agent Temporal lab.
//
// Usage:
//
//	go run . [initial prompt]
//
// Streams events delivered via Temporal Workflow Streams, so each run has a
// durable event log hosted in the Temporal server. Kill the worker or this
// process mid-run, then restart to observe workflow replay and recovery.
//
// GetAgentStream support: on startup the agent checks for a saved run state
// (runID + last offset) in /tmp/durable_agent_runstate.json. If one exists it
// asks whether to reconnect from the last known offset. This demonstrates the
// full crash-and-recover cycle without any extra infrastructure.
//
// At the "you>" prompt type any message. Approval requests pause the stream
// and ask for y/n before continuing.  Type "exit" or "quit" to stop.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/durable_agent/temporal/opts"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

// stateFile is where the agent persists runID + offset between process restarts.
// Use /tmp so it is easy to locate and does not pollute the repo.
const stateFile = "/tmp/durable_agent_temporal_runstate.json"

// runState holds the mid-stream position that survives a process crash.
type runState struct {
	RunID  string `json:"run_id"`
	Offset int64  `json:"offset"`
	Prompt string `json:"prompt"` // original prompt, printed on reconnect for context
}

// loadRunState reads a saved run state from stateFile, or returns nil if none.
func loadRunState() *runState {
	data, err := os.ReadFile(stateFile)
	if err != nil {
		return nil
	}
	var s runState
	if err := json.Unmarshal(data, &s); err != nil || s.RunID == "" {
		return nil
	}
	return &s
}

// saveRunState atomically updates stateFile with the current runID and offset.
func saveRunState(runID string, offset int64, prompt string) {
	data, err := json.Marshal(runState{RunID: runID, Offset: offset, Prompt: prompt})
	if err != nil {
		return
	}
	// WriteFile is not atomic, but good enough for a demo; for production use
	// a rename-based atomic write (write to temp file, then os.Rename).
	_ = os.WriteFile(stateFile, data, 0o600)
}

// clearRunState removes the persisted state after a run completes or is abandoned.
func clearRunState() {
	_ = os.Remove(stateFile)
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	baseOpts := opts.Common(cfg.Host, cfg.Port, cfg.Namespace, cfg.TaskQueue, llmClient, config.NewLoggerFromLogConfig(cfg))
	// Lab default: DisableLocalWorker + separate ./worker.
	// Task batch only (EXAMPLES_AUTO_APPROVE=true): embedded worker so examples.yml can run single-process.
	batch := strings.EqualFold(strings.TrimSpace(os.Getenv("EXAMPLES_AUTO_APPROVE")), "true")
	agentOpts := append([]agent.Option{}, baseOpts...)
	if !batch {
		agentOpts = append(agentOpts, agent.DisableLocalWorker())
	}

	a, err := agent.NewAgent(agentOpts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	var closeOnce sync.Once
	closeAgent := func() {
		closeOnce.Do(func() {
			a.Close()
		})
	}
	defer closeAgent()

	// Buffer 2 so a second signal can force-exit if Close() blocks.
	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		<-sigChan
		fmt.Println("\nShutdown signal received; closing agent...")
		done := make(chan struct{})
		go func() {
			closeAgent()
			close(done)
		}()
		select {
		case <-done:
			fmt.Println("durable_agent stopped.")
			os.Exit(0)
		case <-sigChan:
			fmt.Println("Second signal: forcing exit.")
			os.Exit(1)
		}
	}()

	fmt.Println("=== durable_agent/temporal interactive stream ===")
	fmt.Println("Events are delivered via Temporal Workflow Streams (durable, replayable).")
	if batch {
		fmt.Println("Batch mode: embedded Temporal worker in this process.")
	} else {
		fmt.Println("Start ./durable_agent/temporal/worker first (DisableLocalWorker).")
		fmt.Println("Kill this process mid-stream (Ctrl+C / pkill), then restart to reconnect.")
	}
	fmt.Println("Type 'exit' or 'quit' or 'bye' to stop.")
	fmt.Println()

	// Batch runs: skip leftover reconnect prompts.
	if batch {
		clearRunState()
	}

	scanner := bufio.NewScanner(os.Stdin)

	// Check for a saved run state and offer reconnect before starting the REPL.
	if saved := loadRunState(); saved != nil {
		fmt.Printf("[reconnect] found saved run state:\n")
		fmt.Printf("  run_id : %s\n", saved.RunID)
		fmt.Printf("  offset : %d\n", saved.Offset)
		fmt.Printf("  prompt : %q\n", saved.Prompt)
		fmt.Print("Reconnect from last offset? [y/n]> ")
		if scanner.Scan() {
			ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
			if ans == "y" || ans == "yes" {
				reconnectStream(ctx, a, scanner, saved)
			} else {
				fmt.Println("[reconnect] skipped — clearing saved state.")
				clearRunState()
			}
		}
		fmt.Println()
	}

	initial := strings.Join(os.Args[1:], " ")
	if initial != "" {
		runStream(ctx, a, scanner, initial)
	}

	// Batch + CLI prompt: exit after the one-shot run (stdin is still a TTY under task).
	if batch {
		return
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

// runStream starts a new stream run, persisting runID+offset on each event so a
// crashed process can resume via reconnectStream on the next startup.
func runStream(ctx context.Context, a *agent.Agent, scanner *bufio.Scanner, prompt string) {
	// runID is available synchronously before any events arrive.
	// Persist it immediately — before consuming eventCh — so a Ctrl+C or kill -9
	// between start and the first event doesn't lose the reconnect handle.
	agentStream, err := a.Stream(ctx, prompt, nil)
	if err != nil {
		fmt.Printf("[error] failed to start stream: %v\n\n", err)
		return
	}
	runID := agentStream.ID()
	eventCh, err := agentStream.Events(ctx)
	if err != nil {
		fmt.Printf("[error] failed to subscribe to stream events: %v\n\n", err)
		return
	}
	saveRunState(runID, 0, prompt)
	fmt.Println(shared.RunIDLine(runID))

	fmt.Println("--- stream start ---")
	drainStreamEvents(ctx, agentStream, scanner, prompt, runID, eventCh, 0)
	fmt.Println("--- stream end ---")
	fmt.Println()
}

// reconnectStream resumes a prior stream from the offset saved in state.
// Events at offsets already seen (≤ state.Offset) are silently discarded so
// the user only sees new content.
func reconnectStream(ctx context.Context, a *agent.Agent, scanner *bufio.Scanner, state *runState) {
	fmt.Printf("[reconnect] reconnecting run_id=%s from offset=%d\n", state.RunID, state.Offset)
	fmt.Printf("[reconnect] original prompt: %q\n\n", state.Prompt)

	agentStream, err := a.GetAgentStream(ctx, state.RunID)
	if err != nil {
		if errors.Is(err, agent.ErrRunAlreadyCompleted) {
			fmt.Println("[reconnect] the run completed successfully while you were disconnected.")
			fmt.Println("[reconnect] the response was generated, but streaming events are no longer available.")
			fmt.Println("[reconnect] if conversation history is configured, the response is already saved —")
			fmt.Println("[reconnect] start a new turn to continue. otherwise, start a new run.")
			fmt.Printf("[reconnect] original prompt: %q\n", state.Prompt)
		} else {
			fmt.Printf("[reconnect] GetAgentStream failed: %v\n", err)
		}
		clearRunState()
		return
	}
	eventCh, err := agentStream.Events(ctx, agent.WithOffset(state.Offset))
	if err != nil {
		fmt.Printf("[reconnect] stream events failed: %v\n", err)
		clearRunState()
		return
	}

	fmt.Println("--- stream resumed ---")
	drainStreamEvents(ctx, agentStream, scanner, state.Prompt, state.RunID, eventCh, state.Offset)
	fmt.Println("--- stream end ---")
	fmt.Println()
}

// drainStreamEvents is the shared event loop used by both runStream and
// reconnectStream. It prints events as they arrive and:
//   - skips events at offset ≤ skipBelowOffset (deduplicate on reconnect)
//   - saves the latest offset to the state file after each persisted event
//   - clears the state file on terminal events (RUN_FINISHED, RUN_ERROR)
func drainStreamEvents(
	ctx context.Context,
	agentStream agent.AgentStream,
	scanner *bufio.Scanner,
	prompt string,
	runID string,
	eventCh <-chan agent.AgentEvent,
	skipBelowOffset int64,
) {
	streamed := false

	for ev := range eventCh {
		if ev == nil {
			continue
		}

		// Extract stream offset when available.
		var evOffset int64
		var hasOffset bool
		if ob, ok := ev.(interface{ Offset() (int64, bool) }); ok {
			evOffset, hasOffset = ob.Offset()
		}

		// Skip events already processed before the crash.
		// Events at exactly skipBelowOffset may replay — discard them too.
		if hasOffset && evOffset <= skipBelowOffset && skipBelowOffset > 0 {
			continue
		}

		// Persist the new latest offset before processing the event so that
		// a crash during the handler doesn't lose this position.
		if hasOffset {
			saveRunState(runID, evOffset, prompt)
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
			// Run is terminal — clear saved state.
			clearRunState()

		case agent.AgentEventTypeRunFinished:
			res := shared.RunResultFromFinishedEvent(ev)
			if streamed {
				fmt.Println()
			} else if res != nil && res.Content != "" {
				fmt.Println(res.Content)
			}
			shared.PrintRunFooters(res)
			// Run is terminal — clear saved state.
			clearRunState()

		default:
			continue
		}
	}
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
