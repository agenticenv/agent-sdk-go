package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"log/slog"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/runtime/temporal"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	mcpclient "github.com/agenticenv/agent-sdk-go/pkg/mcp/client"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
)

// TemporalConfig holds connection settings for the Temporal-based execution runtime (host, namespace, task queue).
//
// TaskQueue is required and must be unique per agent. Use different task queues when running
// multiple agents in the same process (e.g. "my-agent-math", "my-agent-creative").
// For multiple instances of the same agent (e.g. scaled pods), use WithInstanceId() so each
// instance gets a unique queue derived as {TaskQueue}-{InstanceId}.
//
// When using DisableLocalWorker, the agent and NewAgentWorker must use the same TaskQueue (and
// same InstanceId if set) so client and worker runtimes pair correctly.
type TemporalConfig = temporal.TemporalConfig

// LLMSampling holds per-agent LLM sampling overrides. nil or zero values mean provider defaults.
// One LLM client can serve multiple agents with different sampling settings.
type LLMSampling = types.LLMSampling

// AgentMode selects interactive (default) or autonomous agent behavior. Aliases [types.AgentMode].
type AgentMode = types.AgentMode

const (
	AgentModeInteractive = types.AgentModeInteractive
	AgentModeAutonomous  = types.AgentModeAutonomous
)

// MCPServers maps a stable server key (e.g. "github", "slack") to per-server MCP settings.
// Registered tool names use prefix mcp_<serverKey>_<toolName> (see [MCPTool]).
// nil or empty map means no MCP servers from configuration.
type MCPServers map[string]MCPConfig

// MCPConfig describes one MCP server: transport, optional tool filter, and client timeouts/retries.
// Set [MCPConfig.Transport] and [MCPConfig.ToolFilter] using types from [github.com/agenticenv/agent-sdk-go/pkg/mcp] (MCPStdio, MCPStreamableHTTP, MCPToolFilter).
// Zero [MCPConfig.Timeout] and [MCPConfig.RetryAttempts] are applied in [mcpclient.BuildConfig] when the default MCP client is created ([mcpclient.NewClient]).
type MCPConfig struct {
	Transport     types.MCPTransportConfig
	ToolFilter    types.MCPToolFilter
	Timeout       time.Duration
	RetryAttempts int
}

// agentConfig holds shared configuration for Agent and AgentWorker.
//
// Option applicability:
//   - Agent only: EnableRemoteWorkers, DisableLocalWorker, WithApprovalHandler, WithTimeout, WithApprovalTimeout
//   - AgentWorker only: (none; worker inherits options passed to NewAgentWorker)
//   - Both: WithName, WithDescription, WithSystemPrompt, WithTemporalConfig, WithTemporalClient,
//     WithInstanceId, WithLLMClient, WithToolApprovalPolicy, WithTools, WithToolRegistry,
//     WithMaxIterations, WithStream, WithLogger, WithLogLevel, WithConversation, WithConversationSize,
//     WithResponseFormat, WithLLMSampling, WithSubAgents, WithMaxSubAgentDepth,
//     WithMCPConfig, WithMCPClients, WithAgentMode
type agentConfig struct {
	ID                 string
	Name               string
	Description        string
	SystemPrompt       string
	temporalConfig     *TemporalConfig
	temporalClient     client.Client
	instanceId         string
	taskQueue          string
	LLMClient          interfaces.LLMClient
	tools              []interfaces.Tool
	toolRegistry       interfaces.ToolRegistry
	toolApprovalPolicy interfaces.AgentToolApprovalPolicy
	maxIterations      int
	streamEnabled      bool
	logger             logger.Logger
	logLevel           string
	approvalHandler    types.ApprovalHandler
	timeout            time.Duration
	approvalTimeout    time.Duration // max wait per tool approval; must be < timeout when tools require approval

	conversation     interfaces.Conversation
	conversationSize int // max messages to fetch for LLM context (default 20)

	// responseFormat: when set, LLM requests use it; otherwise use text-only (no JSON schema).
	responseFormat *interfaces.ResponseFormat

	// llmSampling: per-agent overrides; nil = use provider defaults.
	llmSampling *LLMSampling

	// build-time flags
	disableLocalWorker  bool // true when user calls DisableLocalWorker; no local worker. Agent only.
	enableRemoteWorkers bool // true: run remote event path for streaming/approvals. false (default): in-process only. Agent only.
	remoteWorker        bool // true for AgentWorker: worker-side runtime (remote activities/updates).

	// Sub-agents: direct children exposed to the LLM; subAgentTools is filled by buildSubAgentTools (graph + name checks), merged in toolsList with base and MCP tools. maxSubAgentDepth caps nesting from this agent (direct children = 1; default 2 when unset or <= 0).
	subAgents        []*Agent
	subAgentTools    []interfaces.Tool
	maxSubAgentDepth int

	// MCP: optional server configs and/or explicit clients; merged at build into mcpTools (see buildMCPTools).
	mcpServers MCPServers
	mcpClients []interfaces.MCPClient
	mcpTools   []interfaces.Tool

	agentMode AgentMode
}

