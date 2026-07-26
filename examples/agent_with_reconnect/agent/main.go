// agent_with_reconnect demonstrates Agent.GetAgentStream: how to subscribe to a prior run's
// Temporal Workflow Streams event log from a saved offset, simulating a mid-run crash
// and recovery in a single process.
//
// # Single-process mode (default)
//
// No separate worker is needed. The embedded Temporal worker runs in the same process.
// Cancelling the Events context only closes the subscriber-side event channel — the
// Stream context (and Temporal workflow) keep running. Do not share one cancelable ctx
// for Stream and Events when simulating a subscriber crash. On reconnect, events from
// the saved offset are replayed and new events stream in live.
//
//	AGENT_RUNTIME=temporal go run . [prompt]
//
// # Separate worker mode (optional)
//
// Pass agent.DisableLocalWorker() and start the worker binary in a separate terminal to
// demonstrate the agent+worker split. See agent_with_worker and durable_agent for the
// full split-process story.
//
//	terminal 1:  AGENT_RUNTIME=temporal go run ./worker
//	terminal 2:  AGENT_RUNTIME=temporal go run . [prompt]
//
// # Caller-side reconnect protocol
//
//  1. On Stream start, save runID alongside your correlation key (conversationID, request ID, …).
//  2. Track the offset of each received event: ev.(interface{ Offset() (int64, bool) }).
//  3. On process restart, call agent.GetAgentStream(ctx, savedRunID), then Events(ctx, agent.WithOffset(savedOffset)).
//  4. Events at offset ≤ savedOffset are skipped; resume normal handling from new events.
//  5. Clear the saved runID on RUN_FINISHED or RUN_ERROR.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/agent_with_reconnect/opts"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	// No DisableLocalWorker() — the embedded worker runs in this process.
	// Cancelling the Events context only closes the subscriber channel; the
	// Stream context / workflow keep running so GetAgentStream can replay.
	agentOpts := opts.Common(cfg.Host, cfg.Port, cfg.Namespace, cfg.TaskQueue, llmClient, config.NewLoggerFromLogConfig(cfg))
	a, err := agent.NewAgent(agentOpts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("failed to create agent", err))
	}
	defer a.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What time is it and what is 17 * 23?"
	}
	fmt.Println("user:", prompt)
	fmt.Println()

	// --- Phase A: start the stream and consume up to the first text chunk, then cancel Events ---

	// Stream ctx owns the agent run. Use Background (or a long-lived request ctx) so "simulate
	// crash" does not CancelWorkflow. Events gets its own cancelable ctx (subscriber only).
	agentStream, err := a.Stream(context.Background(), prompt, nil)
	if err != nil {
		log.Fatalf("Stream failed: %v", err)
	}
	runID := agentStream.ID()

	eventsCtx, cancelEvents := context.WithCancel(context.Background())
	defer cancelEvents()

	// runID is available synchronously before any events arrive.
	// Step 1: save it alongside your correlation key before consuming the channel.
	eventCh, err := agentStream.Events(eventsCtx)
	if err != nil {
		log.Fatalf("stream events: %v", err)
	}
	fmt.Println(shared.RunIDLine(runID))
	fmt.Println("--- stream start (phase A: first text chunk, then simulating crash) ---")

	var lastOffset int64
	var lastOffsetSet bool
	var seenFirstTextChunk bool

	for ev := range eventCh {
		if ev == nil {
			continue
		}

		// Step 2: track offset on every event via the promoted Offset() accessor.
		// Only events from the Temporal stream carry a non-zero offset; synthetic events
		// (RUN_STARTED, RUN_FINISHED) emitted client-side by TemporalRuntime return (0, false).
		if ob, ok := ev.(interface{ Offset() (int64, bool) }); ok {
			if off, has := ob.Offset(); has {
				lastOffset = off
				lastOffsetSet = true
			}
		}

		printEvent(ev)

		// Cancel Events ctx after the first text chunk — subscriber gone; workflow keeps running.
		if ev.Type() == agent.AgentEventTypeTextMessageContent && !seenFirstTextChunk {
			seenFirstTextChunk = true
			fmt.Printf("\n=== simulated crash: saved runID=%s lastOffset=%d ===\n\n", runID, lastOffset)
			cancelEvents()
		}
	}

	if !seenFirstTextChunk {
		// Run finished before we could simulate a crash (very fast response / no text content).
		// The full replay below still demonstrates the reconnect API.
		fmt.Println("(run completed before simulated crash; demonstrating replay from offset 0)")
		lastOffset = 0
		lastOffsetSet = true
	}

	if !lastOffsetSet {
		// LocalRuntime or no events with offsets — reconnect is unsupported.
		fmt.Println("no stream offsets received; this runtime does not support reconnect (use AGENT_RUNTIME=temporal)")
		return
	}

	// --- Phase B: reconnect from last seen offset (simulating process restart) ---

	fmt.Printf("=== process restart: reconnecting from offset %d ===\n\n", lastOffset)

	// Step 3: call GetAgentStream with the saved runID, then Events with WithOffset.
	// A fresh context is used here to represent the new process.
	reconnectCtx, cancelReconnect := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelReconnect()

	// GetAgentStream resumes delivery from lastOffset, skipping already-consumed events.
	// Passing lastOffset (not lastOffset+1) here means we may receive the last already-seen
	// event once; callers can discard events at offset <= savedOffset if deduplication matters.
	agentStream, err = a.GetAgentStream(reconnectCtx, runID)
	if err != nil {
		log.Fatalf("GetAgentStream failed: %v", err)
	}
	resumeCh, err := agentStream.Events(reconnectCtx, agent.WithOffset(lastOffset))
	if err != nil {
		log.Fatalf("stream events: %v", err)
	}

	fmt.Println("--- stream resumed (phase B: events from last saved offset) ---")
	for ev := range resumeCh {
		if ev == nil {
			continue
		}
		printEvent(ev)
		if ev.Type() == agent.AgentEventTypeRunFinished {
			res := shared.RunResultFromFinishedEvent(ev)
			shared.PrintRunFooters(res)
		}
	}
	fmt.Println("--- stream end ---")
}

