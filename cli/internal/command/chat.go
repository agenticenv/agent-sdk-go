package command

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/agenticenv/agent-sdk-go/cli/internal/agent"
	"github.com/agenticenv/agent-sdk-go/cli/internal/config"
	"github.com/agenticenv/agent-sdk-go/cli/internal/output"
	sdkagent "github.com/agenticenv/agent-sdk-go/pkg/agent"
)

const (
	exitPrompt = "Type 'exit', 'quit', or 'bye' to end the conversation."
	convID     = "interactive-agctl"
)

// AgentOverrides are shared chat/run flags that override the merged config.
type AgentOverrides struct {
	Runtime  string `name:"runtime" help:"Execution runtime: local, temporal, or restate." env:"AGCTL_RUNTIME"`
	Provider string `name:"provider" help:"LLM provider (openai, anthropic, gemini, ...)." env:"AGCTL_LLM_PROVIDER"`
	Model    string `name:"model" help:"LLM model name." env:"AGCTL_LLM_MODEL"`
	APIKey   string `name:"api-key" help:"LLM API key." env:"AGCTL_LLM_APIKEY"`

	TemporalHost      string `name:"temporal-host" help:"Temporal server host (with --runtime temporal)." env:"AGCTL_TEMPORAL_HOST"`
	TemporalPort      int    `name:"temporal-port" help:"Temporal gRPC port (with --runtime temporal)." env:"AGCTL_TEMPORAL_PORT"`
	TemporalNamespace string `name:"temporal-namespace" help:"Temporal namespace (with --runtime temporal)." env:"AGCTL_TEMPORAL_NAMESPACE"`
	TemporalTaskQueue string `name:"temporal-task-queue" help:"Temporal task queue (with --runtime temporal)." env:"AGCTL_TEMPORAL_TASKQUEUE"`

	RestateIngressURL            string `name:"restate-ingress-url" help:"Restate ingress URL (with --runtime restate)." env:"AGCTL_RESTATE_INGRESS_URL"`
	RestateAdminURL              string `name:"restate-admin-url" help:"Restate admin URL (with --runtime restate)." env:"AGCTL_RESTATE_ADMIN_URL"`
	RestateAuthKey               string `name:"restate-auth-key" help:"Restate ingress auth key (with --runtime restate)." env:"AGCTL_RESTATE_AUTH_KEY"`
	RestateEndpointListenAddress string `name:"restate-endpoint-listen-address" help:"SDK endpoint listen address (with --runtime restate)." env:"AGCTL_RESTATE_ENDPOINT_LISTEN_ADDRESS"`
	RestateDeploymentURL         string `name:"restate-deployment-url" help:"URL Restate uses to call this process (with --runtime restate)." env:"AGCTL_RESTATE_DEPLOYMENT_URL"`

	LLMUsage bool `name:"llm-usage" help:"Print token usage summary on exit." env:"AGCTL_LLM_USAGE"`
}

// ChatCmd is `agctl chat` — interactive conversation mode.
type ChatCmd struct {
	AgentOverrides `embed:""`
}

