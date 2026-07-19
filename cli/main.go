package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation/inmem"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/currenttime"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/echo"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/random"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/search"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/weather"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/wikipedia"
)

// version is set at link time by GoReleaser (release) or Taskfile (local build).
// Plain `go run` / `go build` without -ldflags leaves the default "dev".
var version = "dev"

const (
	exitPrompt = "Type 'exit', 'quit', or 'bye' to end the conversation."
	convID     = "interactive-agctl"
)

func main() {
	var configPath string
	var showVersion bool
	flag.StringVar(&configPath, "config", "config.yaml", "path to config file (env overrides file values)")
	flag.StringVar(&configPath, "c", "config.yaml", "alias for -config")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.BoolVar(&showVersion, "v", false, "alias for -version")
	flag.Parse()

	if showVersion {
		fmt.Printf("%s\n", version)
		return
	}

	cfg, err := LoadConfig(configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	lgr := newLogger(cfg.Logger)
	llmClient, err := NewLLMClient(cfg, lgr)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	reg := agent.NewToolRegistry()
	if err := agent.RegisterTools(reg,
		echo.New(),
		currenttime.New(),
		random.New(),
		calculator.New(),
		weather.New(),
		wikipedia.New(),
		search.New(),
	); err != nil {
		log.Fatalf("register tools: %v", err)
	}
	mcpServers, err := BuildMCPServers(cfg)
	if err != nil {
		log.Fatalf("mcp config: %v", err)
	}

	conv := inmem.NewConversation(inmem.WithMaxSize(100))

	// Single stdin reader: avoids conflict between main loop and approval handler after timeout
	lineCh := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		if err := scanner.Err(); err != nil {
			log.Printf("error reading stdin: %v", err)
		}
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	opts := []agent.Option{
		agent.WithName("agctl"),
		agent.WithSystemPrompt("You are a helpful assistant."),
		agent.WithLLMClient(llmClient),
		agent.WithStream(true),
		agent.WithToolRegistry(reg),
		agent.WithConversation(conversation.DefaultConfig(conv)),
		agent.WithLogger(lgr),
	}
	opts = append(opts, RuntimeOption(cfg)...)
	if len(mcpServers) > 0 {
		opts = append(opts,
			agent.WithMCPConfig(mcpServers),
			agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		)
	}

	a, err := agent.NewAgent(opts...)
	if err != nil {
		log.Fatal(formatNewAgentCreateErr(err))
	}
	defer a.Close()

	fmt.Println("Conversation mode. " + exitPrompt)

	var sessionUsage *agent.LLMUsage
	for {
		fmt.Print("\nYou: ")
		line, ok := <-lineCh
		if !ok {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if isExitCommand(line) {
			fmt.Println("Goodbye!")
			break
		}

		opts := &agent.AgentRunOptions{
			ConversationOptions: &agent.ConversationOptions{
				ID: convID,
			},
		}
		eventCh, err := a.Stream(context.Background(), line, opts)
		if err != nil {
			log.Printf("agent error: %v", err)
			continue
		}
		fmt.Print("assistant: ")
		var finalContent string
		var streamedContent bool
		for ev := range eventCh {
			if ev == nil {
				continue
			}
			if marksStreamDelta(ev) {
				streamedContent = true
			}
			switch ev.Type() {
			case agent.AgentEventTypeCustom:
				ce, ok := ev.(*agent.AgentCustomEvent)
				if !ok || ce == nil {
					printEvent(ev, streamedContent)
					continue
				}
				var approvalToken string
				switch ce.Name {
				case string(agent.AgentCustomEventNameToolApproval):
					apv, err := agent.ParseCustomEventApproval(ce)
					if err != nil || apv.ApprovalToken == "" {
						printEvent(ev, streamedContent)
						continue
					}
					argsLine := ""
					if len(apv.Args) > 0 {
						argsLine = fmt.Sprintf("\nArgs:\n%s\n", toolArgsJSONIndented(apv.Args))
					}
					fmt.Printf("\n--- Tool approval required ---\nSource agent: %s\nTool: %s\n%sApprove? (y/n): ",
						apv.AgentName, apv.ToolName, argsLine)
					approvalToken = apv.ApprovalToken
				case string(agent.AgentCustomEventNameSubAgentDelegation):
					dv, err := agent.ParseCustomEventDelegation(ce)
					if err != nil || dv.ApprovalToken == "" {
						printEvent(ev, streamedContent)
						continue
					}
					argsLine := ""
					if len(dv.Args) > 0 {
						argsLine = fmt.Sprintf("\nArgs:\n%s\n", toolArgsJSONIndented(dv.Args))
					}
					fmt.Printf("\n--- Sub-agent delegation required ---\nSource agent: %s\nSub-agent: %s\n%sApprove? (y/n): ",
						dv.AgentName, dv.SubAgentName, argsLine)
					approvalToken = dv.ApprovalToken
				default:
					printEvent(ev, streamedContent)
					continue
				}
				line2, ok2 := <-lineCh
				status := agent.ApprovalStatusRejected
				if ok2 && strings.TrimSpace(strings.ToLower(line2)) == "y" {
					status = agent.ApprovalStatusApproved
				}
				if err := a.OnApproval(context.Background(), approvalToken, status); err != nil {
					log.Printf("approval failed: %v", err)
				}
			default:
				printEvent(ev, streamedContent)
			}
			if ev.Type() == agent.AgentEventTypeTextMessageContent {
				if t, ok := ev.(*agent.AgentTextMessageContentEvent); ok && t.Delta != "" {
					fmt.Print(t.Delta)
				}
			}
			if res := runResultFromFinishedEvent(ev); res != nil {
				if res.Content != "" {
					finalContent = res.Content
				}
				if cfg.ShowLLMUsage {
					sessionUsage = mergeLLMUsage(sessionUsage, res.LLMUsage)
				}
			}
		}
		if finalContent != "" {
			fmt.Println()
		}
	}

	if cfg.ShowLLMUsage {
		printSessionUsageSummary(sessionUsage)
	}
}

// marksStreamDelta is true for events that stream visible assistant or reasoning text.
func marksStreamDelta(ev agent.AgentEvent) bool {
	if ev == nil {
		return false
	}
	switch ev.Type() {
	case agent.AgentEventTypeTextMessageContent, agent.AgentEventTypeReasoningMessageContent:
		return true
	default:
		return false
	}
}

// runResultFromFinishedEvent returns the typed result from RUN_FINISHED, or nil.
func runResultFromFinishedEvent(ev agent.AgentEvent) *agent.AgentRunResult {
	if ev == nil || ev.Type() != agent.AgentEventTypeRunFinished {
		return nil
	}
	fin, ok := ev.(*agent.AgentRunFinishedEvent)
	if !ok || fin == nil {
		return nil
	}
	return fin.Result
}

func printEvent(ev agent.AgentEvent, streamedContent bool) {
	if ev == nil {
		return
	}
	switch ev.Type() {
	case agent.AgentEventTypeCustom:
		return
	case agent.AgentEventTypeTextMessageStart, agent.AgentEventTypeTextMessageEnd:
		return
	case agent.AgentEventTypeTextMessageContent:
		return
	case agent.AgentEventTypeReasoningStart, agent.AgentEventTypeReasoningEnd,
		agent.AgentEventTypeReasoningMessageStart, agent.AgentEventTypeReasoningMessageEnd:
		return
	case agent.AgentEventTypeReasoningMessageContent:
		if r, ok := ev.(*agent.AgentReasoningMessageContentEvent); ok && r.Delta != "" {
			fmt.Printf("[thinking] %s", r.Delta)
		}
	case agent.AgentEventTypeToolCallStart:
		if t, ok := ev.(*agent.AgentToolCallStartEvent); ok {
			fmt.Printf("\n[tool_call] %s (%s)\n", t.ToolCallName, t.ToolCallID)
		}
	case agent.AgentEventTypeToolCallArgs:
		if t, ok := ev.(*agent.AgentToolCallArgsEvent); ok && t.Delta != "" {
			fmt.Printf("[tool_args] %s %s\n", t.ToolCallID, t.Delta)
		}
	case agent.AgentEventTypeToolCallEnd:
		return
	case agent.AgentEventTypeToolCallResult:
		if t, ok := ev.(*agent.AgentToolCallResultEvent); ok {
			fmt.Printf("[tool_result] %s: %s\n", t.ToolCallID, t.Content)
		}
	case agent.AgentEventTypeRunError:
		if re, ok := ev.(*agent.AgentRunErrorEvent); ok {
			fmt.Printf("[error] %s\n", re.Message)
		}
	case agent.AgentEventTypeRunFinished:
		if res := runResultFromFinishedEvent(ev); res != nil && res.Content != "" && !streamedContent {
			who := strings.TrimSpace(res.AgentName)
			if who == "" {
				who = "agent"
			}
			fmt.Printf("\n[%s complete] %s\n", who, res.Content)
		}
	case agent.AgentEventTypeRunStarted:
		return
	case agent.AgentEventTypeStepStarted:
		if t, ok := ev.(*agent.AgentStepStartedEvent); ok && t.StepName != "" {
			fmt.Printf("\n[step] %s (sub-agent: %s)\n", ev.Type(), t.StepName)
		} else {
			fmt.Printf("\n[step] %s\n", ev.Type())
		}
	case agent.AgentEventTypeStepFinished:
		if t, ok := ev.(*agent.AgentStepFinishedEvent); ok && t.StepName != "" {
			fmt.Printf("[step] %s (sub-agent: %s)\n", ev.Type(), t.StepName)
		} else {
			fmt.Printf("[step] %s\n", ev.Type())
		}
	default:
		//fmt.Printf("[%s] %+v\n", ev.Type(), ev)
		return
	}
}

// toolArgsJSONIndented formats non-empty tool args for the approval prompt (indented JSON).
func toolArgsJSONIndented(args map[string]any) string {
	b, err := json.MarshalIndent(args, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(b)
}

func isExitCommand(s string) bool {
	switch strings.ToLower(s) {
	case "exit", "quit", "bye":
		return true
	}
	return false
}

// mergeLLMUsage accumulates add into acc. Either argument may be nil.
func mergeLLMUsage(acc, add *agent.LLMUsage) *agent.LLMUsage {
	if add == nil {
		return acc
	}
	if acc == nil {
		cp := *add
		return &cp
	}
	return &agent.LLMUsage{
		PromptTokens:       acc.PromptTokens + add.PromptTokens,
		CompletionTokens:   acc.CompletionTokens + add.CompletionTokens,
		TotalTokens:        acc.TotalTokens + add.TotalTokens,
		CachedPromptTokens: acc.CachedPromptTokens + add.CachedPromptTokens,
		ReasoningTokens:    acc.ReasoningTokens + add.ReasoningTokens,
	}
}

// printSessionUsageSummary prints accumulated session token usage, or a short note when none was reported.
func printSessionUsageSummary(u *agent.LLMUsage) {
	if u == nil {
		fmt.Println("\n[USAGE] no token usage reported this session")
		return
	}
	fmt.Println("\n[USAGE] session total")
	fmt.Printf("  prompt_tokens:     %d\n", u.PromptTokens)
	fmt.Printf("  completion_tokens: %d\n", u.CompletionTokens)
	fmt.Printf("  total_tokens:      %d\n", u.TotalTokens)
	if u.CachedPromptTokens > 0 {
		fmt.Printf("  cached_prompt:     %d\n", u.CachedPromptTokens)
	}
	if u.ReasoningTokens > 0 {
		fmt.Printf("  reasoning_tokens:  %d\n", u.ReasoningTokens)
	}
}

func formatNewAgentCreateErr(err error) string {
	if err == nil {
		return ""
	}
	msg := fmt.Sprintf("failed to create agent: %v", err)
	if errors.Is(err, types.ErrTemporalDialTimeout) || errors.Is(err, types.ErrTemporalNamespaceCheckTimeout) {
		msg += "\n\nFor a local Temporal dev server, see temporal-setup.md at the repository root."
	}
	return msg
}
