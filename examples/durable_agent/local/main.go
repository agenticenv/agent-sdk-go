// Interactive streaming REPL for the durable_agent Local lab.
//
// Usage (from examples/):
//
//	go run ./durable_agent/local [initial prompt]
//
// Zero infrastructure: the local runtime is durable **by default** via durable-go —
// no external server, no client/worker split, nothing to install. Every LLM call and
// tool execution is journaled to disk (DataDir "./agent_data/local-durable-agent" by
// default, relative to the working directory this binary runs from). Kill this
// process mid-run, then restart it (same working directory) to reconnect with
// GetAgentStream — durable-go replays each already-completed step as one coalesced
// step_replayed event, then live events resume once reconnect reaches the in-flight
// step.
//
// Unlike Temporal, local's Events does not support WithOffset(n) for n > 0 (there is
// no per-token durable log to seek into — only step-level replay); every reconnect
// call omits WithOffset entirely.
//
// Set DURABILITY=off to opt out via local.DurabilityOff() and see the contrast:
// without a journal, a killed process cannot reconnect at all (ErrStreamNotFound).
//
// On startup the agent checks /tmp/durable_agent_local_runstate.json for a saved
// runID and offers reconnect.
//
// At the "you>" prompt type any message. Type "exit" or "quit" to stop.
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
	"time"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	agentlocal "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/local"
)

// stateFile is where the agent persists runID between process restarts.
// Use /tmp so it is easy to locate and does not pollute the repo.
const stateFile = "/tmp/durable_agent_local_runstate.json"