// printEvent renders a single event to stdout.
func printEvent(ev agent.AgentEvent) {
	if ev == nil {
		return
	}

	// Show offset when available so callers can see the values to persist.
	offsetStr := ""
	if ob, ok := ev.(interface{ Offset() (int64, bool) }); ok {
		if off, has := ob.Offset(); has {
			offsetStr = fmt.Sprintf(" [offset=%d]", off)
		}
	}

	switch ev.Type() {
	case agent.AgentEventTypeRunStarted:
		if e, ok := ev.(*agent.AgentRunStartedEvent); ok {
			fmt.Printf("[RUN_STARTED%s] runID=%s\n", offsetStr, e.RunID)
		}
	case agent.AgentEventTypeTextMessageStart:
		if e, ok := ev.(*agent.AgentTextMessageStartEvent); ok {
			fmt.Printf("[TEXT_START%s] msgID=%s\n", offsetStr, e.MessageID)
		}
	case agent.AgentEventTypeTextMessageContent:
		if e, ok := ev.(*agent.AgentTextMessageContentEvent); ok && e.Delta != "" {
			fmt.Print(e.Delta)
		}
	case agent.AgentEventTypeTextMessageEnd:
		fmt.Printf("\n[TEXT_END%s]\n", offsetStr)
	case agent.AgentEventTypeToolCallStart:
		if e, ok := ev.(*agent.AgentToolCallStartEvent); ok {
			fmt.Printf("[TOOL_START%s] %s (id=%s)\n", offsetStr, e.ToolCallName, e.ToolCallID)
		}
	case agent.AgentEventTypeToolCallArgs:
		if e, ok := ev.(*agent.AgentToolCallArgsEvent); ok && e.Delta != "" {
			fmt.Printf("[TOOL_ARGS%s] %s\n", offsetStr, e.Delta)
		}
	case agent.AgentEventTypeToolCallResult:
		if e, ok := ev.(*agent.AgentToolCallResultEvent); ok {
			fmt.Printf("[TOOL_RESULT%s] %s: %s\n", offsetStr, e.ToolCallID, e.Content)
		}
	case agent.AgentEventTypeRunFinished:
		if e, ok := ev.(*agent.AgentRunFinishedEvent); ok && e.Result != nil && e.Result.Content != "" {
			fmt.Printf("[RUN_FINISHED%s] %s\n", offsetStr, e.Result.Content)
		} else {
			fmt.Printf("[RUN_FINISHED%s]\n", offsetStr)
		}
	case agent.AgentEventTypeRunError:
		if e, ok := ev.(*agent.AgentRunErrorEvent); ok {
			fmt.Printf("[RUN_ERROR%s] %s\n", offsetStr, e.Message)
		}
	default:
		fmt.Printf("[%s%s]\n", ev.Type(), offsetStr)
	}
}
