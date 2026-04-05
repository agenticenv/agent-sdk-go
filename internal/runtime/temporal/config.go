package temporal

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"go.temporal.io/sdk/client"
)

type TemporalConfig struct {
	Host      string
	Port      int
	Namespace string
	TaskQueue string
}

type TemporalRuntimeConfig struct {
	temporalConfig     *TemporalConfig
	temporalClient     client.Client
	taskQueue          string
	instanceId         string
	ownsTemporalClient bool
	remoteWorker       bool

	logger    logger.Logger
	llmClient interfaces.LLMClient

	conversation       interfaces.Conversation
	conversationSize   int
	toolApprovalPolicy interfaces.AgentToolApprovalPolicy

	agentName       string
	systemPrompt    string
	responseFormat  *interfaces.ResponseFormat
	tools           []interfaces.Tool
	llmSampling     *types.LLMSampling
	timeout         time.Duration
	maxIterations   int
	approvalTimeout time.Duration
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

func WithLLMClient(llmClient interfaces.LLMClient) Option {
	return func(c *TemporalRuntimeConfig) {
		c.llmClient = llmClient
	}
}

func WithLLMSampling(llmSampling *types.LLMSampling) Option {
	return func(c *TemporalRuntimeConfig) {
		c.llmSampling = llmSampling
	}
}

func WithTools(tools ...interfaces.Tool) Option {
	return func(c *TemporalRuntimeConfig) {
		c.tools = tools
	}
}

func WithSystemPrompt(systemPrompt string) Option {
	return func(c *TemporalRuntimeConfig) {
		c.systemPrompt = systemPrompt
	}
}

func WithResponseFormat(responseFormat *interfaces.ResponseFormat) Option {
	return func(c *TemporalRuntimeConfig) {
		c.responseFormat = responseFormat
	}
}

func WithConversation(conversation interfaces.Conversation) Option {
	return func(c *TemporalRuntimeConfig) {
		c.conversation = conversation
	}
}

func WithConversationSize(conversationSize int) Option {
	return func(c *TemporalRuntimeConfig) {
		c.conversationSize = conversationSize
	}
}

func WithToolApprovalPolicy(policy interfaces.AgentToolApprovalPolicy) Option {
	return func(c *TemporalRuntimeConfig) { c.toolApprovalPolicy = policy }
}

func WithTimeout(timeout time.Duration) Option {
	return func(c *TemporalRuntimeConfig) { c.timeout = timeout }
}

func WithAgentName(agentName string) Option {
	return func(c *TemporalRuntimeConfig) { c.agentName = agentName }
}

func WithMaxIterations(maxIterations int) Option {
	return func(c *TemporalRuntimeConfig) { c.maxIterations = maxIterations }
}

func WithApprovalTimeout(approvalTimeout time.Duration) Option {
	return func(c *TemporalRuntimeConfig) { c.approvalTimeout = approvalTimeout }
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

	if c.llmClient == nil {
		return nil, fmt.Errorf("llm client is required")
	}

	c.logger.Debug(context.Background(), "temporal runtime config",
		slog.String("agentName", c.agentName),
		slog.String("taskQueue", c.taskQueue),
		slog.String("instanceId", c.instanceId),
		slog.Int("maxIterations", c.maxIterations),
		slog.Bool("remoteWorker", c.remoteWorker),
		slog.Duration("timeout", c.timeout),
		slog.Duration("approvalTimeout", c.approvalTimeout),
		slog.Bool("hasConversation", c.conversation != nil))

	return c, nil
}

func newTemporalClient(config *TemporalConfig, sdkLog logger.Logger) (client.Client, error) {
	ctx := context.Background()
	sdkLog.Info(ctx, "connecting to temporal server", slog.String("host", config.Host), slog.Int("port", config.Port))

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
