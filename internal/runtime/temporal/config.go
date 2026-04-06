package temporal

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"go.temporal.io/sdk/client"
)

type TemporalConfig struct {
	Host      string
	Port      int
	Namespace string
	TaskQueue string
}

// TemporalRuntimeConfig holds connection settings plus the same [sdkruntime.AgentSpec] /
// [sdkruntime.AgentExecution] shape as [sdkruntime.ExecuteRequest], so workers and pkg/agent share one layout.
type TemporalRuntimeConfig struct {
	temporalConfig     *TemporalConfig
	temporalClient     client.Client
	taskQueue          string
	instanceId         string
	ownsTemporalClient bool
	// enableRemoteWorkers: start event worker + event workflow in Execute/ExecuteStream (client agent runtime).
	enableRemoteWorkers bool
	// remoteWorker: true for NewAgentWorker (polls activities); false for client Agent runtime.
	remoteWorker bool

	logger logger.Logger

	AgentSpec         sdkruntime.AgentSpec
	AgentExecution    sdkruntime.AgentExecution
	PolicyFingerprint string // from pkg/agent toolPolicyFingerprint; must match caller temporal.ComputeAgentFingerprint inputs
}

// Option configures a TemporalRuntime.
type Option func(*TemporalRuntimeConfig)

// WithTemporalConfig sets the Temporal config.
func WithTemporalConfig(config *TemporalConfig) Option {
	return func(c *TemporalRuntimeConfig) {
		c.temporalConfig = config
		c.taskQueue = config.TaskQueue
		c.ownsTemporalClient = true
	}
}

// WithTemporalClient sets the Temporal client.
func WithTemporalClient(client client.Client, taskQueue string) Option {
	return func(c *TemporalRuntimeConfig) {
		c.temporalClient = client
		c.taskQueue = taskQueue
		c.ownsTemporalClient = false
	}
}

func WithInstanceId(instanceId string) Option {
	return func(c *TemporalRuntimeConfig) {
		c.instanceId = instanceId
	}
}

func WithEnableRemoteWorkers(enableRemoteWorkers bool) Option {
	return func(c *TemporalRuntimeConfig) {
		c.enableRemoteWorkers = enableRemoteWorkers
	}
}

func WithRemoteWorker(remoteWorker bool) Option {
	return func(c *TemporalRuntimeConfig) {
		c.remoteWorker = remoteWorker
	}
}

func WithLogger(logger logger.Logger) Option {
	return func(c *TemporalRuntimeConfig) {
		c.logger = logger
	}
}

// WithAgentSpec sets identity and response format (same as [sdkruntime.ExecuteRequest.AgentSpec]).
func WithAgentSpec(spec sdkruntime.AgentSpec) Option {
	return func(c *TemporalRuntimeConfig) {
		c.AgentSpec = spec
	}
}

// WithAgentExecution sets LLM, tools, session, and limits (same as [sdkruntime.ExecuteRequest.AgentExecution]).
func WithAgentExecution(exec sdkruntime.AgentExecution) Option {
	return func(c *TemporalRuntimeConfig) {
		c.AgentExecution = exec
	}
}

// WithPolicyFingerprint sets the opaque policy digest used with [ComputeAgentFingerprint].
// Must match pkg/agent's toolPolicyFingerprint for the same agent options.
func WithPolicyFingerprint(fp string) Option {
	return func(c *TemporalRuntimeConfig) {
		c.PolicyFingerprint = fp
	}
}

func buildTemporalRuntimeConfig(opts ...Option) (*TemporalRuntimeConfig, error) {
	c := &TemporalRuntimeConfig{logger: logger.NoopLogger()}
	for _, opt := range opts {
		opt(c)
	}

	if c.temporalConfig == nil && c.temporalClient == nil {
		return nil, fmt.Errorf("temporal config or client is required")
	}

	if c.temporalConfig != nil {
		tc, err := newTemporalClient(c.temporalConfig, c.logger)
		if err != nil {
			return nil, err
		}
		c.temporalClient = tc
	}

	if c.instanceId != "" {
		c.taskQueue = c.taskQueue + "-" + c.instanceId
	}

	if c.AgentExecution.LLM.Client == nil {
		return nil, fmt.Errorf("llm client is required")
	}

	c.logger.Debug(context.Background(), "runtime config resolved",
		slog.String("scope", "runtime"),
		slog.String("agentName", c.AgentSpec.Name),
		slog.String("taskQueue", c.taskQueue),
		slog.String("instanceId", c.instanceId),
		slog.Int("maxIterations", c.AgentExecution.Limits.MaxIterations),
		slog.Bool("remoteWorker", c.remoteWorker),
		slog.Bool("enableRemoteWorkers", c.enableRemoteWorkers),
		slog.Duration("timeout", c.AgentExecution.Limits.Timeout),
		slog.Duration("approvalTimeout", c.AgentExecution.Limits.ApprovalTimeout),
		slog.Bool("hasConversation", c.AgentExecution.Session.Conversation != nil))

	return c, nil
}

func newTemporalClient(config *TemporalConfig, sdkLog logger.Logger) (client.Client, error) {
	ctx := context.Background()
	sdkLog.Info(ctx, "runtime connecting to temporal server", slog.String("scope", "runtime"), slog.String("host", config.Host), slog.Int("port", config.Port))

	clientOptions := client.Options{
		HostPort:                config.Host + ":" + strconv.Itoa(config.Port),
		Namespace:               config.Namespace,
		Logger:                  NewLogAdapter(sdkLog),
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
					sdkLog.Debug(ctx, "runtime temporal client dialed, verifying namespace", slog.String("scope", "runtime"))
					clientReady = true
				} else {
					sdkLog.Debug(ctx, "runtime temporal dial retry", slog.String("scope", "runtime"), slog.Any("error", err))
				}
			} else {
				nsClient, err := client.NewNamespaceClient(clientOptions)
				if err == nil {
					_, err = nsClient.Describe(ctx, config.Namespace)
					nsClient.Close()
					if err == nil {
						sdkLog.Info(ctx, "runtime ready (temporal connected)", slog.String("scope", "runtime"), slog.String("namespace", config.Namespace), slog.String("host", config.Host))
						return c, nil
					}
				}
				sdkLog.Debug(ctx, "runtime namespace check retry", slog.String("scope", "runtime"), slog.String("namespace", config.Namespace), slog.Any("error", err))
			}
		}
	}
}
