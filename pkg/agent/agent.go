package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/vinodvanja/temporal-agents-go/pkg/interfaces"
	"github.com/vinodvanja/temporal-agents-go/pkg/logger"
	"github.com/vinodvanja/temporal-agents-go/pkg/messaging"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	// defaultTimeout is used when DisableWorker but no deadline set, to avoid blocking forever when no workers run.
	defaultTimeout = 5 * time.Minute
)

// TemporalConfig holds Temporal connection settings (Host, Port, Namespace) and the task queue name.
//
// TaskQueue is required and must be unique per agent. Use different TaskQueues when running
// multiple agents in the same process (e.g. "my-agent-math", "my-agent-creative").
// For multiple instances of the same agent (e.g. scaled pods), use WithInstanceId() so each
// instance gets a unique queue derived as {TaskQueue}-{InstanceId}.
//
// When using DisableWorker, the agent and NewAgentWorker must use the same TaskQueue (and
// same InstanceId if set) so they pair correctly.
type TemporalConfig struct {
	Host      string
	Port      int
	Namespace string
	TaskQueue string // Required. Full task queue name. Unique per agent.
}

// Agent runs LLM-backed workflows via Temporal.
type Agent struct {
	Name                  string
	Description           string
	SystemPrompt          string
	LLMClient             interfaces.LLMClient
	temporalConfig        *TemporalConfig
	instanceId            string // optional; when set, task queue becomes {TaskQueue}-{instanceId}
	taskQueue             string // resolved: TaskQueue or TaskQueue-InstanceId
	temporalClient        client.Client
	runWorker             worker.Worker
	eventWorker           worker.Worker
	messaging             interfaces.PubSub
	approvalHandler       ApprovalHandler
	remoteWorker          bool
	agentWorker           bool
	timeout               time.Duration // when > 0, Run uses this to avoid blocking forever (e.g. when no workers)
	logger                log.Logger
	logLevel              string                             // used when logger is nil; default "error"
	tools                 []interfaces.Tool                  // registered tools for the LLM to use
	toolRegistry          interfaces.ToolRegistry            // alternative to tools; used when set
	toolApprovalPolicy    interfaces.AgentToolApprovalPolicy // when nil, defaults to RequireAllToolApprovalPolicy
	maxIterations         int                                // max LLM rounds when tools are used; 0 = default 5
	streamEnabled         bool                               // enable partial content streaming when supported
	eventWorkerMu         sync.Mutex                         // protects eventWorker creation
	runMu                 sync.Mutex                         // protects active run state
	activeRunWorkflowID   string                             // current run's workflow ID (empty if idle)
	activeEventWorkflowID string                             // per-agent event workflow ID (set once, signaled on Close)
	agentUUID             string                             // stable UUID for this agent instance (used in event workflow ID)
}

// ErrAgentAlreadyRunning is returned when Run or RunStream is called while a run is already in progress.
var ErrAgentAlreadyRunning = errors.New("agent already has an active run")

// AgentResponse is the return value of AgentRun.Get / AgentWorkflow.
type AgentResponse struct {
	Content   string         `json:"content"`
	AgentName string         `json:"agent_name"`
	Model     string         `json:"model"`
	Metadata  map[string]any `json:"metadata"`
}

// Option configures an Agent.
type Option func(*Agent)

// WithName sets the agent name.
func WithName(name string) Option {
	return func(a *Agent) { a.Name = name }
}

// WithDescription sets the agent description.
func WithDescription(desc string) Option {
	return func(a *Agent) { a.Description = desc }
}

// WithSystemPrompt sets the system prompt.
func WithSystemPrompt(prompt string) Option {
	return func(a *Agent) { a.SystemPrompt = prompt }
}

// WithTemporalConfig sets the Temporal config. Agent creates client and worker internally.
func WithTemporalConfig(cfg *TemporalConfig) Option {
	return func(a *Agent) { a.temporalConfig = cfg }
}