// Default Run/Stream deadlines when [WithTimeout] is unset: shorter for interactive sessions,
// longer for autonomous runs. Avoids blocking forever when no workers are available.
const (
	defaultTimeoutInteractive = 5 * time.Minute
	defaultTimeoutAutonomous  = 60 * time.Minute
)

const defaultMaxIterations int = 5

// Option configures an agent. See agentConfig for which options apply to Agent vs AgentWorker.
type Option func(*agentConfig)

// WithName sets the human-readable agent name (any characters; leading/trailing space trimmed on build).
// It appears in responses and streaming events as-is. Temporal workflow ID segments derived from the name
// are sanitized separately (spaces and unsafe characters become hyphens); task queue names are not derived from Name.
func WithName(name string) Option {
	return func(c *agentConfig) { c.Name = name }
}

// WithDescription sets the agent description. Applies to Agent and AgentWorker.
func WithDescription(desc string) Option {
	return func(c *agentConfig) { c.Description = desc }
}

// WithSystemPrompt sets the system prompt. Applies to Agent and AgentWorker.
func WithSystemPrompt(prompt string) Option {
	return func(c *agentConfig) { c.SystemPrompt = prompt }
}

// WithTemporalConfig sets connection options for the Temporal execution runtime. Applies to Agent and AgentWorker.
// Use either WithTemporalConfig or WithTemporalClient, not both.
func WithTemporalConfig(cfg *TemporalConfig) Option {
	return func(c *agentConfig) {
		c.temporalConfig = cfg
		c.taskQueue = cfg.TaskQueue
	}
}

// WithTemporalClient sets a pre-configured client for the Temporal execution runtime. Use when you need TLS,
// API keys, cloud endpoints, or other options not covered by [TemporalConfig].
// Task queue must still be set (via this option's taskQueue argument). Use either WithTemporalConfig or WithTemporalClient, not both.
// The agent does not close the client when Close() is called; the caller owns the lifecycle.
func WithTemporalClient(tc client.Client, taskQueue string) Option {
	return func(c *agentConfig) {
		c.temporalClient = tc
		c.taskQueue = taskQueue
	}
}

// WithInstanceId sets the instance identifier. Applies to Agent and AgentWorker.
func WithInstanceId(id string) Option {
	return func(c *agentConfig) { c.instanceId = id }
}

// WithAgentMode sets interactive vs autonomous mode. Applies to Agent and AgentWorker.
// Default is [AgentModeInteractive]. Included in the Temporal agent fingerprint so caller and worker must agree.
func WithAgentMode(mode AgentMode) Option {
	return func(c *agentConfig) { c.agentMode = mode }
}

// WithLLMClient sets the LLM client. Applies to Agent and AgentWorker.
func WithLLMClient(client interfaces.LLMClient) Option {
	return func(c *agentConfig) { c.LLMClient = client }
}

// WithToolApprovalPolicy sets when tools can run without approval. Applies to Agent and AgentWorker.
func WithToolApprovalPolicy(policy interfaces.AgentToolApprovalPolicy) Option {
	return func(c *agentConfig) { c.toolApprovalPolicy = policy }
}

