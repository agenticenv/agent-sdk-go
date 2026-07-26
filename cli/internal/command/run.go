package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/agenticenv/agent-sdk-go/cli/internal/agent"
	"github.com/agenticenv/agent-sdk-go/cli/internal/config"
	"github.com/agenticenv/agent-sdk-go/cli/internal/output"
	sdkagent "github.com/agenticenv/agent-sdk-go/pkg/agent"
)

// RunCmd is `agctl run` — one-shot prompt execution.
type RunCmd struct {
	Prompt         string `name:"prompt" short:"p" help:"Prompt to send to the agent."`
	AgentOverrides `embed:""`

	Args []string `arg:"" optional:"" name:"text" help:"Prompt text (alternative to --prompt)."`
}

func (c *RunCmd) Run(cfg *config.Config) error {
	prompt, err := c.resolvePrompt()
	if err != nil {
		return err
	}
	config.ApplyAgentOverrides(cfg, config.AgentOverrides{
		Runtime:           c.Runtime,
		Provider:          c.Provider,
		Model:             c.Model,
		APIKey:            c.APIKey,
		TemporalHost:      c.TemporalHost,
		TemporalPort:      c.TemporalPort,
		TemporalNamespace: c.TemporalNamespace,
		TemporalTaskQueue: c.TemporalTaskQueue,
		LLMUsage:          c.LLMUsage,
	})

	built, err := agent.Build(cfg, true)
	if err != nil {
		return err
	}
	defer built.Close()

	agentRun, err := built.Agent.Run(context.Background(), prompt, &sdkagent.AgentRunOptions{
		ConversationOptions: &sdkagent.ConversationOptions{
			ID: "agctl-run",
		},
	})
	if err != nil {
		return err
	}
	result, err := agentRun.Get(context.Background())
	if err != nil {
		return err
	}

	if result != nil && strings.TrimSpace(result.Content) != "" {
		fmt.Println(result.Content)
	}
	if cfg.LLMUsage && result != nil {
		output.PrintSessionUsageSummary(result.LLMUsage)
	}
	return nil
}

func (c *RunCmd) resolvePrompt() (string, error) {
	if p := strings.TrimSpace(c.Prompt); p != "" {
		return p, nil
	}
	if len(c.Args) > 0 {
		return strings.TrimSpace(strings.Join(c.Args, " ")), nil
	}
	st, err := os.Stdin.Stat()
	if err == nil && (st.Mode()&os.ModeCharDevice) == 0 {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		if p := strings.TrimSpace(string(b)); p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("prompt required: use --prompt, a positional argument, or pipe stdin")
}