// WithInstanceId sets an instance identifier. When set, the task queue becomes {TaskQueue}-{instanceId}.
// Use when running multiple instances of the same agent in the same process, or when scaling (e.g. each
// pod gets WithInstanceId(os.Getenv("POD_NAME"))). For DisableWorker, the agent and NewAgentWorker
// must use the same InstanceId so they pair on the same derived queue.
func WithInstanceId(id string) Option {
	return func(a *Agent) { a.instanceId = id }
}

// DisableWorker starts the agent without an embedded worker. Use when workers run in a separate process.
func DisableWorker() Option {
	return func(a *Agent) { a.remoteWorker = true }
}

// WithLLMClient sets the LLM client.
func WithLLMClient(c interfaces.LLMClient) Option {
	return func(a *Agent) { a.LLMClient = c }
}

// WithApprovalHandler sets the callback for tool approval. Only invoked when a tool requires approval.
func WithApprovalHandler(fn ApprovalHandler) Option {
	return func(a *Agent) { a.approvalHandler = fn }
}

// WithToolApprovalPolicy sets when tools can run without approval. Default: all tools require approval.
// Use agent.AutoToolApprovalPolicy() to allow all without approval, or agent.AllowlistToolApprovalPolicy("echo", "calculator")
// to allow only specific tools. Requires WithApprovalHandler when any tool needs approval.
func WithToolApprovalPolicy(policy interfaces.AgentToolApprovalPolicy) Option {
	return func(a *Agent) { a.toolApprovalPolicy = policy }
}

// WithTimeout sets a maximum wait for Run. If unset and ctx has no deadline, defaultTimeout (5 min) applies.
func WithTimeout(d time.Duration) Option {
	return func(a *Agent) { a.timeout = d }
}

// WithLogger sets the logger for the Temporal client. Overrides WithLogLevel when both are set.
func WithLogger(l log.Logger) Option {
	return func(a *Agent) { a.logger = l }
}

// WithLogLevel sets the log level for the default ZapAdapter (error, warn, info, debug). Default is "error".
// Ignored when WithLogger is set.
func WithLogLevel(level string) Option {
	return func(a *Agent) { a.logLevel = level }
}

// WithTools registers tools with the agent. Tools are passed to the LLM so it can choose which to call during Run.
func WithTools(tools ...interfaces.Tool) Option {
	return func(a *Agent) { a.tools = tools }
}

// WithToolRegistry sets a tool registry. When set, tools are taken from registry.Tools(); overrides WithTools.
func WithToolRegistry(reg interfaces.ToolRegistry) Option {
	return func(a *Agent) { a.toolRegistry = reg }
}

// WithMaxIterations sets the max number of LLM rounds when tools are used (each round may include tool calls).
// Prevents unbounded loops. Default 5 when unset.
func WithMaxIterations(n int) Option {
	return func(a *Agent) { a.maxIterations = n }
}

// WithStream enables or disables partial content streaming when supported by the LLM (e.g. OpenAI, Anthropic).
// Default: false. Use WithStream(true) to enable streaming in RunStream.
func WithStream(enable bool) Option {
	return func(a *Agent) { a.streamEnabled = enable }
}

// buildAgent applies options, validates, and creates the Temporal client.
func buildAgent(opts []Option) (*Agent, error) {
	a := &Agent{}
	for _, opt := range opts {
		opt(a)
	}
	// Default: require approval for all tools (secure). User must set WithToolApprovalPolicy to allow without approval.
	if a.toolApprovalPolicy == nil {
		a.toolApprovalPolicy = RequireAllToolApprovalPolicy{}
	}
	if a.temporalConfig == nil {
		return nil, errors.New("temporal config is required")
	}
	if a.temporalConfig.TaskQueue == "" {
		return nil, errors.New("TaskQueue is required in TemporalConfig: provide a unique name per agent")
	}
	if a.LLMClient == nil {
		return nil, errors.New("LLM client is required")
	}
	if a.logger == nil {
		a.logger = getDefaultLogger(a.logLevel)
	}
	tc, err := client.Dial(client.Options{
		HostPort:  a.temporalConfig.Host + ":" + strconv.Itoa(a.temporalConfig.Port),
		Namespace: a.temporalConfig.Namespace,
		Logger:    a.logger,
	})
	if err != nil {
		return nil, err
	}
	a.temporalClient = tc

	a.taskQueue = a.temporalConfig.TaskQueue
	if a.instanceId != "" {
		a.taskQueue = a.taskQueue + "-" + a.instanceId
	}

	a.messaging = messaging.NewInMemory()
	a.agentUUID = uuid.New().String()

	// Validate: if any tool requires approval, ApprovalHandler must be set
	for _, t := range a.toolsList() {
		if a.requiresApproval(t) && a.approvalHandler == nil {
			return nil, fmt.Errorf("tool %q requires approval but WithApprovalHandler was not set", t.Name())
		}
	}

	if a.remoteWorker || a.agentWorker {
		return nil, errors.New("remote worker mode (DisableWorker) is not supported: use embedded worker")
	}

	return a, nil
}

