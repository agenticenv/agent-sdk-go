package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"log/slog"

	"github.com/agenticenv/agent-sdk-go/internal/hooks"
	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	agentruntime "github.com/agenticenv/agent-sdk-go/pkg/agent/runtime"
	"github.com/agenticenv/agent-sdk-go/pkg/conversation"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/memory"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
	"github.com/google/uuid"
)

// LLMSampling holds per-agent LLM sampling overrides. nil or zero values mean provider defaults.
// One LLM client can serve multiple agents with different sampling settings.
type LLMSampling = types.LLMSampling

// AgentMode selects interactive (default) or autonomous agent behavior. Aliases [types.AgentMode].
type AgentMode = types.AgentMode

const (
	AgentModeInteractive = types.AgentModeInteractive
	AgentModeAutonomous  = types.AgentModeAutonomous
)

// AgentToolExecutionMode selects parallel (default) or sequential tool execution. Aliases [types.AgentToolExecutionMode].
type AgentToolExecutionMode = types.AgentToolExecutionMode

const (
	AgentToolExecutionModeParallel   = types.AgentToolExecutionModeParallel
	AgentToolExecutionModeSequential = types.AgentToolExecutionModeSequential
)

// ExecutionConfig controls timeout and max attempts for one agent loop operation. Aliases [runtime.ExecutionConfig].
type ExecutionConfig = runtime.ExecutionConfig

// RetrieverMode selects how retrievers are used in a run. Aliases [types.RetrieverMode].
type RetrieverMode = types.RetrieverMode