// WithTools registers tools with the agent. Applies to Agent and AgentWorker.
func WithTools(tools ...interfaces.Tool) Option {
	return func(c *agentConfig) { c.tools = tools }
}

// WithToolRegistry sets a tool registry. Applies to Agent and AgentWorker.
func WithToolRegistry(reg interfaces.ToolRegistry) Option {
	return func(c *agentConfig) { c.toolRegistry = reg }
}

// WithMaxIterations sets the max number of LLM rounds. Applies to Agent and AgentWorker.
func WithMaxIterations(n int) Option {
	return func(c *agentConfig) { c.maxIterations = n }
}

// WithStream enables partial content streaming. Applies to Agent and AgentWorker.
func WithStream(enable bool) Option {
	return func(c *agentConfig) { c.streamEnabled = enable }
}

// WithLogger sets the SDK logger (structured logging with log/slog-style attributes).
// If unset, DefaultLogger is used at WithLogLevel (default "error"), writing to stderr.
// Use NoopLogger() to disable SDK logging entirely.
func WithLogger(l logger.Logger) Option {
	return func(c *agentConfig) { c.logger = l }
}

// NoopLogger returns a logger that discards all SDK log output. Use with WithLogger(NoopLogger()).
func NoopLogger() logger.Logger {
	return logger.NoopLogger()
}

// WithLogLevel sets the log level. Applies to Agent and AgentWorker.
func WithLogLevel(level string) Option {
	return func(c *agentConfig) { c.logLevel = level }
}

// WithApprovalHandler sets the approval callback for Run. Required when tools need approval.
// The callback receives req with req.Respond set; call req.Respond(Approved|Rejected). Agent only; Stream uses OnApproval on events.
func WithApprovalHandler(fn types.ApprovalHandler) Option {
	return func(c *agentConfig) { c.approvalHandler = fn }
}

// WithTimeout sets a maximum wait for Run and Stream. Agent only. Ignored by AgentWorker.
// When unset, the default is 5m for [AgentModeInteractive] and 60m for [AgentModeAutonomous].
func WithTimeout(d time.Duration) Option {
	return func(c *agentConfig) { c.timeout = d }
}

// WithApprovalTimeout sets max wait per tool approval. Must be less than agent timeout.
// Agent only. When tools require approval, used for the approval activity; defaults to timeout-30s if unset.
// Capped at maxApprovalTimeout (31 days). Validation at build: approvalTimeout < timeout.
func WithApprovalTimeout(d time.Duration) Option {
	return func(c *agentConfig) { c.approvalTimeout = d }
}

// EnableRemoteWorkers enables the runtime's remote event path (out-of-process event delivery). Agent only.
// If unset, streaming and approvals use in-process channels only.
// Required for some setups with [DisableLocalWorker] and [NewAgentWorker], and for certain approval/streaming configurations.
func EnableRemoteWorkers() Option {
	return func(c *agentConfig) { c.enableRemoteWorkers = true }
}

// DisableLocalWorker marks to skip local worker creation. Agent only. Use with NewAgentWorker.
func DisableLocalWorker() Option {
	return func(c *agentConfig) { c.disableLocalWorker = true }
}

// WithConversation sets the conversation for message history. Applies to Agent and AgentWorker.
// The user creates the conversation (inmem or redis) and passes it to the agent.
// System messages are not stored; agent SystemPrompt is used for LLM calls.
//
// Choose implementation based on deployment:
//   - Single process: use inmem.NewInMemoryConversation
//   - Remote workers: use redis.NewRedisConversation (in-memory cannot be used across processes)
//
// The user owns the conversation lifecycle. Call Clear on the conversation when appropriate
// (e.g., when ending a session). The agent never calls Clear.
// Note: Agent and worker must use the same conversation and ID when using remote workers.
func WithConversation(conv interfaces.Conversation) Option {
	return func(c *agentConfig) { c.conversation = conv }
}

// WithConversationSize sets the max messages to fetch for LLM context (default 20).
func WithConversationSize(size int) Option {
	return func(c *agentConfig) { c.conversationSize = size }
}