func getDefaultLogger(level string) log.Logger {
	if level == "" {
		level = "error"
	}
	return logger.NewZapAdapter(logger.NewZapLoggerWithLevel(level))
}

// hasWorkersAvailable returns true if there are pollers on the task queue.
func (a *Agent) hasWorkersAvailable(ctx context.Context) bool {
	res, err := a.temporalClient.DescribeTaskQueue(ctx, a.taskQueue, enumspb.TASK_QUEUE_TYPE_WORKFLOW)
	if err != nil {
		return false
	}
	return len(res.GetPollers()) != 0
}

// createWorker creates and registers a Temporal worker for the agent.
func createRunWorker(a *Agent) worker.Worker {
	w := worker.New(a.temporalClient, a.taskQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(a.AgentWorkflow, workflow.RegisterOptions{Name: "AgentWorkflow"})
	w.RegisterActivityWithOptions(a.AgentLLMActivity, activity.RegisterOptions{Name: "AgentLLMActivity"})
	w.RegisterActivityWithOptions(a.AgentLLMStreamActivity, activity.RegisterOptions{Name: "AgentLLMStreamActivity"})
	w.RegisterActivityWithOptions(a.AgentToolApprovalActivity, activity.RegisterOptions{Name: "AgentToolApprovalActivity"})
	w.RegisterActivityWithOptions(a.AgentToolExecuteActivity, activity.RegisterOptions{Name: "AgentToolExecuteActivity"})
	w.RegisterActivityWithOptions(a.SendAgentEventUpdateActivity, activity.RegisterOptions{Name: "SendAgentEventUpdateActivity"})
	return w
}

// createEventWorker starts the event worker if not already running. Called when RunStream or
// approval handling is needed. Per-agent mutex allows parallel creation across different agents
// while preventing double-creation when Run and RunStream are invoked concurrently on the same agent.
func (a *Agent) createEventWorker() {
	a.eventWorkerMu.Lock()
	defer a.eventWorkerMu.Unlock()
	if a.eventWorker != nil {
		return
	}
	eventQueue := a.taskQueue + "-events"
	w := worker.New(a.temporalClient, eventQueue, worker.Options{})
	w.RegisterWorkflowWithOptions(a.AgentEventWorkflow, workflow.RegisterOptions{Name: "AgentEventWorkflow"})
	w.RegisterActivityWithOptions(a.EventPublishActivity, activity.RegisterOptions{Name: "EventPublishActivity"})
	w.RegisterActivityWithOptions(a.ToolRunApprovalPublishActivity, activity.RegisterOptions{Name: "ToolRunApprovalPublishActivity"})
	a.eventWorker = w
	go func() { _ = a.eventWorker.Start() }()
}

// NewAgent creates an Agent with the given options.
// Event worker is started lazily when RunStream is called or when approvals are needed.
func NewAgent(opts ...Option) (*Agent, error) {
	a, err := buildAgent(opts)
	if err != nil {
		return nil, err
	}

	if !a.remoteWorker {
		runWorker := createRunWorker(a)
		go func() { _ = runWorker.Start() }()
		a.runWorker = runWorker
	}
	return a, nil
}

// ensureEventWorkflowStarted starts the event workflow once per agent. Sets activeEventWorkflowID on first call.
func (a *Agent) ensureEventWorkflowStarted(ctx context.Context) (string, error) {
	a.runMu.Lock()
	id := a.activeEventWorkflowID
	a.runMu.Unlock()
	if id != "" {
		return id, nil
	}
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if a.activeEventWorkflowID != "" {
		return a.activeEventWorkflowID, nil
	}
	eid := eventWorkflowIDPrefix + a.Name + "-" + a.agentUUID
	_, err := a.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                       eid,
		TaskQueue:                a.taskQueue + "-events",
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	}, a.AgentEventWorkflow)
	if err != nil {
		return "", err
	}
	a.activeEventWorkflowID = eid
	return eid, nil
}

