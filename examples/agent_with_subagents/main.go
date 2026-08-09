package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	config "github.com/agenticenv/agent-sdk-go/examples"
	"github.com/agenticenv/agent-sdk-go/examples/shared"
	"github.com/agenticenv/agent-sdk-go/pkg/agent"
	agentrestate "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime/restate"
	"github.com/agenticenv/agent-sdk-go/pkg/tools/calculator"
)

// This example demonstrates that tool approval events from a sub-agent (math-specialist)
// flow up to the main agent's Stream subscriber.
//
// Approval flow:
//  1. Main agent asks to delegate to math-specialist → CUSTOM name=delegation
//  2. math-specialist calls the calculator tool    → CUSTOM name=approval
//
// Both approvals arrive on the main agent's Stream event channel. On Restate each
// agent is independent (own listen port / AgentLoop service); when math runs as a
// sub-agent it publishes into the main agent's AgentEventLog.
func main() {
	cfg := config.LoadFromEnv()

	llmClient, err := config.NewLLMClientFromConfig(cfg)
	if err != nil {
		log.Fatalf("failed to create LLM client: %v", err)
	}

	lineCh := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		close(lineCh)
	}()

	mathReg := agent.NewToolRegistry()
	if err := mathReg.Register(calculator.New()); err != nil {
		log.Fatalf("register tools: %v", err)
	}

	mathRuntimeOpts, mainRuntimeOpts, err := runtimeOptsForSubAgents(cfg)
	if err != nil {
		log.Fatal(err)
	}

	mathAgentOpts := []agent.Option{
		agent.WithName("math-specialist"),
		agent.WithDescription("Arithmetic specialist with calculator tool."),
		agent.WithSystemPrompt("You are a math specialist. Use the calculator tool for arithmetic. Reply with a short final answer."),
		agent.WithLLMClient(llmClient),
		agent.WithToolRegistry(mathReg),
		agent.WithToolApprovalPolicy(agent.AutoToolApprovalPolicy()),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	mathAgentOpts = append(mathAgentOpts, mathRuntimeOpts...)
	mathSpecialist, err := agent.NewAgent(mathAgentOpts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("math specialist agent", err))
	}
	defer mathSpecialist.Close()

	mainAgentOpts := []agent.Option{
		agent.WithName("main-agent"),
		agent.WithDescription("General assistant."),
		agent.WithSystemPrompt(
			"You are the main assistant. For arithmetic, delegate using the math-specialist sub-agent tool. " +
				"When the specialist's answer comes back, do not stop there: continue as the main agent—give the user a concise final reply that includes the result, " +
				"then add one short sentence of your own (e.g. sanity check, related tip, or offer to help further). " +
				"Always produce visible assistant text after delegation completes.",
		),
		agent.WithLLMClient(llmClient),
		agent.WithSubAgents(mathSpecialist),
		agent.WithMaxSubAgentDepth(2),
		agent.WithLogger(config.NewLoggerFromLogConfig(cfg)),
	}
	mainAgentOpts = append(mainAgentOpts, config.ToolApprovalOptions()...)
	mainAgentOpts = append(mainAgentOpts, mainRuntimeOpts...)
	mainAgent, err := agent.NewAgent(mainAgentOpts...)
	if err != nil {
		log.Fatal(config.FormatNewAgentError("main agent", err))
	}
	defer mainAgent.Close()

	prompt := strings.Join(os.Args[1:], " ")
	if prompt == "" {
		prompt = "What is 987 multiplied by 654? After you get the exact value, say in one sentence whether that order of magnitude is typical for a quick mental estimate."
	}

	fmt.Println("user:", prompt)
	fmt.Println("All approvals (main agent delegation + sub-agent calculator) are handled here.")
	fmt.Println()

	agentStream, err := mainAgent.Stream(context.Background(), prompt, nil)
	if err != nil {
		log.Fatalf("run stream failed: %v", err)
	}
	runID := agentStream.ID()
	eventCh, err := agentStream.Events(context.Background())
	if err != nil {
		log.Fatalf("stream events: %v", err)
	}
	fmt.Println(shared.RunIDLine(runID))

	for ev := range eventCh {
		if ev == nil {
			continue
		}
		switch eventType := ev.Type(); eventType {
		case agent.AgentEventTypeStepStarted:
			if t, ok := ev.(*agent.AgentStepStartedEvent); ok && t.StepName != "" {
				fmt.Printf("[%s] %s\n", eventType, t.StepName)
			}
		case agent.AgentEventTypeStepFinished:
			if t, ok := ev.(*agent.AgentStepFinishedEvent); ok && t.StepName != "" {
				fmt.Printf("[%s] %s\n", eventType, t.StepName)
			}
		case agent.AgentEventTypeCustom:
			if tv, ok := shared.ToolApprovalValueFromEvent(ev); ok {
				argsJSON, _ := json.MarshalIndent(tv.Args, "", "  ")
				fmt.Printf("\n--- Tool approval ---\n")
				fmt.Printf("[%s] Source agent : %s\n", eventType, tv.AgentName)
				fmt.Printf("[%s] Tool         : %s\n", eventType, tv.ToolName)
				fmt.Printf("[%s] Args:\n%s\nApprove? (y/n): ", eventType, string(argsJSON))

				line, ok := <-lineCh
				approved := ok && strings.TrimSpace(strings.ToLower(line)) == "y"
				status := agent.ApprovalStatusRejected
				if approved {
					status = agent.ApprovalStatusApproved
				}
				if err := agentStream.Approve(context.Background(), tv.ApprovalToken, status); err != nil {
					fmt.Printf("[%s] approval error: %v\n", eventType, err)
				}
				continue
			}
			if dv, ok := shared.DelegationApprovalValueFromEvent(ev); ok {
				argsJSON, _ := json.MarshalIndent(dv.Args, "", "  ")
				fmt.Printf("\n--- Delegate to specialist ---\n")
				fmt.Printf("[%s] Source agent : %s\n", eventType, dv.AgentName)
				fmt.Printf("[%s] Delegate to  : %s\n", eventType, dv.SubAgentName)
				fmt.Printf("[%s] Args:\n%s\nApprove? (y/n): ", eventType, string(argsJSON))

				line, ok := <-lineCh
				approved := ok && strings.TrimSpace(strings.ToLower(line)) == "y"
				status := agent.ApprovalStatusRejected
				if approved {
					status = agent.ApprovalStatusApproved
				}
				if err := agentStream.Approve(context.Background(), dv.ApprovalToken, status); err != nil {
					fmt.Printf("[%s] approval error: %v\n", eventType, err)
				}
			}

		case agent.AgentEventTypeTextMessageContent:
			if t, ok := ev.(*agent.AgentTextMessageContentEvent); ok && t.Delta != "" {
				fmt.Printf("[%s] %s\n", eventType, t.Delta)
			}

		case agent.AgentEventTypeRunFinished:
			res := shared.RunResultFromFinishedEvent(ev)
			if res == nil {
				continue
			}
			who := strings.TrimSpace(res.AgentName)
			if who == "" {
				who = "agent"
			}
			fmt.Printf("\n[%s] [%s complete] %s\n", eventType, who, res.Content)
			shared.PrintRunFooters(res)
		}
	}
}