// WithResponseFormat sets the LLM response format (e.g. JSON with schema). Applies to Agent and AgentWorker.
// When not set, the agent uses text-only output (no response_format override).
func WithResponseFormat(rf *interfaces.ResponseFormat) Option {
	return func(c *agentConfig) { c.responseFormat = rf }
}

// WithLLMSampling sets per-agent LLM sampling overrides. Applies to Agent and AgentWorker.
// When not set, LLM clients use their provider defaults. nil fields / 0 = provider default.
// Use Reasoning (see [types.LLMReasoning]) for generic reasoning/thinking; each provider maps it.
func WithLLMSampling(s *LLMSampling) Option {
	return func(c *agentConfig) { c.llmSampling = s }
}

// WithSubAgents registers sub-agents. Each is exposed to the parent LLM as a tool (AgentTool).
// Delegation runs through the execution runtime (child run), not Tool.Execute.
// The sub-agent graph is validated at agent build: no cycles, depth <= WithMaxSubAgentDepth (default 2).
func WithSubAgents(subAgents ...*Agent) Option {
	return func(c *agentConfig) { c.subAgents = subAgents }
}

// WithMaxSubAgentDepth sets the maximum sub-agent nesting depth from this agent (direct children = 1).
// Default is 2 when unset or <= 0. Applies to Agent and AgentWorker.
func WithMaxSubAgentDepth(depth int) Option {
	return func(c *agentConfig) { c.maxSubAgentDepth = depth }
}

// WithMCPConfig registers MCP servers by stable key (used in tool names and default client naming).
// Each [MCPConfig] must set [MCPConfig.Transport] using transport types from [github.com/agenticenv/agent-sdk-go/pkg/mcp];
// the agent wires a default MCP client internally (no modelcontextprotocol/go-sdk usage in application code).
// Tools are discovered and merged with [WithMCPClients]. Applies to Agent and AgentWorker.
func WithMCPConfig(servers MCPServers) Option {
	return func(c *agentConfig) { c.mcpServers = servers }
}

// WithMCPClients adds caller-supplied MCP clients (e.g. custom transport). Each client's [interfaces.MCPClient.Name]
// must be non-empty and unique among all MCP clients, including those created from [WithMCPConfig].
// Use [mcpclient.NewClient] with [mcpclient.WithToolFilter] for the same allow/block filtering as [MCPConfig.ToolFilter] ([mcpclient.BuildConfig] validates the filter; [github.com/agenticenv/agent-sdk-go/pkg/mcp.MCPToolFilter.Apply] runs in [mcpclient.Client.ListTools]).
// Applies to Agent and AgentWorker.
func WithMCPClients(clients ...interfaces.MCPClient) Option {
	return func(c *agentConfig) {
		if len(clients) == 0 {
			c.mcpClients = nil
			return
		}
		c.mcpClients = append([]interfaces.MCPClient(nil), clients...)
	}
}

