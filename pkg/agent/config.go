package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"log/slog"

	"github.com/agenticenv/agent-sdk-go/internal/runtime/temporal"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
)

// TemporalConfig holds Temporal connection settings (Host, Port, Namespace) and the task queue name.
//
// TaskQueue is required and must be unique per agent. Use different TaskQueues when running
// multiple agents in the same process (e.g. "my-agent-math", "my-agent-creative").
// For multiple instances of the same agent (e.g. scaled pods), use WithInstanceId() so each
// instance gets a unique queue derived as {TaskQueue}-{InstanceId}.
//
// When using DisableLocalWorker, the agent and NewAgentWorker must use the same TaskQueue (and
// same InstanceId if set) so they pair correctly.
type TemporalConfig struct {
	Host      string
	Port      int
	Namespace string
	TaskQueue string // Required. Full task queue name. Unique per agent.
}

// LLMSampling holds per-agent LLM sampling overrides. nil/0 = provider default.
// One LLM client can serve multiple agents with different sampling.
type LLMSampling struct {
	Temperature *float64 // 0-2 OpenAI, 0-1 Anthropic
	MaxTokens   int      // 0 = provider default
	TopP        *float64 // 0-1; OpenAI only
	TopK        *int     // Anthropic only
}

// agentConfig holds shared configuration for Agent and AgentWorker.
//
// Option applicability:
//   - Agent only: EnableRemoteWorkers, DisableLocalWorker, WithApprovalHandler, WithTimeout
//   - AgentWorker only: (none—worker inherits from options passed to NewAgentWorker)
//   - Both: WithName, WithDescription, WithSystemPrompt, WithTemporalConfig, WithTemporalClient,
//     WithTaskQueue, WithInstanceId, WithLLMClient, WithToolApprovalPolicy, WithTools,
//     WithToolRegistry, WithMaxIterations, WithStream, WithLogger, WithLogLevel, WithConversation
type agentConfig struct {
	ID                 string
	Name               string
	Description        string
	SystemPrompt       string
	temporalConfig     *TemporalConfig
	temporalClient     client.Client
	taskQueueOption    string // set by WithTaskQueue; required when using WithTemporalClient
	ownsTemporalClient bool   // true when we create client from config; false when user provides it
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
	approvalHandler    ApprovalHandler
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
	enableRemoteWorkers bool // true: run event worker & workflow. false (default): use agentChannel only. Agent only.
	remoteWorker        bool // true when AgentWorker; activities use UpdateWorkflow
	agentWorker         bool

	// subAgents
	// subAgents are direct children; each is exposed to the LLM via NewSubAgentTool (see toolsList).
	subAgents []*Agent
	// maxSubAgentDepth limits nesting depth from this agent (direct subs = 1). Default 2 when unset or <= 0.
	maxSubAgentDepth int
}

// defaultTimeout is used when no deadline set, to avoid blocking forever when no workers run.
const defaultTimeout = 5 * time.Minute

// maxApprovalTimeout caps approval wait. Approval activity timeout cannot exceed this.
const maxApprovalTimeout = 31 * 24 * time.Hour

// Option configures an agent. See agentConfig for which options apply to Agent vs AgentWorker.
type Option func(*agentConfig)

// WithName sets the agent name. Applies to Agent and AgentWorker.
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

// WithTemporalConfig sets the Temporal config. Applies to Agent and AgentWorker.
// Use either WithTemporalConfig or WithTemporalClient, not both.
func WithTemporalConfig(cfg *TemporalConfig) Option {
	return func(c *agentConfig) { c.temporalConfig = cfg }
}

// WithTemporalClient sets a pre-configured Temporal client. Use when you need TLS, API key auth,
// Temporal Cloud, or other connection options not supported by TemporalConfig.
// Requires WithTaskQueue. Use either WithTemporalConfig or WithTemporalClient, not both.
// The agent does not close the client when Close() is called; the caller owns the lifecycle.
func WithTemporalClient(tc client.Client) Option {
	return func(c *agentConfig) {
		c.temporalClient = tc
		c.ownsTemporalClient = false
	}
}

// WithTaskQueue sets the task queue name. Required when using WithTemporalClient.
// Ignored when using WithTemporalConfig (TaskQueue comes from TemporalConfig).
func WithTaskQueue(queue string) Option {
	return func(c *agentConfig) { c.taskQueueOption = queue }
}