// beginRun marks the start of a run. Returns ErrAgentAlreadyRunning if a run is already active.
func (a *Agent) beginRun(workflowID string) error {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	if a.activeRunWorkflowID != "" {
		return ErrAgentAlreadyRunning
	}
	a.activeRunWorkflowID = workflowID
	return nil
}

// endRun clears the active run state when a run completes. Does not clear activeEventWorkflowID (per-agent).
func (a *Agent) endRun() {
	a.runMu.Lock()
	defer a.runMu.Unlock()
	a.activeRunWorkflowID = ""
}

// Close stops workers, terminates the active run if any, signals the event workflow to complete,
// waits for it to finish, then closes the Temporal client. Only one run can be active per agent.
func (a *Agent) Close() {
	a.runMu.Lock()
	workflowID := a.activeRunWorkflowID
	eventWorkflowID := a.activeEventWorkflowID
	a.runMu.Unlock()

	if a.temporalClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if workflowID != "" {
			_ = a.temporalClient.TerminateWorkflow(ctx, workflowID, "", "agent closed")
		}
		if eventWorkflowID != "" {
			_ = a.temporalClient.SignalWorkflow(ctx, eventWorkflowID, "", eventWorkflowCompleteSignal, nil)
			// Wait for event workflow to complete gracefully (worker must stay running to process the signal)
			run := a.temporalClient.GetWorkflow(ctx, eventWorkflowID, "")
			_ = run.Get(ctx, nil)
		}
	}

	if a.runWorker != nil {
		a.runWorker.Stop()
	}
	if a.eventWorker != nil {
		a.eventWorker.Stop()
	}
	if a.temporalClient != nil {
		a.temporalClient.Close()
	}
}

// Run starts the agent workflow and returns the result. Handles approvals when
// tools require them. Use WithTimeout or a context with deadline to avoid blocking.
func (a *Agent) Run(ctx context.Context, input string) (*AgentResponse, error) {
	runCtx := ctx
	d := a.timeout
	if d == 0 {
		d = defaultTimeout
	}
	if _, ok := ctx.Deadline(); !ok && d > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	id := uuid.New().String()
	workflowID := fmt.Sprintf("%s-%s", a.Name, id)
	if err := a.beginRun(workflowID); err != nil {
		return nil, err
	}

	wfInput := AgentWorkflowInput{
		Input:            input,
		StreamingEnabled: a.streamEnabled,
	}
	var approvalCh <-chan *ApprovalRequest
	var closeApproval func() error
	if a.approvalHandler != nil {
		a.createEventWorker()
		eventWorkflowID, err := a.ensureEventWorkflowStarted(ctx)
		if err != nil {
			a.endRun()
			return nil, err
		}
		wfInput.EventWorkflowID = eventWorkflowID
		approvalCh, closeApproval, err = a.subscribeToApprovals(runCtx, workflowID)
		if err != nil {
			a.endRun()
			return nil, err
		}
		defer closeApproval()
	}

	workfowRun, err := a.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: a.taskQueue,
	}, a.AgentWorkflow, wfInput)
	if err != nil {
		a.endRun()
		return nil, err
	}

	responseCh := make(chan *AgentResponse, 1)
	errCh := make(chan error, 1)
	go func() {
		var response *AgentResponse
		err = workfowRun.Get(runCtx, &response)
		if err != nil {
			errCh <- err
			return
		}
		responseCh <- response
	}()

	if a.approvalHandler == nil {
		select {
		case r := <-responseCh:
			a.endRun()
			return r, nil
		case err := <-errCh:
			a.endRun()
			return nil, err
		case <-runCtx.Done():
			a.endRun()
			return nil, runCtx.Err()
		}
	}

	wfID := workfowRun.GetID()
	type approvalResponse struct {
		taskToken []byte
		status    ApprovalStatus
	}
	approvalResponseCh := make(chan approvalResponse, 16)
	for {
		select {
		case r := <-responseCh:
			a.endRun()
			return r, nil
		case err := <-errCh:
			a.endRun()
			return nil, err
		case <-runCtx.Done():
			a.endRun()
			return nil, runCtx.Err()
		case req := <-approvalCh:
			if req == nil || req.WorkflowID != wfID {
				continue
			}
			taskToken := req.TaskToken
			onApproval := func(status ApprovalStatus) error {
				if status != ApprovalStatusRejected && status != ApprovalStatusApproved {
					return errors.New("invalid approval status")
				}
				approvalResponseCh <- approvalResponse{taskToken: taskToken, status: status}
				return nil
			}
			a.approvalHandler(runCtx, req, onApproval)
		case resp := <-approvalResponseCh:
			if err := a.temporalClient.CompleteActivity(runCtx, resp.taskToken, resp.status, nil); err != nil {
				return nil, err
			}
		}
	}
}

