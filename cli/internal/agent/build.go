// Package agent constructs a ready-to-run SDK agent (LLM client, tool
// registry, MCP servers, runtime, and conversation store) from agctl config.
// It is the single place chat and run assemble an *sdkagent.Agent from.
package agent

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/cli/internal/config"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	sdkagent "github.com/agenticenv/agent-sdk-go/pkg/agent"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation/inmem"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation/redis"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

// Built is a constructed agent ready to run, plus its teardown.
type Built struct {
	Agent *sdkagent.Agent
	Close func()
}

// Build constructs an agent from config (LLM, tools, MCP, runtime, conversation).
// forceAutoApprove enables AutoToolApprovalPolicy even when no MCP servers are
// configured (used by `agctl run`, which has no interactive approval prompt).
func Build(cfg *config.Config, forceAutoApprove bool) (*Built, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if err := requireLLMAPIKey(cfg); err != nil {
		return nil, err
	}
	lgr := config.NewLogger(cfg.Logger)
	llmClient, err := config.NewLLMClient(cfg, lgr)
	if err != nil {
		return nil, fmt.Errorf("create LLM client: %w", err)
	}

	reg := sdkagent.NewToolRegistry()
	if err := registerConfiguredTools(reg, cfg); err != nil {
		return nil, fmt.Errorf("register tools: %w", err)
	}

	mcpServers, err := config.BuildMCPServers(cfg)
	if err != nil {
		return nil, fmt.Errorf("mcp config: %w", err)
	}

	convCfg, convClose, err := buildConversation(cfg)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = "agctl"
	}
	systemPrompt := strings.TrimSpace(cfg.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = "You are a helpful agent. Use the tools available to you whenever they improve accuracy."
	}

	opts := []sdkagent.Option{
		sdkagent.WithName(name),
		sdkagent.WithSystemPrompt(systemPrompt),
		sdkagent.WithLLMClient(llmClient),
		sdkagent.WithToolRegistry(reg),
		sdkagent.WithConversation(convCfg),
		sdkagent.WithLogger(lgr),
	}
	opts = append(opts, config.RuntimeOption(cfg)...)
	if len(mcpServers) > 0 || forceAutoApprove {
		if len(mcpServers) > 0 {
			opts = append(opts, sdkagent.WithMCPConfig(mcpServers))
		}
		opts = append(opts, sdkagent.WithToolApprovalPolicy(sdkagent.AutoToolApprovalPolicy()))
	}

	a, err := sdkagent.NewAgent(opts...)
	if err != nil {
		if convClose != nil {
			convClose()
		}
		return nil, fmt.Errorf("failed to create agent: %w%s", err, createErrorHint(err))
	}
	return &Built{
		Agent: a,
		Close: func() {
			a.Close()
			if convClose != nil {
				convClose()
			}
		},
	}, nil
}

func buildConversation(cfg *config.Config) (conversation.Config, func(), error) {
	cc := cfg.Conversation
	if cc == nil {
		cc = &config.ConversationConfig{Store: "inmem", MaxSize: 100, Size: 20}
	}
	store := strings.ToLower(strings.TrimSpace(cc.Store))
	maxSize := cc.MaxSize
	if maxSize <= 0 {
		maxSize = 100
	}
	size := cc.Size
	if size <= 0 {
		size = conversation.DefaultSize
	}

	out := conversation.Config{
		Size:            size,
		SaveOnIteration: cc.SaveOnIteration,
	}

	switch store {
	case "", "inmem", "memory", "in-memory":
		out.Conversation = inmem.NewConversation(inmem.WithMaxSize(maxSize))
		return out, nil, nil
	case "redis":
		rc := cc.Redis
		if rc == nil {
			rc = &config.RedisConfig{Addr: "localhost:6379"}
		}
		addr := strings.TrimSpace(rc.Addr)
		if addr == "" {
			addr = "localhost:6379"
		}
		opts := []redis.Option{
			redis.WithAddr(addr),
			redis.WithMaxSize(maxSize),
			redis.WithDB(rc.DB),
		}
		if p := strings.TrimSpace(rc.Password); p != "" {
			opts = append(opts, redis.WithPassword(p))
		}
		if p := strings.TrimSpace(rc.KeyPrefix); p != "" {
			opts = append(opts, redis.WithKeyPrefix(p))
		}
		if rc.TTLHours > 0 {
			opts = append(opts, redis.WithTTL(time.Duration(rc.TTLHours)*time.Hour))
		}
		conv, err := redis.NewConversation(opts...)
		if err != nil {
			return conversation.Config{}, nil, fmt.Errorf("redis conversation: %w", err)
		}
		out.Conversation = conv
		return out, func() { _ = conv.Close() }, nil
	default:
		return conversation.Config{}, nil, fmt.Errorf("conversation.store %q: want inmem or redis", cc.Store)
	}
}

func requireLLMAPIKey(cfg *config.Config) error {
	if cfg.LLM == nil {
		return fmt.Errorf("LLM config is required")
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.LLM.Provider))
	// Local Ollama typically needs no API key; cloud/other providers do.
	if provider == string(interfaces.LLMProviderOllama) {
		return nil
	}
	if strings.TrimSpace(cfg.LLM.APIKey) != "" {
		return nil
	}
	return fmt.Errorf("LLM API key required: set AGCTL_LLM_APIKEY (or llm.apiKey via agctl config edit)")
}

// createErrorHint returns a human-readable suffix for known agent-creation
// failure causes, appended (not substituted) so the original error remains
// wrapped for errors.Is/As.
func createErrorHint(err error) string {
	if errors.Is(err, types.ErrTemporalDialTimeout) || errors.Is(err, types.ErrTemporalNamespaceCheckTimeout) {
		return "\n\nFor a local Temporal dev server, see temporal-setup.md at the repository root."
	}
	return ""
}
