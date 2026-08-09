// agent_with_reconnect demonstrates Agent.GetAgentStream: subscribe to a prior run's
// durable event log from a saved offset, simulating a mid-run crash and recovery in a
// single process. Works with Temporal and Restate (not local — no stream offsets).
//
// Temporal embeds a worker; Restate embeds the SDK endpoint. Cancelling the Events
// context only closes the subscriber-side event channel — the Stream context (and
// durable run) keep running. Do not share one cancelable ctx for Stream and Events
// when simulating a subscriber crash.
//
//	AGENT_RUNTIME=temporal go run ./agent_with_reconnect [prompt]
//	AGENT_RUNTIME=restate  go run ./agent_with_reconnect [prompt]
//
// Caller-side reconnect protocol:
//  1. On Stream start, save runID alongside your correlation key.
//  2. Track Offset() on each received event.
//  3. On restart: GetAgentStream(ctx, savedRunID), then Events(ctx, WithOffset(savedOffset)).
//  4. Events at offset ≤ savedOffset may be redelivered; discard duplicates if needed.
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
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/currenttime"
)

func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	if !cfg.UseTemporalRuntime() && !cfg.UseRestateRuntime() {
		log.Fatal("agent_with_reconnect requires AGENT_RUNTIME=temporal or AGENT_RUNTIME=restate")
	}

	reg := agent.NewToolRegistry()
	if err := agent.RegisterTools(reg, currenttime.New(), calculator.New()); err != nil {
		log.Fatalf("failed to register tools: %v", err)
	}

	opts := []agent.Option{
		agent.WithName("reconnect-agent"),
		agent.WithDescription("Agent that demonstrates durable stream reconnect"),
		agent.WithSystemPrompt("You are a helpful assistant that can tell the time and do math. Keep responses short."),
		agent.WithTimeout(5 * time.Minute),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(reg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)

	a, err := agent.NewAgent(opts...)
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

	// Stream ctx owns the agent run. Use Background so "simulate crash" does not cancel the run.
	// Events gets its own cancelable ctx (subscriber only).
	agentStream, err := a.Stream(context.Background(), prompt, nil)
	if err != nil {
		log.Fatalf("Stream failed: %v", err)
	}
	runID := agentStream.ID()

	eventsCtx, cancelEvents := context.WithCancel(context.Background())
	defer cancelEvents()

	// Step 1: save runID before consuming the channel.
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

		// Step 2: track offset on every event.
		if ob, ok := ev.(interface{ Offset() (int64, bool) }); ok {
			if off, has := ob.Offset(); has {
				lastOffset = off
				lastOffsetSet = true
			}
		}

		printEvent(ev)

		// Cancel Events after the first text chunk and leave Phase A immediately so Phase B
		// can reconnect while the durable run is still live (do not wait for channel drain /
		// RUN_FINISHED — that races short runs to "already completed").
		if ev.Type() == agent.AgentEventTypeTextMessageContent && !seenFirstTextChunk {
			seenFirstTextChunk = true
			fmt.Printf("\n=== simulated crash: saved runID=%s lastOffset=%d ===\n\n", runID, lastOffset)
			cancelEvents()
			break
		}
	}

	if !seenFirstTextChunk {
		fmt.Println("(run completed before simulated crash; demonstrating replay from offset 0)")
		lastOffset = 0
		lastOffsetSet = true
	}

	if !lastOffsetSet {
		fmt.Println("no stream offsets received; this runtime does not support reconnect (use AGENT_RUNTIME=temporal or restate)")
		return
	}

	// --- Phase B: reconnect from last seen offset (simulating process restart) ---

	fmt.Printf("=== process restart: reconnecting from offset %d ===\n\n", lastOffset)

	reconnectCtx, cancelReconnect := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelReconnect()

	// Step 3: GetAgentStream + Events(WithOffset).
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

func printEvent(ev agent.AgentEvent) {
	if ev == nil {
		return
	}

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