func (c *ChatCmd) Run(cfg *config.Config) error {
	config.ApplyAgentOverrides(cfg, config.AgentOverrides{
		Runtime:                      c.Runtime,
		Provider:                     c.Provider,
		Model:                        c.Model,
		APIKey:                       c.APIKey,
		TemporalHost:                 c.TemporalHost,
		TemporalPort:                 c.TemporalPort,
		TemporalNamespace:            c.TemporalNamespace,
		TemporalTaskQueue:            c.TemporalTaskQueue,
		RestateIngressURL:            c.RestateIngressURL,
		RestateAdminURL:              c.RestateAdminURL,
		RestateAuthKey:               c.RestateAuthKey,
		RestateEndpointListenAddress: c.RestateEndpointListenAddress,
		RestateDeploymentURL:         c.RestateDeploymentURL,
		LLMUsage:                     c.LLMUsage,
	})

	built, err := agent.Build(cfg, false)
	if err != nil {
		return err
	}
	defer built.Close()
	a := built.Agent

	lineCh := make(chan string)
	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			lineCh <- scanner.Text()
		}
		if err := scanner.Err(); err != nil {
			log.Printf("error reading stdin: %v", err)
		}
		close(lineCh)
	}()

	fmt.Println("Conversation mode. " + exitPrompt)

	var sessionUsage *sdkagent.LLMUsage
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
		if output.IsExitCommand(line) {
			fmt.Println("Goodbye!")
			break
		}

		streamOpts := &sdkagent.AgentStreamOptions{
			ConversationOptions: &sdkagent.ConversationOptions{
				ID: convID,
			},
		}
		agentStream, err := a.Stream(context.Background(), line, streamOpts)
		if err != nil {
			log.Printf("agent error: %v", err)
			continue
		}
		eventCh, err := agentStream.Events(context.Background())
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
			if output.MarksStreamDelta(ev) {
				streamedContent = true
			}
			switch ev.Type() {
			case sdkagent.AgentEventTypeCustom:
				ce, ok := ev.(*sdkagent.AgentCustomEvent)
				if !ok || ce == nil {
					output.PrintEvent(ev, streamedContent)
					continue
				}
				var approvalToken string
				switch ce.Name {
				case string(sdkagent.AgentCustomEventNameToolApproval):
					apv, err := sdkagent.ParseCustomEventApproval(ce)
					if err != nil || apv.ApprovalToken == "" {
						output.PrintEvent(ev, streamedContent)
						continue
					}
					argsLine := ""
					if len(apv.Args) > 0 {
						argsLine = fmt.Sprintf("\nArgs:\n%s\n", output.ToolArgsJSONIndented(apv.Args))
					}
					fmt.Printf("\n--- Tool approval required ---\nSource agent: %s\nTool: %s\n%sApprove? (y/n): ",
						apv.AgentName, apv.ToolName, argsLine)
					approvalToken = apv.ApprovalToken
				case string(sdkagent.AgentCustomEventNameSubAgentDelegation):
					dv, err := sdkagent.ParseCustomEventDelegation(ce)
					if err != nil || dv.ApprovalToken == "" {
						output.PrintEvent(ev, streamedContent)
						continue
					}
					argsLine := ""
					if len(dv.Args) > 0 {
						argsLine = fmt.Sprintf("\nArgs:\n%s\n", output.ToolArgsJSONIndented(dv.Args))
					}
					fmt.Printf("\n--- Sub-agent delegation required ---\nSource agent: %s\nSub-agent: %s\n%sApprove? (y/n): ",
						dv.AgentName, dv.SubAgentName, argsLine)
					approvalToken = dv.ApprovalToken
				default:
					output.PrintEvent(ev, streamedContent)
					continue
				}
				line2, ok2 := <-lineCh
				status := sdkagent.ApprovalStatusRejected
				if ok2 && strings.TrimSpace(strings.ToLower(line2)) == "y" {
					status = sdkagent.ApprovalStatusApproved
				}
				if err := agentStream.Approve(context.Background(), approvalToken, status); err != nil {
					log.Printf("approval failed: %v", err)
				}
			default:
				output.PrintEvent(ev, streamedContent)
			}
			if ev.Type() == sdkagent.AgentEventTypeTextMessageContent {
				if t, ok := ev.(*sdkagent.AgentTextMessageContentEvent); ok && t.Delta != "" {
					fmt.Print(t.Delta)
				}
			}
			if res := output.RunResultFromFinishedEvent(ev); res != nil {
				if res.Content != "" {
					finalContent = res.Content
				}
				if cfg.LLMUsage {
					sessionUsage = output.MergeLLMUsage(sessionUsage, res.LLMUsage)
				}
			}
		}
		if finalContent != "" {
			fmt.Println()
		}
	}

	if cfg.LLMUsage {
		output.PrintSessionUsageSummary(sessionUsage)
	}
	return nil
}
