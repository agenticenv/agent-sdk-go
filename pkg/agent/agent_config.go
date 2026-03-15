package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"github.com/vinodvanja/temporal-agents-go/pkg/logger"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	"go.uber.org/zap"
)

// agentConfig holds shared configuration for Agent and AgentWorker.
//
// Option applicability:
//   - Agent only: EnableRemoteWorkers, DisableWorker, WithApprovalHandler, WithTimeout
//   - AgentWorker only: (none—worker inherits from options passed to NewAgentWorker)
//   - Both: WithName, WithDescription, WithSystemPrompt, WithTemporalConfig, WithInstanceId,
//     WithLLMClient, WithToolApprovalPolicy, WithTools, WithToolRegistry, WithMaxIterations,
//     WithStream, WithLogger, WithLogLevel
type agentConfig struct {
	ID                 string
	Name               string
	Description        string
	SystemPrompt       string
	temporalConfig     *TemporalConfig
	instanceId         string
	taskQueue          string
	temporalClient     client.Client
	LLMClient          interfaces.LLMClient
	tools              []interfaces.Tool
	toolRegistry       interfaces.ToolRegistry
	toolApprovalPolicy interfaces.AgentToolApprovalPolicy
	maxIterations      int
	streamEnabled      bool
	logger             log.Logger
	logLevel           string
	approvalHandler    ApprovalHandler
	timeout            time.Duration

	// build-time flags
	disableWorker       bool // true when user calls DisableWorker; no local worker. Agent only.
	enableRemoteWorkers bool // true: run event worker & workflow. false (default): use agentChannel only. Agent only.
	remoteWorker        bool // true when AgentWorker; activities use UpdateWorkflow
	agentWorker         bool
}

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
func WithTemporalConfig(cfg *TemporalConfig) Option {
	return func(c *agentConfig) { c.temporalConfig = cfg }
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

// WithLogger sets the logger. Applies to Agent and AgentWorker.
func WithLogger(l log.Logger) Option {
	return func(c *agentConfig) { c.logger = l }
}

// WithLogLevel sets the log level. Applies to Agent and AgentWorker.
func WithLogLevel(level string) Option {
	return func(c *agentConfig) { c.logLevel = level }
}

// WithApprovalHandler sets the callback for tool approval. Agent only. Ignored by AgentWorker.
func WithApprovalHandler(fn ApprovalHandler) Option {
	return func(c *agentConfig) { c.approvalHandler = fn }
}

// WithTimeout sets a maximum wait for Run. Agent only. Ignored by AgentWorker.
func WithTimeout(d time.Duration) Option {
	return func(c *agentConfig) { c.timeout = d }
}

// WithEnableRemoteWorkers enables the event worker and event workflow. Agent only. Default false.
// When false (default): no event worker/workflow; approvals and events use agentChannel directly.
// When true: run event worker and event workflow; required when using NewAgentWorker (DisableWorker).
func WithEnableRemoteWorkers(enable bool) Option {
	return func(c *agentConfig) { c.enableRemoteWorkers = enable }
}

// DisableWorker marks to skip local worker creation. Agent only. Use with NewAgentWorker.
func DisableWorker() Option {
	return func(c *agentConfig) { c.disableWorker = true }
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
	if c.temporalConfig == nil {
		return nil, errors.New("temporal config is required")
	}
	if c.temporalConfig.TaskQueue == "" {
		return nil, errors.New("TaskQueue is required in TemporalConfig: provide a unique name per agent")
	}
	if c.LLMClient == nil {
		return nil, errors.New("LLM client is required")
	}
	if c.logLevel == "" {
		c.logLevel = "error"
	}
	if c.logger == nil {
		c.logger = logger.NewZapAdapter(logger.NewZapLoggerWithConfig(logger.ZapLoggerConfig{Level: c.logLevel}))
	}

	tc, err := newTemporalClient(c.temporalConfig, c.logger)
	if err != nil {
		return nil, err
	}

	c.temporalClient = tc
	c.taskQueue = c.temporalConfig.TaskQueue
	if c.instanceId != "" {
		c.taskQueue = c.taskQueue + "-" + c.instanceId
	}

	c.logger.Info("agent config built", zap.String("name", c.Name), zap.String("taskQueue", c.taskQueue))
	// Debug: full config summary for troubleshooting (no sensitive: systemPrompt, API keys)
	temporalCfg := c.temporalConfig
	c.logger.Debug("agent config",
		zap.String("name", c.Name),
		zap.String("taskQueue", c.taskQueue),
		zap.String("instanceId", c.instanceId),
		zap.String("temporalHost", temporalCfg.Host),
		zap.Int("temporalPort", temporalCfg.Port),
		zap.String("namespace", temporalCfg.Namespace),
		zap.Int("maxIterations", c.maxIterations),
		zap.Bool("streamEnabled", c.streamEnabled),
		zap.Bool("disableWorker", c.disableWorker),
		zap.Bool("enableRemoteWorkers", c.enableRemoteWorkers),
		zap.Bool("remoteWorker", c.remoteWorker),
		zap.Bool("hasApprovalHandler", c.approvalHandler != nil),
		zap.Duration("timeout", c.timeout),
		zap.String("logLevel", c.logLevel),
		zap.Int("toolCount", len(c.toolsList())))
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
	if c.toolRegistry != nil {
		return c.toolRegistry.Tools()
	}
	return c.tools
}

func (c *agentConfig) requiresApproval(t interfaces.Tool) bool {
	return c.toolApprovalPolicy != nil && c.toolApprovalPolicy.RequiresApproval(t)
}

func newTemporalClient(config *TemporalConfig, logger log.Logger) (client.Client, error) {
	logger.Info("connecting to temporal server", zap.String("host", config.Host), zap.Int("port", config.Port))

	clientOptions := client.Options{
		HostPort:                config.Host + ":" + strconv.Itoa(config.Port),
		Namespace:               config.Namespace,
		Logger:                  logger,
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
					logger.Info("successfully created temporal client, checking namespace readiness")
					clientReady = true
				} else {
					logger.Info("failed to create temporal client, dialing again...", zap.Error(err))
				}
			} else {
				nsClient, err := client.NewNamespaceClient(clientOptions)
				if err == nil {
					_, err = nsClient.Describe(ctx, config.Namespace)
					nsClient.Close()
					if err == nil {
						logger.Info("successfully find temporal namespace", zap.String("namespace", config.Namespace))
						return c, nil
					}
				}
				logger.Info("failed to find temporal namespace, trying again..", zap.String("namespace", config.Namespace), zap.Error(err))
			}
		}
	}
}