// buildAgentConfig applies options, validates, and sets defaults (logger, timeouts, iterations).
// WithTemporalConfig lets the runtime create a Temporal client from host settings; WithTemporalClient supplies a caller-owned client.
// remoteWorker is false for Agent; NewAgentWorker sets it to true for worker-side activities.
func buildAgentConfig(opts []Option) (*agentConfig, error) {
	c := &agentConfig{remoteWorker: false, ID: uuid.New().String()}
	for _, opt := range opts {
		opt(c)
	}

	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return nil, errors.New("name is required")
	}

	if c.toolApprovalPolicy == nil {
		c.toolApprovalPolicy = RequireAllToolApprovalPolicy{}
	}
	// Either TemporalConfig or TemporalClient is required, not both.
	if c.temporalConfig != nil && c.temporalClient != nil {
		return nil, errors.New("provide either WithTemporalConfig or WithTemporalClient, not both")
	}
	if c.temporalConfig == nil && c.temporalClient == nil {
		return nil, errors.New("temporal connection is required: use WithTemporalConfig or WithTemporalClient")
	}
	if c.temporalConfig != nil {
		if c.temporalConfig.TaskQueue == "" {
			return nil, errors.New("TaskQueue is required in TemporalConfig: provide a unique name per agent")
		}
	}
	if c.temporalClient != nil && c.taskQueue == "" {
		return nil, errors.New("taskQueue is required when using WithTemporalClient")
	}
	if c.LLMClient == nil {
		return nil, errors.New("LLM client is required")
	}
	if c.conversation != nil && (c.enableRemoteWorkers || c.disableLocalWorker) && !c.conversation.IsDistributed() {
		return nil, errors.New("in-memory conversation cannot be used with remote workers (DisableLocalWorker or EnableRemoteWorkers()): use distributed storage such as redis.NewRedisConversation")
	}
	if c.conversationSize <= 0 {
		c.conversationSize = 20
	}
	if c.agentMode == "" {
		c.agentMode = AgentModeInteractive
	}
	switch c.agentMode {
	case AgentModeInteractive, AgentModeAutonomous:
	default:
		return nil, fmt.Errorf("invalid agent mode %q: use %q or %q", c.agentMode, AgentModeInteractive, AgentModeAutonomous)
	}
	if c.maxSubAgentDepth <= 0 {
		c.maxSubAgentDepth = defaultMaxSubAgentDepth
	}
	if c.logLevel == "" {
		c.logLevel = "error"
	}
	if c.logger == nil {
		c.logger = logger.DefaultLogger(c.logLevel)
	}
	if err := c.buildMCPTools(); err != nil {
		return nil, err
	}
	if err := c.buildSubAgentTools(); err != nil {
		return nil, err
	}
	if err := c.validateToolNames(); err != nil {
		return nil, err
	}
	if c.timeout == 0 {
		switch c.agentMode {
		case AgentModeAutonomous:
			c.timeout = defaultTimeoutAutonomous
		default:
			c.timeout = defaultTimeoutInteractive
		}
	}

	// Validate approvalTimeout when any tool requires approval (approvalTimeout must be < timeout)
	if c.hasApprovalTools() {
		c.logger.Debug(context.Background(), "tools require approval", slog.String("scope", "agent"), slog.String("name", c.Name))
		if c.approvalTimeout == 0 {
			c.approvalTimeout = c.timeout - 30*time.Second
		}
		if c.approvalTimeout >= c.timeout {
			return nil, fmt.Errorf("approvalTimeout (%v) must be less than agent timeout (%v)", c.approvalTimeout, c.timeout)
		}
		if c.approvalTimeout > types.MaxApprovalTimeout {
			return nil, fmt.Errorf("approvalTimeout (%v) exceeds max (%v)", c.approvalTimeout, types.MaxApprovalTimeout)
		}
	}

	if c.maxIterations <= 0 {
		c.maxIterations = defaultMaxIterations
	}

	ctx := context.Background()
	c.logger.Info(ctx, "agent config built", slog.String("scope", "agent"), slog.String("name", c.Name), slog.String("taskQueue", c.taskQueue))
	// Debug: full config summary for troubleshooting (no sensitive: systemPrompt, API keys)
	c.logger.Debug(ctx, "agent config detail",
		slog.String("scope", "agent"),
		slog.String("name", c.Name),
		slog.String("taskQueue", c.taskQueue),
		slog.String("instanceId", c.instanceId),
		slog.Int("maxIterations", c.maxIterations),
		slog.Bool("streamEnabled", c.streamEnabled),
		slog.Bool("disableLocalWorker", c.disableLocalWorker),
		slog.Bool("enableRemoteWorkers", c.enableRemoteWorkers),
		slog.Bool("remoteWorker", c.remoteWorker),
		slog.String("agentMode", string(c.agentMode)),
		slog.Bool("hasApprovalHandler", c.approvalHandler != nil),
		slog.Duration("timeout", c.timeout),
		slog.Duration("approvalTimeout", c.approvalTimeout),
		slog.String("logLevel", c.logLevel),
		slog.Int("toolCount", len(c.toolsList())),
		slog.Int("subAgentToolCount", len(c.subAgentTools)),
		slog.Int("mcpToolCount", len(c.mcpTools)),
		slog.Bool("hasConversation", c.conversation != nil))
	return c, nil
}