// WithInstanceId sets the instance identifier. Applies to Agent and AgentWorker.
func WithInstanceId(id string) Option {
	return func(c *agentConfig) { c.instanceId = id }
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
// The callback receives req with req.Respond set; call req.Respond(Approved|Rejected). Agent only; RunStream uses OnApproval on events.
func WithApprovalHandler(fn ApprovalHandler) Option {
	return func(c *agentConfig) { c.approvalHandler = fn }
}

// WithTimeout sets a maximum wait for Run and RunStream. Agent only. Ignored by AgentWorker.
func WithTimeout(d time.Duration) Option {
	return func(c *agentConfig) { c.timeout = d }
}

// WithApprovalTimeout sets max wait per tool approval. Must be less than agent timeout.
// Agent only. When tools require approval, used for the approval activity; defaults to timeout-30s if unset.
// Capped at maxApprovalTimeout (31 days). Validation at build: approvalTimeout < timeout.
func WithApprovalTimeout(d time.Duration) Option {
	return func(c *agentConfig) { c.approvalTimeout = d }
}

// EnableRemoteWorkers enables the event worker and event workflow. Agent only.
// If this option is not passed, approvals and events use agentChannel directly (no event worker/workflow).
// Call EnableRemoteWorkers() to run the event worker and event workflow (required when using NewAgentWorker with DisableLocalWorker, and for some approval/streaming setups).
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
func WithLLMSampling(s *LLMSampling) Option {
	return func(c *agentConfig) { c.llmSampling = s }
}

// WithSubAgents registers sub-agents. Each is exposed to the parent LLM as a tool (AgentTool).
// Execution is via workflow (child workflow), not Tool.Execute — see future workflow integration.
// The sub-agent graph is validated at agent build: no cycles, depth <= WithMaxSubAgentDepth (default 2).
func WithSubAgents(subAgents ...*Agent) Option {
	return func(c *agentConfig) { c.subAgents = subAgents }
}

// WithMaxSubAgentDepth sets the maximum sub-agent nesting depth from this agent (direct children = 1).
// Default is 2 when unset or <= 0. Applies to Agent and AgentWorker.
func WithMaxSubAgentDepth(depth int) Option {
	return func(c *agentConfig) { c.maxSubAgentDepth = depth }
}

// buildAgentConfig applies options, validates, and creates the Temporal client.
// remoteWorker is false for Agent (local); NewAgentWorker overrides to true.
func buildAgentConfig(opts []Option) (*agentConfig, error) {
	c := &agentConfig{remoteWorker: false, ID: uuid.New().String()}
	for _, opt := range opts {
		opt(c)
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
	if c.temporalClient != nil {
		if c.taskQueueOption == "" {
			return nil, errors.New("WithTaskQueue is required when using WithTemporalClient")
		}
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
	if c.maxSubAgentDepth <= 0 {
		c.maxSubAgentDepth = defaultMaxSubAgentDepth
	}
	if err := c.validateSubAgents(); err != nil {
		return nil, err
	}
	if err := c.validateToolsAndSubAgentNames(); err != nil {
		return nil, err
	}
	if c.timeout == 0 {
		c.timeout = defaultTimeout
	}

	// Validate approvalTimeout when any tool requires approval (approvalTimeout must be < timeout)
	if c.hasApprovalTools() {
		if c.approvalTimeout == 0 {
			c.approvalTimeout = c.timeout - 30*time.Second
		}
		if c.approvalTimeout >= c.timeout {
			return nil, fmt.Errorf("approvalTimeout (%v) must be less than agent timeout (%v)", c.approvalTimeout, c.timeout)
		}
		if c.approvalTimeout > maxApprovalTimeout {
			return nil, fmt.Errorf("approvalTimeout (%v) exceeds max (%v)", c.approvalTimeout, maxApprovalTimeout)
		}
	}

	if c.logLevel == "" {
		c.logLevel = "error"
	}
	if c.logger == nil {
		c.logger = logger.DefaultLogger(c.logLevel)
	}

	if c.temporalConfig != nil {
		tc, err := newTemporalClient(c.temporalConfig, c.logger)
		if err != nil {
			return nil, err
		}
		c.temporalClient = tc
		c.ownsTemporalClient = true
		c.taskQueue = c.temporalConfig.TaskQueue
	} else {
		c.taskQueue = c.taskQueueOption
	}
	if c.instanceId != "" {
		c.taskQueue = c.taskQueue + "-" + c.instanceId
	}

	ctx := context.Background()
	c.logger.Info(ctx, "agent config built", slog.String("name", c.Name), slog.String("taskQueue", c.taskQueue))
	// Debug: full config summary for troubleshooting (no sensitive: systemPrompt, API keys)
	c.logger.Debug(ctx, "agent config",
		slog.String("name", c.Name),
		slog.String("taskQueue", c.taskQueue),
		slog.String("instanceId", c.instanceId),
		slog.Bool("ownsTemporalClient", c.ownsTemporalClient),
		slog.Int("maxIterations", c.maxIterations),
		slog.Bool("streamEnabled", c.streamEnabled),
		slog.Bool("disableLocalWorker", c.disableLocalWorker),
		slog.Bool("enableRemoteWorkers", c.enableRemoteWorkers),
		slog.Bool("remoteWorker", c.remoteWorker),
		slog.Bool("hasApprovalHandler", c.approvalHandler != nil),
		slog.Duration("timeout", c.timeout),
		slog.Duration("approvalTimeout", c.approvalTimeout),
		slog.String("logLevel", c.logLevel),
		slog.Int("toolCount", len(c.toolsList())),
		slog.Bool("hasConversation", c.conversation != nil))
	return c, nil
}

// buildAgentConfigForWorker builds config for NewAgentWorker (allows agentWorker mode).
func buildAgentConfigForWorker(opts []Option) (*agentConfig, error) {
	opts = append(opts, func(c *agentConfig) { c.agentWorker = true })
	c, err := buildAgentConfig(opts)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *agentConfig) toolsList() []interfaces.Tool {
	var base []interfaces.Tool
	if c.toolRegistry != nil {
		base = c.toolRegistry.Tools()
	} else {
		base = c.tools
	}
	if len(c.subAgents) == 0 {
		return base
	}
	out := make([]interfaces.Tool, len(base), len(base)+len(c.subAgents))
	copy(out, base)
	for _, sa := range c.subAgents {
		if st := NewSubAgentTool(sa); st != nil {
			out = append(out, st)
		}
	}
	return out
}

// validateSubAgents checks for nil sub-agents, duplicate roots, cycles, and max nesting depth.
func (c *agentConfig) validateSubAgents() error {
	if len(c.subAgents) == 0 {
		return nil
	}
	maxDepth := c.maxSubAgentDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxSubAgentDepth
	}
	seen := make(map[*Agent]struct{}, len(c.subAgents))
	for _, s := range c.subAgents {
		if s == nil {
			return fmt.Errorf("sub-agent must not be nil")
		}
		if _, dup := seen[s]; dup {
			return fmt.Errorf("duplicate sub-agent %q in WithSubAgents", subAgentLabel(s))
		}
		seen[s] = struct{}{}
	}
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
		return fmt.Errorf("sub-agent depth exceeds max (%d): at %q", maxDepth, subAgentLabel(a))
	}
	for _, child := range a.subAgents {
		if child == nil {
			return fmt.Errorf("sub-agent %q has a nil entry in WithSubAgents", subAgentLabel(a))
		}
		if _, cycle := path[child]; cycle {
			return fmt.Errorf("sub-agent cycle detected involving %q and %q", subAgentLabel(a), subAgentLabel(child))
		}
		path[child] = struct{}{}
		if err := dfsSubAgentDepth(child, path, depth+1, maxDepth); err != nil {
			return err
		}
		delete(path, child)
	}
	return nil
}

func subAgentLabel(a *Agent) string {
	if a == nil {
		return "<nil>"
	}
	if n := strings.TrimSpace(a.Name); n != "" {
		return n
	}
	return a.ID
}

// validateToolsAndSubAgentNames ensures tool names are unique across WithTools/registry and sub-agent tools.
func (c *agentConfig) validateToolsAndSubAgentNames() error {
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
	for _, sa := range c.subAgents {
		if sa == nil {
			continue
		}
		n := SubAgentToolName(sa)
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

func newTemporalClient(config *TemporalConfig, sdkLog logger.Logger) (client.Client, error) {
	ctx := context.Background()
	sdkLog.Info(ctx, "connecting to temporal server", slog.String("host", config.Host), slog.Int("port", config.Port))

	clientOptions := client.Options{
		HostPort:                config.Host + ":" + strconv.Itoa(config.Port),
		Namespace:               config.Namespace,
		Logger:                  temporal.NewLogAdapter(sdkLog),
		WorkerHeartbeatInterval: -1, // Disable; requires Temporal server 1.29.1+ with frontend.WorkerHeartbeatsEnabled=true
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	connectionTimeout := 10 * time.Second

	timeoutExceeded := time.After(connectionTimeout)

	var c client.Client
	var err error
	clientReady := false

	ctx, cancelFunc := context.WithCancel(context.Background())
	defer cancelFunc()

	for {
		select {
		case <-timeoutExceeded:
			if !clientReady {
				return nil, fmt.Errorf("temporal conneciton failed after %v timeout", connectionTimeout)
			} else {
				c.Close()
				return nil, fmt.Errorf("temporal namespace check failed after %v timeout", connectionTimeout)
			}
		case <-ticker.C:
			if !clientReady {
				c, err = client.Dial(clientOptions)
				if err == nil {
					sdkLog.Info(ctx, "successfully created temporal client, checking namespace availability")
					clientReady = true
				} else {
					sdkLog.Info(ctx, "failed to create temporal client, dialing again...", slog.Any("error", err))
				}
			} else {
				nsClient, err := client.NewNamespaceClient(clientOptions)
				if err == nil {
					_, err = nsClient.Describe(ctx, config.Namespace)
					nsClient.Close()
					if err == nil {
						sdkLog.Info(ctx, "successfully find temporal namespace", slog.String("namespace", config.Namespace))
						return c, nil
					}
				}
				sdkLog.Info(ctx, "failed to find temporal namespace, trying again..", slog.String("namespace", config.Namespace), slog.Any("error", err))
			}
		}
	}
}