// runState holds the mid-stream identity that survives a process crash. Unlike the
// Temporal/Restate labs, there is no offset to persist here — local reconnect always
// replays full step history from the journal (see the package doc above).
type runState struct {
	RunID  string `json:"run_id"`
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

// saveRunState atomically updates stateFile with the current runID.
func saveRunState(runID, prompt string) {
	data, err := json.Marshal(runState{RunID: runID, Prompt: prompt})
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

	// DURABILITY=off opts out of the SDK's local-runtime default via
	// local.DurabilityOff() — see the contrast in scenario 5 of the README:
	// without a journal, a killed process cannot reconnect at all.
	durabilityOff := strings.EqualFold(strings.TrimSpace(os.Getenv("DURABILITY")), "off")

	opts := []agent.Option{
		agent.WithName("local-durable-agent"),
		agent.WithDescription("Single-process durable agent on the local runtime (durable-go journal, no external server)"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithTimeout(3 * time.Minute),
		agent.WithLLMClient(llmClient),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	if durabilityOff {
		// Opt out entirely: pure in-memory execution, same as pre-durability behavior.
		opts = append(opts, agentlocal.WithLocalConfig(&agentlocal.LocalConfig{
			Durability: agentlocal.DurabilityOff(),
		}))
	}
	// Durability on (the default) needs no option at all — that is the point of
	// "durable by default": omitting agentlocal.WithLocalConfig already gives you
	// a durable-go-journaled run under ./agent_data/local-durable-agent.

	a, err := agent.NewAgent(opts...)
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
			fmt.Println("durable_agent/local stopped.")
			os.Exit(0)
		case <-sigChan:
			fmt.Println("Second signal: forcing exit.")
			os.Exit(1)
		}
	}()

	// Task batch (examples:*) sets EXAMPLES_AUTO_APPROVE; manual go run leaves it unset.
	batch := strings.EqualFold(strings.TrimSpace(os.Getenv("EXAMPLES_AUTO_APPROVE")), "true")

	fmt.Println("=== durable_agent/local interactive stream ===")
	if durabilityOff {
		fmt.Println("Durability: OFF (DURABILITY=off) — pure in-memory, no journal, no reconnect after a crash.")
	} else {
		fmt.Println("Durability: ON (default) — steps are journaled under ./agent_data/local-durable-agent.")
		fmt.Println("No external server required.")
	}
	if batch {
		fmt.Println("Batch mode: one-shot stream (no REPL).")
	} else {
		fmt.Println("Kill -9 this process mid-stream, then restart (same working directory) to reconnect mid-run.")
		fmt.Println("(Ctrl+C is a graceful Close — it cancels the run instead of leaving it resumable; see README scenario 3.)")
		fmt.Println("Type 'exit' or 'quit' or 'bye' to stop.")
	}
	fmt.Println()

	// Batch: skip leftover reconnect prompts so task runs never block on y/n.
	if batch {
		clearRunState()
	}

	scanner := bufio.NewScanner(os.Stdin)

	// Check for a saved run state and offer reconnect before starting the REPL.
	if saved := loadRunState(); saved != nil {
		fmt.Printf("[reconnect] found saved run state:\n")
		fmt.Printf("  run_id : %s\n", saved.RunID)
		fmt.Printf("  prompt : %q\n", saved.Prompt)
		fmt.Print("Reconnect? [y/n]> ")
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

// runStream starts a new stream run, persisting runID on each event so a crashed
// process can resume via reconnectStream on the next startup.
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
	saveRunState(runID, prompt)
	fmt.Println(shared.RunIDLine(runID))

	fmt.Println("--- stream start ---")
	drainStreamEvents(ctx, agentStream, scanner, eventCh)
	fmt.Println("--- stream end ---")
	fmt.Println()
}

// reconnectStream resumes a prior stream.
//
// Reconnect fidelity note: on the local runtime, already-completed steps replay as
// one full message each (step-granularity, via step_replayed custom events) — not the
// original token-by-token stream, and not seekable by offset (Events never takes
// WithOffset here; that option only applies to Temporal). Once the reconnect catches
// up to the in-flight step, tokens stream live as usual.
func reconnectStream(ctx context.Context, a *agent.Agent, scanner *bufio.Scanner, state *runState) {
	fmt.Printf("[reconnect] reconnecting run_id=%s\n", state.RunID)
	fmt.Printf("[reconnect] original prompt: %q\n\n", state.Prompt)

	agentStream, err := a.GetAgentStream(ctx, state.RunID)
	if err != nil {
		if errors.Is(err, agent.ErrRunAlreadyCompleted) {
			fmt.Println("[reconnect] the run completed successfully while you were disconnected.")
			fmt.Println("[reconnect] the response was generated, but streaming events are no longer available.")
			fmt.Println("[reconnect] if conversation history is configured, the response is already saved —")
			fmt.Println("[reconnect] start a new turn to continue. otherwise, start a new run.")
			fmt.Printf("[reconnect] original prompt: %q\n", state.Prompt)
		} else if errors.Is(err, agent.ErrStreamNotFound) {
			fmt.Println("[reconnect] ErrStreamNotFound — no journal for this run.")
			fmt.Println("[reconnect] this is expected if DURABILITY=off was set for the crashed process,")
			fmt.Println("[reconnect] or if ./agent_data/local-durable-agent was deleted/moved before restart.")
		} else {
			fmt.Printf("[reconnect] GetAgentStream failed: %v\n", err)
		}
		clearRunState()
		return
	}
	// No WithOffset: local's stream handle only supports fromOffset 0. Reconnect
	// always replays the full step history, then continues live from there.
	eventCh, err := agentStream.Events(ctx)
	if err != nil {
		fmt.Printf("[reconnect] stream events failed: %v\n", err)
		clearRunState()
		return
	}

	fmt.Println("--- stream resumed ---")
	drainStreamEvents(ctx, agentStream, scanner, eventCh)
	fmt.Println("--- stream end ---")
	fmt.Println()
}

// drainStreamEvents is the shared event loop used by both runStream and
// reconnectStream. It prints events as they arrive, including step_replayed markers
// during a reconnect's replay phase, and clears the state file on terminal events
// (RUN_FINISHED, RUN_ERROR).
func drainStreamEvents(
	ctx context.Context,
	agentStream agent.AgentStream,
	scanner *bufio.Scanner,
	eventCh <-chan agent.AgentEvent,
) {
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
			} else if ce, ok := ev.(*agent.AgentCustomEvent); ok && ce.Name == string(agent.AgentCustomEventNameStepReplayed) {
				if v, err := agent.ParseCustomEventStepReplayed(ce); err == nil {
					fmt.Printf("\n[step_replayed] step=%s status=%s — a previously completed step replayed as one coalesced message, not the original tokens\n", v.StepID, v.Status)
				}
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