// toolsList returns WithTools or registry tools, merged MCP tools ([mcpTools]), then [subAgentTools] from [buildSubAgentTools].
func (c *agentConfig) toolsList() []interfaces.Tool {
	var base []interfaces.Tool
	if c.toolRegistry != nil {
		base = c.toolRegistry.Tools()
	} else {
		base = c.tools
	}
	if len(c.mcpTools) > 0 {
		merged := make([]interfaces.Tool, len(base)+len(c.mcpTools))
		copy(merged, base)
		copy(merged[len(base):], c.mcpTools)
		base = merged
	}
	if len(c.subAgentTools) > 0 {
		merged := make([]interfaces.Tool, len(base)+len(c.subAgentTools))
		copy(merged, base)
		copy(merged[len(base):], c.subAgentTools)
		base = merged
	}
	return base
}

// buildSubAgentTools sets subAgentTools from [agentConfig.subAgents] using [NewSubAgentTool],
// and validates roots (non-nil, no duplicate agent pointer, no duplicate derived tool name) and the nested graph (cycles, max depth).
func (c *agentConfig) buildSubAgentTools() error {
	if len(c.subAgents) == 0 {
		c.subAgentTools = nil
		return nil
	}
	maxDepth := c.maxSubAgentDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxSubAgentDepth
	}
	seen := make(map[*Agent]struct{}, len(c.subAgents))
	seenNames := make(map[string]struct{}, len(c.subAgents))
	out := make([]interfaces.Tool, 0, len(c.subAgents))
	for _, sa := range c.subAgents {
		if sa == nil {
			return fmt.Errorf("sub-agent must not be nil")
		}
		if _, dup := seen[sa]; dup {
			return fmt.Errorf("duplicate sub-agent %q in WithSubAgents", sa.Name)
		}
		seen[sa] = struct{}{}
		n, err := subAgentToolName(sa.Name)
		if err != nil {
			return fmt.Errorf("WithSubAgents: %w", err)
		}
		if _, dup := seenNames[n]; dup {
			return fmt.Errorf("duplicate sub-agent tool name %q", n)
		}
		seenNames[n] = struct{}{}
		if st := NewSubAgentTool(sa); st != nil {
			out = append(out, st)
		}
	}
	c.subAgentTools = out
	for _, s := range c.subAgents {
		path := map[*Agent]struct{}{s: {}}
		if err := dfsSubAgentDepth(s, path, 1, maxDepth); err != nil {
			return err
		}
	}
	return nil
}

func dfsSubAgentDepth(a *Agent, path map[*Agent]struct{}, depth, maxDepth int) error {
	if depth > maxDepth {
		return fmt.Errorf("sub-agent depth exceeds max (%d): at %q", maxDepth, a.Name)
	}
	for _, child := range a.subAgents {
		if child == nil {
			return fmt.Errorf("sub-agent %q has a nil entry in WithSubAgents", a.Name)
		}
		if _, cycle := path[child]; cycle {
			return fmt.Errorf("sub-agent cycle detected involving %q and %q", a.Name, child.Name)
		}
		path[child] = struct{}{}
		if err := dfsSubAgentDepth(child, path, depth+1, maxDepth); err != nil {
			return err
		}
		delete(path, child)
	}
	return nil
}

// validateToolNames ensures tool names are unique across WithTools/registry, MCP tools, and [subAgentTools].
func (c *agentConfig) validateToolNames() error {
	var base []interfaces.Tool
	if c.toolRegistry != nil {
		base = c.toolRegistry.Tools()
	} else {
		base = c.tools
	}
	names := make(map[string]struct{})
	for _, t := range base {
		n := t.Name()
		if _, ok := names[n]; ok {
			return fmt.Errorf("duplicate tool name %q in WithTools or registry", n)
		}
		names[n] = struct{}{}
	}
	for _, t := range c.mcpTools {
		if t == nil {
			return fmt.Errorf("mcp tool must not be nil")
		}
		n := t.Name()
		if _, ok := names[n]; ok {
			return fmt.Errorf("duplicate tool name %q: MCP tool conflicts with an existing tool", n)
		}
		names[n] = struct{}{}
	}
	for _, t := range c.subAgentTools {
		if t == nil {
			return fmt.Errorf("sub-agent tool must not be nil")
		}
		n := t.Name()
		if _, ok := names[n]; ok {
			return fmt.Errorf("sub-agent tool name %q conflicts with an existing tool", n)
		}
		names[n] = struct{}{}
	}
	return nil
}