// runtimeOptsForSubAgents returns per-agent runtime options.
// Restate: each agent is an independent deployment — unique listen port + DeploymentURL
// (same pattern as multiple_agents). Temporal/local: shared RuntimeOption.
func runtimeOptsForSubAgents(cfg *config.Config) (mathOpts, mainOpts []agent.Option, err error) {
	if cfg == nil || !cfg.UseRestateRuntime() {
		opts := config.RuntimeOption(cfg)
		return opts, opts, nil
	}
	mathListen := cfg.Restate.EndpointListenAddress
	mainListen, err := bumpListenPort(mathListen, 1)
	if err != nil {
		return nil, nil, fmt.Errorf("restate listen address for main-agent: %w", err)
	}
	return restateRuntimeOption(cfg, mathListen), restateRuntimeOption(cfg, mainListen), nil
}

func restateRuntimeOption(cfg *config.Config, listen string) []agent.Option {
	deploy := cfg.Restate.DeploymentURL
	if deploy != "" {
		if port, err := listenPort(listen); err == nil {
			deploy = withURLPort(deploy, port)
		}
	}
	return []agent.Option{
		agentrestate.WithRestateConfig(&agentrestate.RestateConfig{
			Ingress: agentrestate.IngressConfig{
				URL:     cfg.Restate.IngressURL,
				AuthKey: cfg.Restate.AuthKey,
			},
			Endpoint: agentrestate.EndpointConfig{
				ListenAddress: listen,
				AdminURL:      cfg.Restate.AdminURL,
				DeploymentURL: deploy,
			},
			EventLog: agentrestate.EventLogConfig{DisableClear: true},
		}),
	}
}

func bumpListenPort(listen string, delta int) (string, error) {
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return "", err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(port+delta)), nil
}

func listenPort(listen string) (int, error) {
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(portStr)
}

func withURLPort(raw string, port int) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	host := u.Hostname()
	u.Host = net.JoinHostPort(host, strconv.Itoa(port))
	return u.String()
}