// RunStream starts the run and returns a channel of AgentEvent. Events are streamed until
// AgentEventComplete. Handles approvals via ApprovalHandler when tools require them.
func (a *Agent) RunStream(ctx context.Context, input string) (chan *AgentEvent, error) {
	runCtx := ctx
	d := a.timeout
	if d == 0 {
		d = defaultTimeout
	}
	if _, ok := ctx.Deadline(); !ok && d > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}

	id := uuid.New().String()
	workflowID := fmt.Sprintf("%s-%s", a.Name, id)
	if err := a.beginRun(workflowID); err != nil {
		return nil, err
	}

	a.createEventWorker()
	eventWorkflowID, err := a.ensureEventWorkflowStarted(ctx)
	if err != nil {
		a.endRun()
		return nil, err
	}

	wfInput := AgentWorkflowInput{
		Input:            input,
		EventWorkflowID:  eventWorkflowID,
		StreamingEnabled: a.streamEnabled,
	}

	eventCh, closeEvent, err := a.subscribeToAgentEvents(runCtx, workflowID)
	if err != nil {
		a.endRun()
		return nil, err
	}

	var approvalCh <-chan *ApprovalRequest
	var closeApproval func() error
	if a.approvalHandler != nil {
		approvalCh, closeApproval, err = a.subscribeToApprovals(runCtx, workflowID)
		if err != nil {
			closeEvent()
			return nil, err
		}
	}

	_, err = a.temporalClient.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: a.taskQueue,
	}, a.AgentWorkflow, wfInput)
	if err != nil {
		a.endRun()
		closeEvent()
		if closeApproval != nil {
			closeApproval()
		}
		return nil, err
	}

	outCh := make(chan *AgentEvent, 64)
	go func() {
		defer close(outCh)
		defer closeEvent()
		if closeApproval != nil {
			defer closeApproval()
		}
		defer a.endRun()
		for ev := range eventCh {
			outCh <- ev
			if ev != nil && ev.Type == AgentEventComplete {
				return
			}
		}
	}()

	if a.approvalHandler != nil {
		approvalResponseCh := make(chan struct {
			taskToken []byte
			status    ApprovalStatus
		}, 16)
		go func() {
			for req := range approvalCh {
				if req == nil || req.WorkflowID != workflowID {
					continue
				}
				taskToken := req.TaskToken
				onApproval := func(status ApprovalStatus) error {
					if status != ApprovalStatusRejected && status != ApprovalStatusApproved {
						return errors.New("invalid approval status")
					}
					approvalResponseCh <- struct {
						taskToken []byte
						status    ApprovalStatus
					}{taskToken: taskToken, status: status}
					return nil
				}
				a.approvalHandler(runCtx, req, onApproval)
			}
		}()
		go func() {
			for resp := range approvalResponseCh {
				_ = a.temporalClient.CompleteActivity(runCtx, resp.taskToken, resp.status, nil)
			}
		}()
	}

	return outCh, nil
}