// responseFormatForLLM returns the response format for LLM requests.
// When user sets WithResponseFormat, that is used; otherwise text-only.
func (c *agentConfig) responseFormatForLLM() *interfaces.ResponseFormat {
	if c.responseFormat != nil {
		return c.responseFormat
	}
	return &interfaces.ResponseFormat{Type: interfaces.ResponseFormatText}
}

// runtimeAgentSpec matches [runtime.ExecuteRequest.AgentSpec] / [temporal.TemporalRuntimeConfig.AgentSpec].
// ResponseFormat uses [agentConfig.responseFormatForLLM] so unset format defaults to text (same as LLM requests).
func (c *agentConfig) runtimeAgentSpec() runtime.AgentSpec {
	return runtime.AgentSpec{
		Name:           c.Name,
		Description:    c.Description,
		SystemPrompt:   c.SystemPrompt,
		ResponseFormat: c.responseFormatForLLM(),
	}
}

// runtimeAgentExecution matches [runtime.ExecuteRequest.AgentExecution] / [temporal.TemporalRuntimeConfig.AgentExecution].
func (c *agentConfig) runtimeAgentExecution() runtime.AgentExecution {
	d := runtime.AgentExecution{
		LLM: runtime.AgentLLM{
			Client: c.LLMClient,
		},
		Tools: runtime.AgentTools{
			Tools:          c.toolsList(),
			Registry:       c.toolRegistry,
			ApprovalPolicy: c.toolApprovalPolicy,
		},
		Session: runtime.AgentSession{
			Conversation:     c.conversation,
			ConversationSize: c.conversationSize,
		},
		Limits: runtime.AgentLimits{
			MaxIterations:   c.maxIterations,
			Timeout:         c.timeout,
			ApprovalTimeout: c.approvalTimeout,
		},
	}
	if c.llmSampling != nil {
		d.LLM.Sampling = &runtime.LLMSampling{
			Temperature: c.llmSampling.Temperature,
			MaxTokens:   c.llmSampling.MaxTokens,
			TopP:        c.llmSampling.TopP,
			TopK:        c.llmSampling.TopK,
			Reasoning:   cloneLLMReasoning(c.llmSampling.Reasoning),
		}
	}
	return d
}

// agentConfigFingerprint hashes identity, prompts, tools, sampling, limits, approval policy,
// and MCP wiring digest (transports, timeouts, filters, extra MCP client names) for SubAgentRoute.AgentFingerprint;
// same inputs as temporal.NewTemporalRuntime agent fingerprint.
func (c *agentConfig) agentConfigFingerprint() string {
	mat := temporal.BuildAgentFingerprintPayload(
		c.runtimeAgentSpec(),
		temporal.ToolNamesFromTools(c.toolsList()),
		toolPolicyFingerprint(c.toolApprovalPolicy),
		llmSamplingRuntimeView(c.llmSampling),
		c.conversationSize,
		runtime.AgentLimits{
			MaxIterations:   c.maxIterations,
			Timeout:         c.timeout,
			ApprovalTimeout: c.approvalTimeout,
		},
		mcpConfigFingerprint(c.mcpServers, mcpExtraClientNames(c.mcpClients)),
		string(c.agentMode),
	)
	return temporal.ComputeAgentFingerprint(mat)
}

func llmSamplingRuntimeView(s *LLMSampling) *runtime.LLMSampling {
	if s == nil {
		return nil
	}
	return &runtime.LLMSampling{
		Temperature: s.Temperature,
		MaxTokens:   s.MaxTokens,
		TopP:        s.TopP,
		TopK:        s.TopK,
		Reasoning:   cloneLLMReasoning(s.Reasoning),
	}
}