const (
	RetrieverModeAgentic  = types.RetrieverModeAgentic
	RetrieverModePrefetch = types.RetrieverModePrefetch
	RetrieverModeHybrid   = types.RetrieverModeHybrid
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

// A2AServers maps a stable server key (e.g. "planner", "coder") to per-server A2A settings.
// Registered tool names use prefix a2a_<serverKey>_<skillID> (see [A2ATool]).
// nil or empty map means no A2A servers from configuration.
type A2AServers map[string]A2AConfig

// A2AConfig describes one A2A agent server: base URL, optional authentication, timeout, and
// optional skill filter. Zero [A2AConfig.Timeout] falls back to [defaultA2AToolTimeout] in
// the fingerprint; the actual client applies its own default from [pkg/a2a/client.BuildConfig].
type A2AConfig struct {
	// URL is the base URL used for card resolution (e.g. "https://agent.example.com").
	URL string
	// Timeout is the per-call HTTP timeout. Zero means the client default (30 s).
	Timeout time.Duration
	// Token is a bearer token injected as "Authorization: Bearer <token>".
	// Ignored (silently) when the value is whitespace-only.
	// Prefer [A2AConfig.Headers] for non-bearer schemes.
	Token string
	// Headers are extra HTTP request headers sent on every call (e.g. "X-Tenant: acme").
	// Header keys are included in the fingerprint; values are not.
	Headers map[string]string
	// SkillFilter restricts which skills from [interfaces.A2AClient.ListSkills] are registered.
	// Set either [types.A2ASkillFilter.AllowSkills] or [types.A2ASkillFilter.BlockSkills], not both.
	// Use [github.com/agenticenv/agent-sdk-go/pkg/a2a.A2ASkillFilter] for the same type in application code.
	SkillFilter types.A2ASkillFilter
	// SkipTLSVerify disables TLS certificate verification for the A2A HTTP client. Use only in development.
	SkipTLSVerify bool
}

// A2AServerConfig holds the listen address and optional authentication for the built-in A2A HTTP server.
//
// Use [WithA2ADefaultServer] to accept the defaults (localhost:9999) or
// [WithA2AServer] to supply your own values. Zero-value fields are replaced
// with their defaults when the option is applied.
type A2AServerConfig struct {
	// Hostname is the network interface the A2A HTTP server binds to.
	// Defaults to "localhost". Set to "0.0.0.0" to accept external connections.
	Hostname string
	// Port is the TCP port the A2A HTTP server listens on. Defaults to 9999.
	Port int
	// BearerTokens is an optional list of accepted static Bearer tokens.
	// When non-empty every inbound JSON-RPC request must carry an
	// "Authorization: Bearer <token>" header whose value matches at least one
	// entry; requests without a valid token are rejected with ErrUnauthenticated.
	// The well-known agent-card endpoint is always public (no auth required).
	// Tokens are compared with constant-time equality to prevent timing attacks.
	// Leave empty to run with no authentication (development / trusted networks only).
	BearerTokens []string
	// AgentCard, when non-nil, supplies optional overrides using [interfaces.A2AAgentCard]
	// (the same shape as [interfaces.A2AClient.ResolveCard]). Non-empty fields replace defaults;
	// omitted fields use agent name/description/version, tool-derived skills, and listen URL.
	// See [Agent.buildSDKAgentCard].
	AgentCard *interfaces.A2AAgentCard
}

// OTLPProtocol is an alias for [types.OTLPProtocol] re-exported from pkg/agent so callers
// do not need to import internal/types when constructing an [ObservabilityConfig].
type OTLPProtocol = types.OTLPProtocol

const (
	// OTLPProtocolGRPC selects gRPC transport for OTLP export (default).
	OTLPProtocolGRPC OTLPProtocol = types.OTLPProtocolGRPC
	// OTLPProtocolHTTP selects HTTP/protobuf transport for OTLP export.
	OTLPProtocolHTTP OTLPProtocol = types.OTLPProtocolHTTP
)

// ObservabilityConfig selects OTLP export for traces, metrics, and logs on both the client [Agent] and
// [AgentWorker] when merged at [buildAgentConfig]. It is included in [agentConfigFingerprint] so
// caller and worker processes agree on collector wiring (see [observabilityConfigFingerprint]).
//
// Use [WithObservabilityConfig] together with identical values on the process that runs workflows
// and the process that hosts activities.
//
// Timing fields (export timeout, batch timeout, metrics interval) are intentionally omitted here;
// the SDK uses [types.DefaultOTLP*] constants automatically. Use the [pkg/observability] package
// directly with [observability.BuildConfig] if you need per-field control.
type ObservabilityConfig struct {
	// Endpoint is the OTLP collector URL, e.g. "collector:4317" (gRPC) or
	// "http://collector:4318" (HTTP). Required when ObservabilityConfig is non-nil.
	Endpoint string

	// Protocol selects the OTLP wire transport. Defaults to [OTLPProtocolGRPC].
	// Must match on both the agent caller process and the worker process.
	Protocol OTLPProtocol

	// Insecure disables TLS for the OTLP connection. Use only in development or
	// on networks where the collector sits on the same trusted host.
	Insecure bool

	// DisableTraces when true skips constructing a [interfaces.Tracer] from this config during build.
	DisableTraces bool
	// DisableMetrics when true skips constructing [interfaces.Metrics] from this config during build.
	DisableMetrics bool
	// DisableLogs when true skips constructing [*observability.Logs] from this config and skips
	// OTLP log wiring tied to this config. A prior [WithLogs] injection is preserved; if it is
	// [*observability.Logs] and the default SDK logger is used, the logger is still bridged to that client.
	DisableLogs bool
}

// agentConfig holds shared configuration for Agent and AgentWorker.
//
// Option applicability:
//   - Agent only: DisableLocalWorker, WithApprovalHandler, WithTimeout, WithApprovalTimeout
//   - AgentWorker only: (none; worker inherits options passed to NewAgentWorker)
//   - Both: WithName, WithDescription, WithSystemPrompt, runtime factory via
//     pkg/agent/runtime/temporal or pkg/agent/runtime/restate,
//     WithInstanceId, WithLLMClient, WithToolApprovalPolicy, WithTools, WithToolRegistry,
//     WithMCPRegistry, WithA2ARegistry, WithSubAgentRegistry,
//     WithMaxIterations, WithBudget, WithLogger, WithLogLevel, WithConversation, WithMemory,
//     WithResponseFormat, WithLLMSampling, WithSubAgents, WithMaxSubAgentDepth,
//     WithMCPConfig, WithMCPClients, WithA2AConfig, WithA2AClients, WithRetrievers, WithRetrieverMode, WithAgentMode, WithDisableFingerprintCheck, WithAgentToolExecutionMode,
//     WithLLMExecutionConfig, WithToolAuthExecutionConfig, WithToolExecutionConfig, WithMCPExecutionConfig, WithA2AExecutionConfig,
//     WithRetrieverExecutionConfig, WithMemoryExecutionConfig, WithConversationExecutionConfig, WithSubAgentExecutionConfig,
//     WithObservabilityConfig, WithTracer, WithMetrics, WithLogs, WithHooks
//
// When [WithObservabilityConfig] is set and a signal is not disabled, [buildAgentConfig] replaces
// [WithTracer], [WithMetrics], and [WithLogs] for that signal with OTLP clients built from the config.
type agentConfig struct {
	ID                 string
	Name               string
	Description        string
	SystemPrompt       string
	runtimeFactory     agentruntime.RuntimeFactory
	factoryConflict    error // set when conflicting runtime factories are applied
	instanceId         string
	LLMClient          interfaces.LLMClient
	tools              []interfaces.Tool // staging for [WithTools]; consumed when the agent is created
	toolRegistry       ToolRegistry
	mcpRegistry        MCPRegistry
	a2aRegistry        A2ARegistry
	subAgentRegistry   SubAgentRegistry
	toolApprovalPolicy interfaces.AgentToolApprovalPolicy
	maxIterations      int
	logger             logger.Logger
	logLevel           string
	approvalHandler    types.ApprovalHandler
	timeout            time.Duration
	approvalTimeout    time.Duration            // max wait per tool approval; must be < timeout when tools require approval
	executionConfigs   runtime.ExecutionConfigs // per-operation timeout/retry overrides; zero fields use SDK defaults at resolve time

	conversationConfig *conversation.Config

	memoryConfig *memory.Config

	// responseFormat: when set, LLM requests use it; otherwise use text-only (no JSON schema).
	responseFormat *interfaces.ResponseFormat

	// llmSampling: per-agent overrides; nil = use provider defaults.
	llmSampling *LLMSampling

	// build-time flags
	disableLocalWorker bool // true when user calls DisableLocalWorker; no local worker. Agent only.
	remoteWorker       bool // true for AgentWorker: worker-side runtime (remote activities/updates).
	// break-glass: disable caller-vs-worker fingerprint guard at activity entry.
	disableFingerprintCheck bool

	// Sub-agents: direct children in [subAgentRegistry].
	subAgents        []*Agent // staging for [WithSubAgents]; consumed when the agent is created
	maxSubAgentDepth int

	// MCP: [WithMCPConfig] / [WithMCPClients] populate [mcpRegistry]; tools resolved on each run.
	mcpServers MCPServers
	mcpClients []interfaces.MCPClient

	// A2A: [WithA2AConfig] / [WithA2AClients] populate [a2aRegistry]; skills resolved on each run.
	a2aServers A2AServers
	a2aClients []interfaces.A2AClient

	// Retrievers: optional vector/document backends (e.g. Weaviate) for RAG; validated at build.
	retrievers    []interfaces.Retriever
	retrieverMode RetrieverMode

	//A2A Server: optional server config; merged at build into a2aServer (see RunA2A).
	a2aServerConfig *A2AServerConfig

	agentMode              AgentMode
	agentToolExecutionMode AgentToolExecutionMode

	// budgetConfig is the optional per-run token/cost budget. Nil means no budget enforcement.
	budgetConfig *types.BudgetConfig

	// Observability: optional OTLP config and/or injected clients. When [observabilityConfig] is
	// non-nil, [buildAgentConfig] may construct [tracer], [metrics], and [logs] via
	// [observability.NewTracer] / [observability.NewMetrics] / [observability.NewLogs]
	// unless the corresponding Disable* flag is set. The built client always overwrites any
	// prior [WithTracer] / [WithMetrics] / [WithLogs] injection (same precedence for all
	// three). When logs are enabled and no custom logger was supplied via [WithLogger], the logger
	// is rebuilt with the explicit log provider so records reach the OTLP backend without
	// relying on the global OTel LoggerProvider (typically unset in short-lived processes).
	// When [WithObservabilityConfig] is nil and the caller set [WithLogs] to [*observability.Logs]
	// alone, the same wiring runs after options (see unified OTLP logger wiring below).
	observabilityConfig *ObservabilityConfig
	tracer              interfaces.Tracer
	metrics             interfaces.Metrics
	logs                interfaces.Logs

	// Hooks: named middleware hook groups for the agent execution lifecycle.
	hooks []hooks.HookGroup
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

// WithName sets the unique identity for this agent. Applies to Agent and AgentWorker.
//
// Letters, digits, hyphens, or underscores only; max 64 characters. Two agents
// with the same name are treated as the same logical agent and will collide at
// runtime — always pick a distinct name per agent.
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

// WithInstanceId is deprecated. Use a distinct [WithName] instead. When set, the value is
// ignored after a deprecation warning at [buildAgentConfig].
//
// Deprecated: use [WithName].
func WithInstanceId(id string) Option {
	return func(c *agentConfig) { c.instanceId = id }
}

// WithAgentMode sets interactive vs autonomous mode. Applies to Agent and AgentWorker.
// Default is [AgentModeInteractive]. Included in the Temporal agent fingerprint so caller and worker must agree.
func WithAgentMode(mode AgentMode) Option {
	return func(c *agentConfig) { c.agentMode = mode }
}

// WithAgentToolExecutionMode sets the tool execution mode. Applies to Agent and AgentWorker.
// When this option is omitted, the agent uses [AgentToolExecutionModeParallel].
func WithAgentToolExecutionMode(mode AgentToolExecutionMode) Option {
	return func(c *agentConfig) { c.agentToolExecutionMode = mode }
}

// WithLLMClient sets the LLM client. Applies to Agent and AgentWorker.
func WithLLMClient(client interfaces.LLMClient) Option {
	return func(c *agentConfig) { c.LLMClient = client }
}

// WithToolApprovalPolicy sets when tools can run without approval. Applies to Agent and AgentWorker.
func WithToolApprovalPolicy(policy interfaces.AgentToolApprovalPolicy) Option {
	return func(c *agentConfig) { c.toolApprovalPolicy = policy }
}

// WithTools sets tools at agent creation. See [WithToolRegistry] to change tools later.
// Applies to Agent and AgentWorker.
func WithTools(tools ...interfaces.Tool) Option {
	return func(c *agentConfig) { c.tools = tools }
}

// WithToolRegistry sets the tool registry. Use Register and Unregister before Run or Stream.
// Applies to Agent and AgentWorker.
func WithToolRegistry(reg ToolRegistry) Option {
	return func(c *agentConfig) { c.toolRegistry = reg }
}

// WithMCPRegistry sets the MCP client registry. Use Register, RegisterClient, and Unregister before Run or Stream.
// Applies to Agent and AgentWorker.
func WithMCPRegistry(reg MCPRegistry) Option {
	return func(c *agentConfig) { c.mcpRegistry = reg }
}

// WithA2ARegistry sets the A2A client registry. Use Register, RegisterClient, and Unregister before Run or Stream.
// Applies to Agent and AgentWorker.
func WithA2ARegistry(reg A2ARegistry) Option {
	return func(c *agentConfig) { c.a2aRegistry = reg }
}

// WithSubAgentRegistry sets the sub-agent registry. Use Register and Unregister before Run or Stream.
// Applies to Agent and AgentWorker.
func WithSubAgentRegistry(reg SubAgentRegistry) Option {
	return func(c *agentConfig) { c.subAgentRegistry = reg }
}

// WithMaxIterations sets the max number of LLM rounds. Applies to Agent and AgentWorker.
func WithMaxIterations(n int) Option {
	return func(c *agentConfig) { c.maxIterations = n }
}

// WithBudget sets a per-run token and cost budget for the agent.
// Limits apply to the current run only and reset when a new run starts.
// When the agent runs nested subagents, their usage is included in the parent run totals.
//
// At least one of cfg.MaxTokens or cfg.MaxCostUSD must be non-zero.
// When cfg.MaxCostUSD is non-zero, cfg.PromptUSDPer1M and cfg.CompletionUSDPer1M must be set.
// When cfg.OnExceeded is BudgetWaitForApproval, WithApprovalHandler must also be set;
// zero ApprovalExtraTokens / ApprovalExtraCostUSD default to MaxTokens / MaxCostUSD.
// cfg.OnExceeded defaults to BudgetStopRun when not set (ApprovalExtra* are ignored).
//
// Applies to Agent and AgentWorker.
func WithBudget(cfg types.BudgetConfig) Option {
	return func(c *agentConfig) { c.budgetConfig = &cfg }
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

// WithApprovalHandler registers the approval callback once at [NewAgent] construction time for
// [Agent.Run]. Required when tools need approval. The callback receives req
// with req.Respond set; call req.Respond(Approved|Rejected). Agent only; [Agent.Stream] uses
// [AgentStream.Approve] on CUSTOM events instead.
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

// WithLLMExecutionConfig overrides timeout and/or max attempts for LLM generate and stream calls.
// Zero fields keep the SDK default (Timeout: 30m, MaxAttempts: 3). Applies to Agent and AgentWorker.
func WithLLMExecutionConfig(cfg ExecutionConfig) Option {
	return func(c *agentConfig) { c.executionConfigs.LLM = cfg }
}

// WithToolAuthExecutionConfig overrides timeout and/or max attempts for tool authorization calls.
// Zero fields keep the SDK default (Timeout: 30m, MaxAttempts: 1). Applies to Agent and AgentWorker.
func WithToolAuthExecutionConfig(cfg ExecutionConfig) Option {
	return func(c *agentConfig) { c.executionConfigs.ToolAuth = cfg }
}

// WithToolExecutionConfig overrides timeout and/or max attempts for tool execution calls.
// Zero fields keep the SDK default (Timeout: 30m, MaxAttempts: 3). Applies to Agent and AgentWorker.
func WithToolExecutionConfig(cfg ExecutionConfig) Option {
	return func(c *agentConfig) { c.executionConfigs.ToolExecute = cfg }
}

// WithMCPExecutionConfig overrides timeout and/or max attempts for MCP calls.
// Zero fields keep the SDK default (Timeout: 30m, MaxAttempts: 3). Applies to Agent and AgentWorker.
func WithMCPExecutionConfig(cfg ExecutionConfig) Option {
	return func(c *agentConfig) { c.executionConfigs.MCP = cfg }
}

// WithA2AExecutionConfig overrides timeout and/or max attempts for A2A calls.
// Zero fields keep the SDK default (Timeout: 30m, MaxAttempts: 3). Applies to Agent and AgentWorker.
func WithA2AExecutionConfig(cfg ExecutionConfig) Option {
	return func(c *agentConfig) { c.executionConfigs.A2A = cfg }
}

// WithRetrieverExecutionConfig overrides timeout and/or max attempts for retriever prefetch calls.
// Zero fields keep the SDK default (Timeout: 5m, MaxAttempts: 3). Applies to Agent and AgentWorker.
func WithRetrieverExecutionConfig(cfg ExecutionConfig) Option {
	return func(c *agentConfig) { c.executionConfigs.Retriever = cfg }
}

// WithMemoryExecutionConfig overrides timeout and/or max attempts for memory recall and store calls.
// Zero fields keep the SDK default (Timeout: 5m, MaxAttempts: 3). Applies to Agent and AgentWorker.
func WithMemoryExecutionConfig(cfg ExecutionConfig) Option {
	return func(c *agentConfig) { c.executionConfigs.Memory = cfg }
}

// WithConversationExecutionConfig overrides timeout and/or max attempts for conversation persist calls.
// Zero fields keep the SDK default (Timeout: 30s, MaxAttempts: 1). Applies to Agent and AgentWorker.
func WithConversationExecutionConfig(cfg ExecutionConfig) Option {
	return func(c *agentConfig) { c.executionConfigs.Conversation = cfg }
}

// WithSubAgentExecutionConfig overrides timeout and/or max attempts for sub-agent delegation calls.
// Zero fields keep the SDK default (MaxAttempts: 1). Zero Timeout falls back to the agent run
// timeout set via [WithTimeout] rather than a fixed default. Applies to Agent and AgentWorker.
func WithSubAgentExecutionConfig(cfg ExecutionConfig) Option {
	return func(c *agentConfig) { c.executionConfigs.SubAgent = cfg }
}

// DisableLocalWorker marks to skip local worker creation. Agent only. Use with NewAgentWorker.
func DisableLocalWorker() Option {
	return func(c *agentConfig) { c.disableLocalWorker = true }
}

// WithDisableFingerprintCheck disables caller-vs-worker fingerprint verification at activity entry.
// This option is applicable to the Temporal runtime only (pkg/agent/runtime/temporal).
// Break-glass only: keep false by default to avoid config drift across pods/workers.
// Not allowed for [NewAgentWorker] (remote worker process).
func WithDisableFingerprintCheck(disable bool) Option {
	return func(c *agentConfig) { c.disableFingerprintCheck = disable }
}

// WithConversation sets conversation history for the agent. Applies to Agent and AgentWorker.
// Pass [conversation.DefaultConfig] for SDK defaults or a custom [conversation.Config].
//
// Choose implementation based on deployment:
//   - Single process: use inmem.NewConversation
//   - Remote workers: use redis.NewConversation (in-memory cannot be used across processes)
//
// The application owns Clear on the conversation when required; the agent runtime does not call Clear.
// Agent and worker must use the same conversation config and ID when using remote workers.
func WithConversation(cfg conversation.Config) Option {
	return func(c *agentConfig) {
		c.conversationConfig = &cfg
	}
}

// WithMemory enables long-term memory for the agent. Applies to Agent and AgentWorker.
// Pass [memory.DefaultConfig] for SDK defaults or a custom [memory.Config].
// Use a shipped backend (pkg/memory/weaviate or pkg/memory/pgvector) or your own [interfaces.Memory].
// The application owns Clear on the store when required; the agent runtime does not call Clear.
func WithMemory(cfg memory.Config) Option {
	return func(c *agentConfig) {
		c.memoryConfig = &cfg
	}
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

// WithSubAgents sets sub-agents at agent creation. See [WithSubAgentRegistry] to change them later.
// Applies to Agent and AgentWorker.
func WithSubAgents(subAgents ...*Agent) Option {
	return func(c *agentConfig) { c.subAgents = subAgents }
}

// WithMaxSubAgentDepth sets the maximum sub-agent nesting depth from this agent (direct children = 1).
// Default is 2 when unset or <= 0. Applies to Agent and AgentWorker.
func WithMaxSubAgentDepth(depth int) Option {
	return func(c *agentConfig) { c.maxSubAgentDepth = depth }
}

// WithMCPConfig registers MCP servers by key. See [WithMCPRegistry] to change clients later.
// Applies to Agent and AgentWorker.
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

// WithA2AConfig registers remote A2A agents by key. See [WithA2ARegistry] to change clients later.
// Applies to Agent and AgentWorker.
func WithA2AConfig(servers A2AServers) Option {
	return func(c *agentConfig) { c.a2aServers = servers }
}

// WithA2AClients adds caller-supplied A2A clients (e.g. for custom transports or pre-built agents).
// Each client's [interfaces.A2AClient.Name] must be non-empty and unique among all A2A clients,
// including those created from [WithA2AConfig].
// Applies to Agent and AgentWorker.
func WithA2AClients(clients ...interfaces.A2AClient) Option {
	return func(c *agentConfig) {
		if len(clients) == 0 {
			c.a2aClients = nil
			return
		}
		c.a2aClients = append([]interfaces.A2AClient(nil), clients...)
	}
}

// WithRetrievers registers vector/document retrievers (e.g. [pkg/retriever/weaviate]).
// Each entry must be non-nil. Applies to Agent and AgentWorker.
func WithRetrievers(retrievers ...interfaces.Retriever) Option {
	return func(c *agentConfig) {
		if len(retrievers) == 0 {
			c.retrievers = nil
			return
		}
		c.retrievers = append([]interfaces.Retriever(nil), retrievers...)
	}
}

// WithRetrieverMode sets how retrievers participate in runs. Applies to Agent and AgentWorker.
// When omitted, [RetrieverModeAgentic] is used: retrievers are exposed as tools and the LLM
// decides when to call them. [RetrieverModeHybrid] combines pre-fetched context with agentic
// tool access. [RetrieverModePrefetch] injects context before the first LLM call without
// exposing retriever tools.
func WithRetrieverMode(mode RetrieverMode) Option {
	return func(c *agentConfig) { c.retrieverMode = mode }
}

// WithA2ADefaultServer enables the built-in A2A HTTP server with default
// settings (hostname "localhost", port 9999). Use this when you want to
// expose the agent as an A2A server without customising the listen address.
func WithA2ADefaultServer() Option {
	return func(c *agentConfig) {
		c.a2aServerConfig = &A2AServerConfig{
			Hostname: defaultA2AHostname,
			Port:     defaultA2APort,
		}
	}
}

// WithA2AServer enables the built-in A2A HTTP server with the provided
// [A2AServerConfig]. A nil config or zero-value fields fall back to the
// same defaults as [WithA2ADefaultServer] (localhost:9999).
func WithA2AServer(config *A2AServerConfig) Option {
	return func(c *agentConfig) {
		if config == nil {
			config = &A2AServerConfig{
				Hostname: defaultA2AHostname,
				Port:     defaultA2APort,
			}
		}
		c.a2aServerConfig = config
		if c.a2aServerConfig.Hostname == "" {
			c.a2aServerConfig.Hostname = defaultA2AHostname
		}
		if c.a2aServerConfig.Port == 0 {
			c.a2aServerConfig.Port = defaultA2APort
		}
	}
}

// WithObservabilityConfig sets OTLP export settings shared by the interactive/autonomous client and
// the worker. The digest of this struct participates in [agentConfigFingerprint] so Temporal
// caller and worker agree before executing activities.
//
// When non-nil, [buildAgentConfig] builds OTLP [interfaces.Tracer], [interfaces.Metrics], and
// (unless [ObservabilityConfig.DisableLogs] is true) [*observability.Logs] from this config whenever
// the corresponding Disable* flag is false. Those built clients always replace any values from
// [WithTracer], [WithMetrics], or [WithLogs] for the same signal; warnings are logged if an
// injection would be discarded. Use either observability-driven OTLP from this struct or manual
// injection — not both for the same signal.
func WithObservabilityConfig(config *ObservabilityConfig) Option {
	return func(c *agentConfig) { c.observabilityConfig = config }
}

// WithTracer supplies a [interfaces.Tracer] for use without [WithObservabilityConfig], or when
// [ObservabilityConfig.DisableTraces] is true (no OTLP tracer is built from config).
//
// When [WithObservabilityConfig] is non-nil and [ObservabilityConfig.DisableTraces] is false, any
// [WithTracer] value is ineffective: [buildAgentConfig] always replaces it with the OTLP tracer
// built from the observability config (same precedence as [WithMetrics] / [WithLogs]). A warning is
// logged if [WithTracer] had been set.
func WithTracer(tracer interfaces.Tracer) Option {
	return func(c *agentConfig) { c.tracer = tracer }
}

// WithMetrics supplies a [interfaces.Metrics] for use without [WithObservabilityConfig], or when
// [ObservabilityConfig.DisableMetrics] is true (no OTLP metrics client is built from config).
//
// When [WithObservabilityConfig] is non-nil and [ObservabilityConfig.DisableMetrics] is false, any
// [WithMetrics] value is ineffective: [buildAgentConfig] always replaces it with the OTLP metrics
// client built from the observability config (same precedence as [WithTracer] / [WithLogs]). A
// warning is logged if [WithMetrics] had been set.
func WithMetrics(metrics interfaces.Metrics) Option {
	return func(c *agentConfig) { c.metrics = metrics }
}

// WithLogs supplies an [interfaces.Logs] for OTel log export lifecycle management
// (flush on [Agent.Close] / [AgentWorker.Stop]).
//
// When [WithObservabilityConfig] is nil and the default SDK logger is used (no [WithLogger]),
// if this value is a concrete [*observability.Logs] from [observability.NewLogs], [buildAgentConfig]
// wires [logger.DefaultLoggerWithOtelProvider] to the same OpenTelemetry LoggerProvider so SDK log lines
// reach OTLP without an extra [WithLogger] call.
//
// When [WithObservabilityConfig] is non-nil and [ObservabilityConfig.DisableLogs] is false, any
// [WithLogs] value is ineffective: [buildAgentConfig] always replaces it with [*observability.Logs]
// built from the observability config (same precedence as [WithTracer] / [WithMetrics]). A warning is
// logged if [WithLogs] had been set.
func WithLogs(l interfaces.Logs) Option {
	return func(c *agentConfig) { c.logs = l }
}

// WithHooks registers a named group of middleware hooks on the agent.
// name should be unique across all WithHooks calls; it is used for Temporal fingerprinting and
// run correlation (see [RunMeta]). Can be called multiple times; each call appends a new group
// that runs independently with its own [RunMeta].HooksGroup value, preserving declaration order.
//
// Use hooks to intercept and modify agent behavior at runtime — for example guardrails and PII
// scrubbing on [BeforeLLMHook]/[AfterLLMHook], tool input scrubbing on [BeforeToolHook]/[AfterToolHook],
// retrieval filtering on [AfterRetrieveHook], and memory tenant isolation on
// [BeforeMemoryLoadHook]/[BeforeMemoryStoreHook]. See [AgentHooks] for the full list of hook
// points and common use cases.
func WithHooks(name string, agentHooks AgentHooks) Option {
	return func(c *agentConfig) {
		c.hooks = append(c.hooks, hooks.HookGroup{
			Name:  strings.TrimSpace(name),
			Hooks: agentHooks,
		})
	}
}

// mergedHooks returns all hook groups combined in registration order.
func (c *agentConfig) mergedHooks() AgentHooks {
	var out AgentHooks
	for _, g := range c.hooks {
		out = out.Merge(g.Hooks)
	}
	return out
}

// otlpLogsClientConfigured reports whether logs holds a concrete OTLP [*observability.Logs] client
// (built by the SDK or injected after [observability.NewLogs]), before [DefaultNoopLogs] fallback.
func otlpLogsClientConfigured(logs interfaces.Logs) bool {
	if logs == nil {
		return false
	}
	_, ok := logs.(*observability.Logs)
	return ok
}

// buildAgentConfig applies options, validates, and sets defaults (logger, timeouts, iterations).
// When neither Temporal nor Restate backend is set, the local in-process runtime is used.
// remoteWorker is false for Agent; NewAgentWorker sets it to true for worker-side activities.
func buildAgentConfig(opts []Option) (*agentConfig, error) {
	c := &agentConfig{remoteWorker: false, ID: uuid.New().String()}
	for _, opt := range opts {
		opt(c)
	}

	c.Name = sanitizeName(c.Name)
	if err := validateAgentName(c.Name); err != nil {
		return nil, err
	}

	c.instanceId = strings.TrimSpace(c.instanceId)

	if c.logLevel == "" {
		c.logLevel = "error"
	}

	// userProvidedLogger is true when the caller passed WithLogger; we never replace it.
	// When false we build a tentative plain stderr logger here for validation messages, then
	// replace it with an OTLP-aware one below once the log provider is available.
	userProvidedLogger := c.logger != nil
	if !userProvidedLogger {
		c.logger = logger.DefaultLogger(c.logLevel)
	}

	if c.instanceId != "" {
		c.logger.Warn(context.Background(), "WithInstanceId is deprecated and ignored — use a unique WithName for Temporal task queues and Restate service names")
	}

	if c.Description == "" {
		// auto-generate a minimal one rather than failing
		c.Description = fmt.Sprintf("%s is an AI agent.", c.Name)
		c.logger.Warn(context.Background(), "no description provided — using default for agent card; "+
			"set WithDescription() for better agent discoverability")
	}

	if c.toolApprovalPolicy == nil {
		c.toolApprovalPolicy = RequireAllToolApprovalPolicy{}
	}
	if err := c.validateHookGroups(); err != nil {
		return nil, err
	}
	// Runtime factory validation: only when the caller opts in via
	// pkg/agent/runtime/temporal or pkg/agent/runtime/restate. Otherwise local is default.
	if c.factoryConflict != nil {
		return nil, c.factoryConflict
	}
	if c.runtimeFactory != nil {
		if err := c.runtimeFactory.Validate(); err != nil {
			return nil, err
		}
	}
	// WithDisableFingerprintCheck is Temporal-only.
	if !c.hasTemporalRuntime() && c.disableFingerprintCheck {
		c.logger.Warn(context.Background(), "WithDisableFingerprintCheck has no effect on this runtime — it only applies to the Temporal runtime (pkg/agent/runtime/temporal)")
	}
	if c.LLMClient == nil {
		return nil, errors.New("LLM client is required")
	}
	if c.conversationConfig != nil {
		cfg := c.conversationConfig.WithDefaults()
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		// disableLocalWorker means the agent process does not run a worker itself, so a
		// separate worker process must be running elsewhere; conversation state can no
		// longer live in local memory and must use a distributed backend.
		if err := conversation.ValidateDistributed(cfg.Conversation, c.disableLocalWorker); err != nil {
			return nil, err
		}
		c.conversationConfig = &cfg
	}
	if c.memoryConfig != nil {
		cfg := c.memoryConfig.WithDefaults()
		if err := cfg.Validate(); err != nil {
			return nil, err
		}
		if cfg.Store.Mode == memory.StoreModeAlways && cfg.Store.Extract == nil && c.LLMClient == nil {
			c.logger.Warn(context.Background(), "memory StoreMode always requires custom Extract or LLM client — run-end store will be skipped",
				slog.String("scope", "agent"))
		}
		c.memoryConfig = &cfg
	}
	if c.agentMode == "" {
		c.agentMode = AgentModeInteractive
	}
	switch c.agentMode {
	case AgentModeInteractive, AgentModeAutonomous:
	default:
		return nil, fmt.Errorf("invalid agent mode %q: use %q or %q", c.agentMode, AgentModeInteractive, AgentModeAutonomous)
	}
	if c.agentToolExecutionMode == "" {
		c.agentToolExecutionMode = AgentToolExecutionModeParallel
	}
	switch c.agentToolExecutionMode {
	case AgentToolExecutionModeSequential, AgentToolExecutionModeParallel:
	default:
		return nil, fmt.Errorf("invalid tool execution mode %q: use %q or %q", c.agentToolExecutionMode, AgentToolExecutionModeSequential, AgentToolExecutionModeParallel)
	}
	if c.maxSubAgentDepth <= 0 {
		c.maxSubAgentDepth = defaultMaxSubAgentDepth
	}
	if err := c.buildRegistries(); err != nil {
		return nil, err
	}
	if err := validateRetrievers(c.retrievers); err != nil {
		return nil, err
	}
	mode, err := validateRetrieverMode(c.retrieverMode)
	if err != nil {
		return nil, err
	}
	c.retrieverMode = mode
	// Fail fast at NewAgent: merge registries, discover MCP/A2A tools, validate names (same path as each run).
	if _, err := c.resolveTools(context.Background()); err != nil {
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

	if c.approvalTimeout == 0 {
		c.approvalTimeout = c.timeout - 30*time.Second
	}
	if c.approvalTimeout >= c.timeout {
		return nil, fmt.Errorf("approvalTimeout (%v) must be less than agent timeout (%v)", c.approvalTimeout, c.timeout)
	}
	if c.approvalTimeout > types.MaxApprovalTimeout {
		return nil, fmt.Errorf("approvalTimeout (%v) exceeds max (%v)", c.approvalTimeout, types.MaxApprovalTimeout)
	}

	if c.maxIterations <= 0 {
		c.maxIterations = defaultMaxIterations
	}

	if err := c.validateBudget(); err != nil {
		return nil, err
	}

	// Snapshot injected OTLP clients before observability may replace them (WithTracer / WithMetrics / WithLogs).
	injectedTracerBeforeObs := c.tracer
	injectedMetricsBeforeObs := c.metrics
	injectedLogsBeforeObs := c.logs
	otelLoggerWired := false

	if c.observabilityConfig != nil {
		obsOpts := observabilityOptions(c)
		if !c.observabilityConfig.DisableLogs {
			if userProvidedLogger {
				// OTLP Logs client would be orphaned: SDK log lines never reach it unless the custom
				// logger bridges to the same LoggerProvider (e.g. DefaultLoggerWithOtelProvider).
				c.logger.Warn(context.Background(), "WithObservabilityConfig OTLP logs are not wired to a custom WithLogger — use logger.DefaultLoggerWithOtelProvider with the same LoggerProvider as your OTLP Logs client, or omit WithLogger to use the default SDK logger")
			} else {
				if injectedLogsBeforeObs != nil {
					c.logger.Warn(context.Background(), "WithLogs is ignored when WithObservabilityConfig enables OTLP logs — the SDK builds [*observability.Logs] from the observability config; remove WithLogs to silence this warning")
				}
				lp, err := observability.NewLogs(obsOpts...)
				if err != nil {
					return nil, err
				}
				c.logs = lp
				c.logger = logger.DefaultLoggerWithOtelProvider(c.logLevel, lp.Provider())
				otelLoggerWired = true
			}
		}
		if !c.observabilityConfig.DisableTraces {
			if injectedTracerBeforeObs != nil {
				c.logger.Warn(context.Background(), "WithTracer is ignored when WithObservabilityConfig enables traces — the SDK builds an OTLP tracer from the observability config; remove WithTracer to silence this warning")
			}
			tracer, err := observability.NewTracer(obsOpts...)
			if err != nil {
				return nil, err
			}
			c.tracer = tracer
		}
		if !c.observabilityConfig.DisableMetrics {
			if injectedMetricsBeforeObs != nil {
				c.logger.Warn(context.Background(), "WithMetrics is ignored when WithObservabilityConfig enables metrics — the SDK builds an OTLP metrics client from the observability config; remove WithMetrics to silence this warning")
			}
			metrics, err := observability.NewMetrics(obsOpts...)
			if err != nil {
				return nil, err
			}
			c.metrics = metrics
		}
	}

	// Without observability (or with DisableLogs), an injected [*observability.Logs] still needs the
	// default SDK logger bridged to the same LoggerProvider (mirrors the objects/ example pattern).
	if !userProvidedLogger && !otelLoggerWired {
		if ol, ok := c.logs.(*observability.Logs); ok && ol != nil {
			c.logger = logger.DefaultLoggerWithOtelProvider(c.logLevel, ol.Provider())
			otelLoggerWired = true
		}
	}

	runtimeName := "local"
	if c.runtimeFactory != nil {
		runtimeName = c.runtimeFactory.Name()
	}

	ctx := context.Background()
	c.logger.Info(ctx, "agent config built",
		slog.String("scope", "agent"),
		slog.String("name", c.Name),
		slog.String("runtime", runtimeName),
	)

	// Full config summary for troubleshooting (no sensitive values: systemPrompt, API keys).
	commonAttrs := []any{
		slog.String("scope", "agent"),
		slog.String("name", c.Name),
		slog.String("runtime", runtimeName),
		slog.Int("maxIterations", c.maxIterations),
		slog.String("agentMode", string(c.agentMode)),
		slog.String("agentToolExecutionMode", string(c.agentToolExecutionMode)),
		slog.Bool("hasApprovalHandler", c.approvalHandler != nil),
		slog.Duration("timeout", c.timeout),
		slog.Duration("approvalTimeout", c.approvalTimeout),
		slog.String("logLevel", c.logLevel),
		slog.Int("toolRegistryCount", len(c.toolRegistry.List())),
		slog.Int("mcpRegistryCount", len(c.mcpRegistry.List())),
		slog.Int("a2aRegistryCount", len(c.a2aRegistry.List())),
		slog.Int("subAgentRegistryCount", len(c.subAgentRegistry.List())),
		slog.Int("retrieverCount", len(c.retrievers)),
		slog.String("retrieverMode", string(c.retrieverMode)),
		slog.Bool("hasConversation", c.conversationConfig != nil),
		slog.Bool("hasObservability", c.observabilityConfig != nil),
		slog.Bool("enabledTracer", c.tracer != nil),
		slog.Bool("enabledMetrics", c.metrics != nil),
		slog.Bool("otlpSdkLogsExporter", otlpLogsClientConfigured(c.logs)),
		slog.Bool("otelLoggerWired", otelLoggerWired),
	}
	if c.instanceId != "" {
		commonAttrs = append(commonAttrs, slog.String("instanceId", c.instanceId))
	}
	if c.hasTemporalRuntime() {
		c.logger.Info(ctx, "agent config detail", append(commonAttrs,
			slog.Bool("disableLocalWorker", c.disableLocalWorker),
			slog.Bool("remoteWorker", c.remoteWorker),
			slog.Bool("disableFingerprintCheck", c.disableFingerprintCheck),
		)...)
	} else {
		c.logger.Info(ctx, "agent config detail", commonAttrs...)
	}

	if c.tracer == nil {
		c.tracer = observability.DefaultNoopTracer
	}
	if c.metrics == nil {
		c.metrics = observability.DefaultNoopMetrics
	}
	if c.logs == nil {
		c.logs = observability.DefaultNoopLogs
	}

	return c, nil
}

// buildRegistries wires registries from agent options during [buildAgentConfig].
func (c *agentConfig) buildRegistries() error {
	if err := c.buildToolRegistry(); err != nil {
		return err
	}
	if err := c.buildMCPRegistry(); err != nil {
		return err
	}
	if err := c.buildA2ARegistry(); err != nil {
		return err
	}
	if err := c.buildSubAgentRegistry(); err != nil {
		return err
	}
	return nil
}

func (c *agentConfig) buildToolRegistry() error {
	reg := c.toolRegistry
	if reg == nil {
		reg = NewToolRegistry()
	}
	for _, tool := range c.tools {
		if err := reg.Register(tool); err != nil {
			return fmt.Errorf("WithTools: %w", err)
		}
	}
	c.toolRegistry = reg
	c.tools = nil
	return nil
}

func (c *agentConfig) buildMCPRegistry() error {
	reg := c.mcpRegistry
	if reg == nil {
		reg = NewMCPRegistry(c.logger)
	}
	if len(c.mcpServers) > 0 {
		keys := make([]string, 0, len(c.mcpServers))
		for k := range c.mcpServers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := reg.Register(k, c.mcpServers[k]); err != nil {
				return fmt.Errorf("WithMCPConfig: %w", err)
			}
		}
	}
	for _, cl := range c.mcpClients {
		if cl == nil {
			return fmt.Errorf("WithMCPClients: mcp client must not be nil")
		}
		if err := reg.RegisterClient(cl); err != nil {
			return fmt.Errorf("WithMCPClients: %w", err)
		}
	}
	if err := validateMCPClients(reg.List()); err != nil {
		return err
	}
	c.mcpRegistry = reg
	return nil
}

func (c *agentConfig) buildA2ARegistry() error {
	reg := c.a2aRegistry
	if reg == nil {
		reg = NewA2ARegistry(c.logger)
	}
	if len(c.a2aServers) > 0 {
		keys := make([]string, 0, len(c.a2aServers))
		for k := range c.a2aServers {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := reg.Register(k, c.a2aServers[k]); err != nil {
				return fmt.Errorf("WithA2AConfig: %w", err)
			}
		}
	}
	for _, cl := range c.a2aClients {
		if cl == nil {
			return fmt.Errorf("WithA2AClients: a2a client must not be nil")
		}
		if err := reg.RegisterClient(cl); err != nil {
			return fmt.Errorf("WithA2AClients: %w", err)
		}
	}
	if err := validateA2AClients(reg.List()); err != nil {
		return err
	}
	c.a2aRegistry = reg
	return nil
}

func (c *agentConfig) buildSubAgentRegistry() error {
	reg := c.subAgentRegistry
	if reg == nil {
		reg = NewSubAgentRegistry()
	}
	for _, sa := range c.subAgents {
		if err := reg.Register(sa); err != nil {
			return fmt.Errorf("WithSubAgents: %w", err)
		}
	}
	if err := validateSubAgentRegistry(c, reg); err != nil {
		return err
	}
	c.subAgentRegistry = reg
	c.subAgents = nil
	return nil
}

// validateSubAgentRegistry checks roots and nested graph (cycles, max depth) for reg.List().
func validateSubAgentRegistry(c *agentConfig, reg SubAgentRegistry) error {
	agents := reg.List()
	if len(agents) == 0 {
		return nil
	}
	maxDepth := c.maxSubAgentDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxSubAgentDepth
	}
	seen := make(map[*Agent]struct{}, len(agents))
	seenNames := make(map[string]struct{}, len(agents))
	for _, sa := range agents {
		if sa == nil {
			return fmt.Errorf("sub-agent must not be nil")
		}
		if _, dup := seen[sa]; dup {
			return fmt.Errorf("duplicate sub-agent %q in WithSubAgents", sa.Name)
		}
		seen[sa] = struct{}{}
		if sa.Name == c.Name {
			return fmt.Errorf("sub-agent name %q must differ from root agent name", sa.Name)
		}
		toolName, err := subAgentToolName(sa.Name)
		if err != nil {
			return fmt.Errorf("WithSubAgents: %w", err)
		}
		if _, dup := seenNames[toolName]; dup {
			return fmt.Errorf("duplicate sub-agent tool name %q", toolName)
		}
		seenNames[toolName] = struct{}{}
	}
	for _, s := range agents {
		path := map[*Agent]struct{}{s: {}}
		if err := dfsSubAgentDepth(s, path, 1, maxDepth); err != nil {
			return err
		}
	}
	return nil
}

// resolveTools builds the merged tool list for one run from registries and resolution.
// Order is stable-first for prompt-cache friendliness:
// native → MCP → A2A → sub-agents → save_memory → RAG/retriever tools last.
func (c *agentConfig) resolveTools(ctx context.Context) ([]interfaces.Tool, error) {
	tools := c.toolRegistry.List()

	mcpTools, err := c.resolveMCPTools(ctx)
	if err != nil {
		return nil, err
	}
	tools = append(tools, mcpTools...)

	a2aTools, err := c.resolveA2ATools(ctx)
	if err != nil {
		return nil, err
	}
	tools = append(tools, a2aTools...)

	subAgentTools, err := c.resolveSubAgentTools()
	if err != nil {
		return nil, err
	}
	tools = append(tools, subAgentTools...)

	memoryTools, err := c.resolveMemoryTools()
	if err != nil {
		return nil, err
	}
	tools = append(tools, memoryTools...)

	retrieverTools, err := c.resolveRetrieverTools()
	if err != nil {
		return nil, err
	}
	tools = append(tools, retrieverTools...)

	if err := validateToolNames(tools); err != nil {
		return nil, err
	}
	if c.subAgentRegistry != nil {
		if err := validateSubAgentRegistry(c, c.subAgentRegistry); err != nil {
			return nil, err
		}
	}
	return tools, nil
}

// resolveMemoryTools registers save_memory when long-term memory uses on-demand store.
func (c *agentConfig) resolveMemoryTools() ([]interfaces.Tool, error) {
	if c.memoryConfig == nil {
		return nil, nil
	}
	cfg := c.memoryConfig.WithDefaults()
	if cfg.Memory == nil || cfg.Store.Mode != memory.StoreModeOnDemand {
		return nil, nil
	}
	return []interfaces.Tool{NewRegisteredMemoryTool()}, nil
}

// resolveSubAgentTools returns sub-agent delegation tools from [subAgentRegistry].
func (c *agentConfig) resolveSubAgentTools() ([]interfaces.Tool, error) {
	if c.subAgentRegistry == nil {
		return nil, nil
	}
	agents := c.subAgentRegistry.List()
	if len(agents) == 0 {
		return nil, nil
	}
	out := make([]interfaces.Tool, 0, len(agents))
	for _, sa := range agents {
		if st := NewSubAgentTool(sa); st != nil {
			out = append(out, st)
		}
	}
	return out, nil
}

func dfsSubAgentDepth(a *Agent, path map[*Agent]struct{}, depth, maxDepth int) error {
	if depth > maxDepth {
		return fmt.Errorf("sub-agent depth exceeds max (%d): at %q", maxDepth, a.Name)
	}
	if a == nil || a.subAgentRegistry == nil {
		return nil
	}
	for _, child := range a.subAgentRegistry.List() {
		if child == nil {
			return fmt.Errorf("sub-agent %q has a nil entry in sub-agent registry", a.Name)
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

// validateToolNames ensures tool names are unique across registry, MCP, A2A, retriever, and sub-agent tools.
func validateToolNames(tools []interfaces.Tool) error {
	seen := make(map[string]string, len(tools))
	for _, tool := range tools {
		if tool == nil {
			return fmt.Errorf("tool must not be nil")
		}
		name := tool.Name()
		kind := types.KindOf(tool)
		if prev, ok := seen[name]; ok {
			return fmt.Errorf("duplicate tool name %q: %s tool conflicts with an existing %s tool", name, kind, prev)
		}
		seen[name] = string(kind)
	}
	return nil
}

func (c *agentConfig) validateHookGroups() error {
	seen := make(map[string]struct{}, len(c.hooks))
	for _, g := range c.hooks {
		if g.Name == "" {
			return errors.New("WithHooks: hook group name is required")
		}
		if _, dup := seen[g.Name]; dup {
			return fmt.Errorf("WithHooks: duplicate hook group name %q", g.Name)
		}
		seen[g.Name] = struct{}{}
	}
	return nil
}

const maxAgentNameLen = 64

// sanitizeName normalizes an agent name for this release: lowercase, spaces to hyphens,
// strip characters outside [a-z0-9_-], truncate to 64. Prefer valid names at call sites;
// this helper will be removed in the next major release.
func sanitizeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range strings.ToLower(name) {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			b.WriteByte('-')
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_':
			b.WriteRune(r)
		}
		if b.Len() >= maxAgentNameLen {
			break
		}
	}
	out := b.String()
	if len(out) > maxAgentNameLen {
		out = out[:maxAgentNameLen]
	}
	return out
}

// validateAgentName requires a non-empty name of letters, digits, hyphens, or underscores, max 64.
func validateAgentName(name string) error {
	if name == "" {
		return errors.New("name is required")
	}
	if len(name) > maxAgentNameLen {
		return fmt.Errorf("name %q is invalid: max length is %d", name, maxAgentNameLen)
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_'
		if !ok {
			return fmt.Errorf("name %q is invalid: use letters, digits, hyphens, or underscores only", name)
		}
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

// runtimeAgentSpec is static agent identity wired onto the runtime at construction.
// ResponseFormat uses [agentConfig.responseFormatForLLM] so unset format defaults to text (same as LLM requests).
func (c *agentConfig) runtimeAgentSpec() runtime.AgentSpec {
	return runtime.AgentSpec{
		Name:           c.Name,
		Description:    c.Description,
		SystemPrompt:   c.SystemPrompt,
		ResponseFormat: c.responseFormatForLLM(),
	}
}

// runtimeAgentMemory maps memory config onto the runtime memory view.
func (c *agentConfig) runtimeAgentMemory() runtime.AgentMemory {
	if c.memoryConfig == nil {
		return runtime.AgentMemory{}
	}
	cfg := c.memoryConfig.WithDefaults()
	return runtime.AgentMemory{Config: &cfg}
}

// runtimeAgentSession maps conversation config onto the runtime session view.
func (c *agentConfig) runtimeAgentSession() runtime.AgentSession {
	if c.conversationConfig == nil {
		return runtime.AgentSession{}
	}
	return runtime.AgentSession{
		Conversation:                c.conversationConfig.Conversation,
		ConversationSize:            c.conversationConfig.Size,
		ConversationSaveOnIteration: c.conversationConfig.SaveOnIteration,
	}
}

// runtimeAgentConfig is static wiring copied onto the runtime at construction.
func (c *agentConfig) runtimeAgentConfig() runtime.AgentConfig {
	d := runtime.AgentConfig{
		LLM: runtime.AgentLLM{
			Client: c.LLMClient,
		},
		ToolApprovalPolicy: c.toolApprovalPolicy,
		Retrievers: runtime.AgentRetrievers{
			Retrievers: c.retrievers,
			Mode:       c.retrieverMode,
		},
		Session: c.runtimeAgentSession(),
		Memory:  c.runtimeAgentMemory(),
		Limits: runtime.AgentLimits{
			MaxIterations:   c.maxIterations,
			Timeout:         c.timeout,
			ApprovalTimeout: c.approvalTimeout,
			Budget:          c.budgetConfig,
		},
		ExecutionConfigs: c.executionConfigs,
		Hooks:            c.runtimeHookGroups(),
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

// runtimeHookGroups copies configured hook groups onto the runtime view.
func (c *agentConfig) runtimeHookGroups() []hooks.HookGroup {
	if len(c.hooks) == 0 {
		return nil
	}
	out := make([]hooks.HookGroup, len(c.hooks))
	copy(out, c.hooks)
	return out
}

// validateBudget checks that the BudgetConfig (if set) is internally consistent.
// When OnExceeded is BudgetWaitForApproval, zero ApprovalExtraTokens / ApprovalExtraCostUSD
// are filled from MaxTokens / MaxCostUSD so the tracker always uses the Extra fields for
// the watermark window. ApprovalHandler presence is NOT checked here; it is validated at
// Run() call time so that stream-only callers never need to provide a handler.
func (c *agentConfig) validateBudget() error {
	b := c.budgetConfig
	if b == nil {
		return nil
	}
	if b.MaxTokens < 0 {
		return fmt.Errorf("WithBudget: MaxTokens must be >= 0")
	}
	if b.MaxCostUSD < 0 {
		return fmt.Errorf("WithBudget: MaxCostUSD must be >= 0")
	}
	if b.PromptUSDPer1M < 0 {
		return fmt.Errorf("WithBudget: PromptUSDPer1M must be >= 0")
	}
	if b.CompletionUSDPer1M < 0 {
		return fmt.Errorf("WithBudget: CompletionUSDPer1M must be >= 0")
	}
	if b.MaxTokens == 0 && b.MaxCostUSD == 0 {
		return fmt.Errorf("WithBudget: at least one of MaxTokens or MaxCostUSD must be non-zero")
	}
	if b.MaxCostUSD > 0 && (b.PromptUSDPer1M <= 0 || b.CompletionUSDPer1M <= 0) {
		return fmt.Errorf("WithBudget: MaxCostUSD requires PromptUSDPer1M and CompletionUSDPer1M to be set")
	}
	if b.ApprovalExtraTokens < 0 {
		return fmt.Errorf("WithBudget: ApprovalExtraTokens must be >= 0")
	}
	if b.ApprovalExtraCostUSD < 0 {
		return fmt.Errorf("WithBudget: ApprovalExtraCostUSD must be >= 0")
	}
	if b.MaxApprovals < 0 {
		return fmt.Errorf("WithBudget: MaxApprovals must be >= 0")
	}
	if b.OnExceeded == "" {
		b.OnExceeded = types.BudgetStopRun
	}
	switch b.OnExceeded {
	case types.BudgetStopRun:
		if b.ApprovalExtraTokens != 0 || b.ApprovalExtraCostUSD != 0 {
			return fmt.Errorf("WithBudget: ApprovalExtraTokens and ApprovalExtraCostUSD are only valid with BudgetWaitForApproval, not BudgetStopRun")
		}
		if b.MaxApprovals != 0 {
			return fmt.Errorf("WithBudget: MaxApprovals is only valid with BudgetWaitForApproval, not BudgetStopRun")
		}
	case types.BudgetWaitForApproval:
		// ApprovalHandler is validated at Run() call time, not here, so stream-only
		// callers are not forced to provide a no-op handler.
		if b.ApprovalExtraTokens == 0 && b.MaxTokens > 0 {
			b.ApprovalExtraTokens = b.MaxTokens
		}
		if b.ApprovalExtraCostUSD == 0 && b.MaxCostUSD > 0 {
			b.ApprovalExtraCostUSD = b.MaxCostUSD
		}
		// MaxApprovals defaults are applied in BudgetTracker; no mutation needed here.
	default:
		return fmt.Errorf("WithBudget: unknown OnExceeded action %q; use BudgetStopRun or BudgetWaitForApproval", b.OnExceeded)
	}
	return nil
}

// hookGroupsFingerprint returns a stable SHA-256 digest of configured hook group names for
// [temporal.ComputeAgentFingerprint]. Names are sorted for stability. Returns "" when no hook
// groups are configured. Hook implementations are not hashed — caller and worker must register
// matching behavior under each name.
func hookGroupsFingerprint(groups []hooks.HookGroup) string {
	if len(groups) == 0 {
		return ""
	}
	names := make([]string, len(groups))
	for i, g := range groups {
		names[i] = g.Name
	}
	sort.Strings(names)
	b, err := json.Marshal(names)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type observabilityFpShot struct {
	Endpoint       string `json:"endpoint"`
	Protocol       string `json:"protocol"`
	Insecure       bool   `json:"insecure"`
	DisableTraces  bool   `json:"disable_traces"`
	DisableMetrics bool   `json:"disable_metrics"`
	DisableLogs    bool   `json:"disable_logs"`
}

// observabilityConfigFingerprint returns a stable digest for [temporal.ComputeAgentFingerprint]:
// trimmed OTLP [ObservabilityConfig.Endpoint], [ObservabilityConfig.Protocol], insecure flag,
// and disable flags. Secrets (headers) are not included.
// Empty when [ObservabilityConfig] is nil so callers without observability match workers without it.
func observabilityConfigFingerprint(obs *ObservabilityConfig) string {
	if obs == nil {
		return ""
	}
	proto := strings.TrimSpace(string(obs.Protocol))
	if proto == "" {
		proto = string(types.OTLPProtocolGRPC)
	}
	s := observabilityFpShot{
		Endpoint:       strings.TrimSpace(obs.Endpoint),
		Protocol:       proto,
		Insecure:       obs.Insecure,
		DisableTraces:  obs.DisableTraces,
		DisableMetrics: obs.DisableMetrics,
		DisableLogs:    obs.DisableLogs,
	}
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// observabilityOptions builds the [observability.Option] slice passed to [observability.NewTracer]
// and [observability.NewMetrics] from [agentConfig.observabilityConfig]. Timing fields use
// [types.DefaultOTLP*] constants. Uses [agentConfig.logger] for OTLP exporter diagnostics and
// trims endpoint / normalizes protocol the same way as [observabilityConfigFingerprint].
func observabilityOptions(c *agentConfig) []observability.Option {
	obs := c.observabilityConfig
	if obs == nil {
		return nil
	}
	endpoint := strings.TrimSpace(obs.Endpoint)
	opts := []observability.Option{
		observability.WithLogger(c.logger),
		observability.WithName(strings.TrimSpace(c.Name)),
		observability.WithEndpoint(endpoint),
		observability.WithExportTimeout(types.DefaultOTLPExportTimeout),
		observability.WithBatchTimeout(types.DefaultOTLPBatchTimeout),
		observability.WithMetricsInterval(types.DefaultOTLPMetricsInterval),
	}
	proto := strings.TrimSpace(string(obs.Protocol))
	if proto == "" {
		proto = string(types.OTLPProtocolGRPC)
	}
	opts = append(opts, observability.WithProtocol(observability.Protocol(proto)))
	if obs.Insecure {
		opts = append(opts, observability.WithInsecure(true))
	}
	return opts
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

func (c *agentConfig) requiresApproval(tool interfaces.Tool) bool {
	if c.toolApprovalPolicy == nil {
		// No policy: honor tool's ApprovalRequired
		if ar, ok := tool.(interfaces.ToolApproval); ok && ar.ApprovalRequired() {
			return true
		}
		return false
	}
	// Policy set: policy decides (can override tool default)
	return c.toolApprovalPolicy.RequiresApproval(tool)
}

func (c *agentConfig) hasApprovalTools(tools []interfaces.Tool) bool {
	for _, tool := range tools {
		if c.requiresApproval(tool) {
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
		name := strings.TrimSpace(cl.Name())
		if name == "" {
			return fmt.Errorf("mcp client name must not be empty")
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("duplicate mcp client name %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// resolveMCPTools lists tools from [mcpRegistry] clients.
func (c *agentConfig) resolveMCPTools(ctx context.Context) ([]interfaces.Tool, error) {
	if c.mcpRegistry == nil {
		return nil, nil
	}
	clients := c.mcpRegistry.List()
	if len(clients) == 0 {
		return nil, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	tools := make([]interfaces.Tool, 0)
	for _, cl := range clients {
		serverKey := strings.TrimSpace(cl.Name())
		specs, err := cl.ListTools(ctx)
		if err != nil {
			return nil, fmt.Errorf("mcp %q: list tools: %w", serverKey, err)
		}
		for _, spec := range specs {
			tools = append(tools, NewMCPTool(serverKey, spec, cl))
		}
	}
	return tools, nil
}

// validateRetrievers checks for nil entries in [WithRetrievers].
func validateRetrievers(retrievers []interfaces.Retriever) error {
	for i, r := range retrievers {
		if r == nil {
			return fmt.Errorf("retriever at index %d must not be nil", i)
		}
	}
	return nil
}

// validateRetrieverMode applies the default [RetrieverModeAgentic] when mode is empty and
// ensures mode is one of the supported values.
func validateRetrieverMode(mode RetrieverMode) (RetrieverMode, error) {
	if mode == "" {
		mode = RetrieverModeAgentic
	}
	switch mode {
	case RetrieverModeAgentic, RetrieverModePrefetch, RetrieverModeHybrid:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid retriever mode %q: use %q, %q, or %q",
			mode, RetrieverModeAgentic, RetrieverModePrefetch, RetrieverModeHybrid)
	}
}

// resolveRetrieverTools returns a [RetrieverTool] per [WithRetrievers] entry when mode is
// [RetrieverModeAgentic] or [RetrieverModeHybrid].
// [RetrieverModePrefetch] does not expose tools (context is injected before the first LLM call).
func (c *agentConfig) resolveRetrieverTools() ([]interfaces.Tool, error) {
	if c.retrieverMode != RetrieverModeAgentic && c.retrieverMode != RetrieverModeHybrid {
		return nil, nil
	}
	if len(c.retrievers) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(c.retrievers))
	tools := make([]interfaces.Tool, 0, len(c.retrievers))
	for _, retriever := range c.retrievers {
		name := strings.TrimSpace(retriever.Name())
		if name == "" {
			return nil, fmt.Errorf("retriever name must not be empty")
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("duplicate retriever name %q", name)
		}
		seen[name] = struct{}{}
		tool := NewRetrieverTool(retriever)
		if tool == nil {
			return nil, fmt.Errorf("retriever %q: failed to build tool", name)
		}
		tools = append(tools, tool)
	}
	return tools, nil
}

// validateA2AClients checks for nil clients, empty names, and duplicate [interfaces.A2AClient.Name] values.
func validateA2AClients(clients []interfaces.A2AClient) error {
	seen := make(map[string]struct{}, len(clients))
	for _, cl := range clients {
		if cl == nil {
			return fmt.Errorf("a2a client must not be nil")
		}
		name := strings.TrimSpace(cl.Name())
		if name == "" {
			return fmt.Errorf("a2a client name must not be empty")
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("duplicate a2a client name %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

// resolveA2ATools lists skills from [a2aRegistry] clients.
func (c *agentConfig) resolveA2ATools(ctx context.Context) ([]interfaces.Tool, error) {
	if c.a2aRegistry == nil {
		return nil, nil
	}
	clients := c.a2aRegistry.List()
	if len(clients) == 0 {
		return nil, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	tools := make([]interfaces.Tool, 0)
	for _, cl := range clients {
		serverKey := strings.TrimSpace(cl.Name())
		skills, err := cl.ListSkills(ctx)
		if err != nil {
			return nil, fmt.Errorf("a2a %q: list skills: %w", serverKey, err)
		}
		for _, skill := range skills {
			tools = append(tools, NewA2ATool(serverKey, interfaces.ToolSpec{
				Name:        skill.ID,
				Description: skill.Description,
			}, skill, cl))
		}
	}
	return tools, nil
}