func cloneLLMReasoning(r *types.LLMReasoning) *types.LLMReasoning {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

// applySamplingToRequest sets Temperature, MaxTokens, TopP, TopK on req from agent LLMSampling.
// When llmSampling is nil, nothing is set; LLM clients use their provider defaults.
func (c *agentConfig) applySamplingToRequest(req *interfaces.LLMRequest) {
	if c.llmSampling == nil {
		return
	}
	s := c.llmSampling
	if s.Temperature != nil {
		req.Temperature = s.Temperature
	}
	if s.MaxTokens > 0 {
		req.MaxTokens = s.MaxTokens
	}
	if s.TopP != nil {
		req.TopP = s.TopP
	}
	if s.TopK != nil {
		req.TopK = s.TopK
	}
	if s.Reasoning != nil {
		r := *s.Reasoning
		req.Reasoning = &r
	}
}

func (c *agentConfig) requiresApproval(t interfaces.Tool) bool {
	if c.toolApprovalPolicy == nil {
		// No policy: honor tool's ApprovalRequired
		if ar, ok := t.(interfaces.ToolApproval); ok && ar.ApprovalRequired() {
			return true
		}
		return false
	}
	// Policy set: policy decides (can override tool default)
	return c.toolApprovalPolicy.RequiresApproval(t)
}

func (c *agentConfig) hasApprovalTools() bool {
	for _, t := range c.toolsList() {
		if c.requiresApproval(t) {
			return true
		}
	}
	return false
}

// validateMCPClients checks for nil clients, empty names, and duplicate [interfaces.MCPClient.Name] values.
func validateMCPClients(clients []interfaces.MCPClient) error {
	seen := make(map[string]struct{}, len(clients))
	for _, cl := range clients {
		if cl == nil {
			return fmt.Errorf("mcp client must not be nil")
		}
		n := strings.TrimSpace(cl.Name())
		if n == "" {
			return fmt.Errorf("mcp client name must not be empty")
		}
		if _, dup := seen[n]; dup {
			return fmt.Errorf("duplicate mcp client name %q", n)
		}
		seen[n] = struct{}{}
	}
	return nil
}

// buildMCPTools merges [agentConfig.mcpServers] (default SDK client per key) with [agentConfig.mcpClients],
// validates names, lists tools from each client (tool allow/block filtering runs inside [mcpclient.Client.ListTools] when configured), and appends [MCPTool] to [agentConfig.mcpTools].
func (c *agentConfig) buildMCPTools() error {
	c.mcpTools = []interfaces.Tool{}
	if len(c.mcpServers) == 0 && len(c.mcpClients) == 0 {
		return nil
	}

	keys := make([]string, 0, len(c.mcpServers))
	for k := range c.mcpServers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	clients := make([]interfaces.MCPClient, 0, len(keys)+len(c.mcpClients))
	for _, k := range keys {
		cfg := c.mcpServers[k]
		if cfg.Transport == nil {
			return fmt.Errorf("mcp %q: Transport is required", k)
		}
		mcpOpts := []mcpclient.Option{
			mcpclient.WithLogger(c.logger),
			mcpclient.WithTimeout(cfg.Timeout),
			mcpclient.WithRetryAttempts(cfg.RetryAttempts),
			mcpclient.WithToolFilter(cfg.ToolFilter),
		}
		cl, err := mcpclient.NewClient(k, cfg.Transport, mcpOpts...)
		if err != nil {
			return fmt.Errorf("mcp %q: new client: %w", k, err)
		}
		clients = append(clients, cl)
	}
	clients = append(clients, c.mcpClients...)

	if err := validateMCPClients(clients); err != nil {
		return err
	}

	ctx := context.Background()
	var tools []interfaces.Tool
	for _, cl := range clients {
		sk := strings.TrimSpace(cl.Name())
		specs, err := cl.ListTools(ctx)
		if err != nil {
			return fmt.Errorf("mcp %q: list tools: %w", sk, err)
		}
		for _, sp := range specs {
			tools = append(tools, NewMCPTool(sk, sp, cl))
		}
	}
	c.mcpTools = tools
	return nil
}
