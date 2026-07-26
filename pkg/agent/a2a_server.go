package agent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"iter"
	stdlog "log"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

const (
	// a2aServerVersion is the agent version embedded in the published agent card.
	a2aServerVersion = "1.0.0"
	// defaultA2AHostname is the bind address used when A2AServerConfig.Hostname is empty.
	defaultA2AHostname = "localhost"
	// defaultA2APort is the TCP port used when A2AServerConfig.Port is 0.
	defaultA2APort = 9999
)

// RunA2A starts the built-in A2A HTTP server for this agent.
//
// The server mounts two handlers on a single [http.Server]:
//   - [a2asrv.WellKnownAgentCardPath] — serves the agent card (GET), built once from the
//     agent's name, description, registered tools, and listen address.
//   - "/" — JSON-RPC v2 only ([a2asrv.NewJSONRPCHandler]): methods such as SendMessage,
//     SendStreamingMessage, GetTask, … Use PascalCase names as defined by a2asrv / A2A v2.
//
// The server blocks until ctx is cancelled, then performs a graceful shutdown.
// Returns a non-nil error if the server was not configured (use [WithA2ADefaultServer] or
// [WithA2AServer]) or if [http.Server.ListenAndServe] fails with an unexpected error.
func (a *Agent) RunA2A(ctx context.Context) error {
	if a.a2aServerConfig == nil {
		return fmt.Errorf("a2a server: not configured — use WithA2ADefaultServer() or WithA2AServer()")
	}

	executor := &agentA2AExecutor{agent: a}

	handlerOpts := []a2asrv.RequestHandlerOption{
		// Route a2asrv internal logging through the same *slog.Logger as the agent when the
		// configured logger is [*logger.SlogLogger] (DefaultLogger, NewSlog, etc.). Other
		// [logger.Logger] implementations fall back to [slog.Default]. See [a2asrv.WithLogger].
		a2asrvHandlerLoggerOption(a.logger),
		// Log every A2A method call (SendMessage, GetTask, CancelTask, …) with duration and
		// errors. Logs go through the context logger set above. Error responses are promoted
		// to slog.LevelError so they surface even when Level is info.
		a2asrv.WithCallInterceptors(a2asrv.NewLoggingInterceptor(&a2asrv.LoggingConfig{
			Level:      slog.LevelInfo,
			ErrorLevel: slog.LevelError,
		})),
	}
	if len(a.a2aServerConfig.BearerTokens) > 0 {
		handlerOpts = append(handlerOpts, a2asrv.WithCallInterceptors(
			&bearerAuthInterceptor{tokens: a.a2aServerConfig.BearerTokens},
		))
	}
	handler := a2asrv.NewHandler(executor, handlerOpts...)

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewAgentCardHandler((*agentCardProducer)(a)))
	jrpc := a2asrv.NewJSONRPCHandler(handler)
	mux.Handle("/", a2aJSONRPCDetachInboundCancel(jrpc))

	addr := fmt.Sprintf("%s:%d", a.a2aServerConfig.Hostname, a.a2aServerConfig.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: a.a2aHTTPAccessLog(mux),
		// Route http.Server internal errors (TLS, connection errors, panic recovery …)
		// through the agent logger instead of the stdlib log.Default().
		ErrorLog: a2aHTTPServerErrorLog(a.logger),
	}

	a.logger.Info(ctx, "a2a server: starting",
		"name", a.Name,
		"addr", addr,
	)

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		} else {
			errCh <- nil
		}
	}()

	select {
	case <-ctx.Done():
		a.logger.Info(ctx, "a2a server: shutting down", "addr", addr)
		if err := srv.Shutdown(context.Background()); err != nil {
			return fmt.Errorf("a2a server: shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("a2a server: %w", err)
		}
		return nil
	}
}

// a2aJSONRPCDetachInboundCancel replaces the request context with [context.WithoutCancel] for
// POST /. The a2asrv JSON-RPC streaming implementation uses this context for subscription
// queue reads while the executor runs under a detached context; if the inbound HTTP context
// is cancelled quickly (client idle timers, proxies, Inspector defaults), the subscription can
// fail with "queue read failed: context canceled" before Temporal finishes workflow cold start.
// Detaching aligns reader lifetime with the executor. Client disconnect alone no longer cancels
// the agent run; rely on CancelTask or workflow limits for enforcement.
func a2aJSONRPCDetachInboundCancel(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/" {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithoutCancel(r.Context())))
	})
}

// ---------------------------------------------------------------------------
// Agent card producer
// ---------------------------------------------------------------------------

// agentCardProducer is a thin type alias over [Agent] that implements both
// [a2asrv.AgentCardProducer] and [a2asrv.AgentCardJSONProducer].
//
// [a2asrv.NewAgentCardHandler] requires an [a2asrv.AgentCardProducer] argument, but when the
// value also satisfies [a2asrv.AgentCardJSONProducer] the handler takes the CardJSON path and
// never calls Card. Card is therefore only present to satisfy the compiler — it returns the
// typed struct without the "url" field, which is fine since it is never served.
//
// CardJSON is the live path: it builds the card on every request (dynamic), marshals it to
// JSON, then injects the required top-level "url" field that [a2a.AgentCard] does not expose
// as a struct field but the A2A spec (and validators such as the A2A Inspector) require.
type agentCardProducer Agent

var (
	_ a2asrv.AgentCardProducer     = (*agentCardProducer)(nil)
	_ a2asrv.AgentCardJSONProducer = (*agentCardProducer)(nil)
)

// Card implements [a2asrv.AgentCardProducer]. Never called when CardJSON is also present.
func (p *agentCardProducer) Card(_ context.Context) (*a2a.AgentCard, error) {
	return (*Agent)(p).buildSDKAgentCard(), nil
}

// CardJSON implements [a2asrv.AgentCardJSONProducer].
// Called by [a2asrv.NewAgentCardHandler] on every GET /.well-known/agent-card.json request.
func (p *agentCardProducer) CardJSON(_ context.Context) ([]byte, error) {
	a := (*Agent)(p)
	card := a.buildSDKAgentCard()
	raw, err := json.Marshal(card)
	if err != nil {
		return nil, fmt.Errorf("a2a card: marshal: %w", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("a2a card: unmarshal: %w", err)
	}
	// Inject the required top-level "url" field (canonical base URL of this server).
	m["url"] = fmt.Sprintf("http://%s:%d", a.a2aServerConfig.Hostname, a.a2aServerConfig.Port)
	out, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("a2a card: re-marshal: %w", err)
	}
	return out, nil
}

// a2asrvHandlerLoggerOption returns [a2asrv.WithLogger] when we can obtain a *slog.Logger
// from the agent's [logger.Logger]; otherwise [slog.Default]. The returned logger carries
// scope=a2a-handler so A2A protocol log lines are easy to grep separately.
func a2asrvHandlerLoggerOption(l logger.Logger) a2asrv.RequestHandlerOption {
	if sl, ok := l.(*logger.SlogLogger); ok {
		if base := sl.Slog(); base != nil {
			return a2asrv.WithLogger(base.With(slog.String("scope", "a2a-handler")))
		}
	}
	return a2asrv.WithLogger(slog.Default())
}

// a2aHTTPServerErrorLog returns a *log.Logger that bridges http.Server internal errors
// (TLS, connection errors, net/http panic recovery …) to the agent's slog logger at
// slog.LevelError. Returns nil when the agent logger is not a [*logger.SlogLogger], which
// leaves [http.Server] using its built-in [log.Default()] — same as the zero value.
func a2aHTTPServerErrorLog(l logger.Logger) *stdlog.Logger {
	if sl, ok := l.(*logger.SlogLogger); ok {
		if base := sl.Slog(); base != nil {
			return slog.NewLogLogger(
				base.With(slog.String("scope", "a2a-http-server")).Handler(),
				slog.LevelError,
			)
		}
	}
	return nil
}

// a2asrvHandlerLoggerOption returns [a2asrv.WithLogger] when we can obtain a *slog.Logger
// using the agent logger at Info level. Implements [http.Flusher] delegation when the
// underlying [http.ResponseWriter] supports it (needed for JSON-RPC streaming/SSE).
// The request context is captured before ServeHTTP so it is valid even for long-lived
// streaming responses where the context may be cancelled by the time the handler returns.
func (a *Agent) a2aHTTPAccessLog(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// Capture context before ServeHTTP: for streaming/SSE responses the request context
		// can be cancelled (client gone) by the time the handler returns.
		reqCtx := r.Context()
		sw := &a2aStatusResponseWriter{ResponseWriter: w}
		h.ServeHTTP(sw, r)
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		a.logger.Info(reqCtx, "a2a http access",
			slog.String("scope", "a2a-http"),
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			slog.Duration("duration", time.Since(start)),
		)
	})
}

// a2aStatusResponseWriter captures the HTTP status code for access logs.
type a2aStatusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *a2aStatusResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *a2aStatusResponseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *a2aStatusResponseWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap returns the underlying [http.ResponseWriter] so that middleware using
// [http.ResponseController] (Go 1.20+) can reach the original writer's capabilities
// (e.g. SetReadDeadline, SetWriteDeadline, Hijack if ever needed).
// Note: a2asrv's SSE layer does a direct w.(http.Flusher) assertion, so Flush() above
// is still required — Unwrap() alone would not satisfy that cast.
func (w *a2aStatusResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// ---------------------------------------------------------------------------
// Agent card builder
// ---------------------------------------------------------------------------

// buildSDKAgentCard builds an [a2a.AgentCard] for the inbound server: JSON-RPC transport,
// streaming from the agent LLM, optional bearer schemes from [A2AServerConfig.BearerTokens].
//
// Defaults come from the agent (name, description, tool-derived skills, bind URL). When
// [A2AServerConfig.AgentCard] is non-nil, each non-empty field from [interfaces.A2AAgentCard]
// overrides the corresponding default (URL base, skills when the slice is non-empty, etc.).
func (a *Agent) buildSDKAgentCard() *a2a.AgentCard {
	cfg := a.a2aServerConfig
	baseURL := fmt.Sprintf("http://%s:%d", cfg.Hostname, cfg.Port)
	ic := cfg.AgentCard

	name := a.Name
	desc := a.Description
	ver := a2aServerVersion
	docURL := ""
	inModes := []string{"text/plain"}
	outModes := []string{"text/plain"}
	var skills []a2a.AgentSkill

	if ic != nil {
		if u := strings.TrimSpace(ic.URL); u != "" {
			baseURL = strings.TrimRight(u, "/")
		}
		if s := strings.TrimSpace(ic.Name); s != "" {
			name = s
		}
		if s := strings.TrimSpace(ic.Description); s != "" {
			desc = s
		}
		if s := strings.TrimSpace(ic.Version); s != "" {
			ver = s
		}
		docURL = strings.TrimSpace(ic.DocumentationURL)
		if len(ic.InputModes) > 0 {
			inModes = ic.InputModes
		}
		if len(ic.OutputModes) > 0 {
			outModes = ic.OutputModes
		}
		if len(ic.Skills) > 0 {
			skills = make([]a2a.AgentSkill, 0, len(ic.Skills))
			for _, spec := range ic.Skills {
				skills = append(skills, toSDKSkill(spec))
			}
		}
	}
	if len(skills) == 0 {
		skills = a.deriveSDKSkills()
	}

	card := &a2a.AgentCard{
		Name:             name,
		Description:      desc,
		Version:          ver,
		DocumentationURL: docURL,
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(baseURL, a2a.TransportProtocolJSONRPC),
		},
		Capabilities: a2a.AgentCapabilities{
			Streaming: a.LLMClient != nil && a.LLMClient.IsStreamSupported(),
		},
		DefaultInputModes:  inModes,
		DefaultOutputModes: outModes,
		Skills:             skills,
	}
	if len(cfg.BearerTokens) > 0 {
		card.SecuritySchemes = a2a.NamedSecuritySchemes{
			a2aSecuritySchemeName: a2a.HTTPAuthSecurityScheme{
				Scheme:      "Bearer",
				Description: "Static bearer token. Include as: Authorization: Bearer <token>",
			},
		}
		card.SecurityRequirements = a2a.SecurityRequirementsOptions{
			{a2aSecuritySchemeName: {}},
		}
	}
	return card
}

// deriveSDKSkills maps the agent's registered [interfaces.Tool] slice to [a2a.AgentSkill]s
// for the published agent card.
//
// For [A2ATool] entries (delegated A2A client skills), the original [interfaces.A2ASkillSpec]
// is converted via [toSDKSkill] to faithfully preserve Tags, InputModes, OutputModes, and
// Examples from the remote agent card. All other tools are mapped generically using the
// tool's Name, DisplayName, and Description. Tools with an empty Name are skipped.
func (a *Agent) deriveSDKSkills() []a2a.AgentSkill {
	tools, err := a.resolveTools(context.Background())
	if err != nil {
		return nil
	}
	skills := make([]a2a.AgentSkill, 0, len(tools))
	for _, t := range tools {
		if t == nil {
			continue
		}
		id := strings.TrimSpace(t.Name())
		if id == "" {
			continue
		}

		// For delegated A2A skills, preserve the original protocol-level fields.
		if a2aTool, ok := t.(*A2ATool); ok {
			skills = append(skills, toSDKSkill(a2aTool.SkillSpec))
			continue
		}

		// MCP tool — tag with mcp + server name
		if mcpTool, ok := t.(*MCPTool); ok {
			skills = append(skills, a2a.AgentSkill{
				ID:          id,
				Name:        t.DisplayName(),
				Description: t.Description(),
				Tags:        []string{"mcp", mcpTool.ServerName},
			})
			continue
		}

		skills = append(skills, a2a.AgentSkill{
			ID:          id,
			Name:        t.DisplayName(),
			Description: t.Description(),
			Tags:        []string{"tool", t.Name()},
		})
	}
	return skills
}

// ---------------------------------------------------------------------------
// AgentExecutor adapter
// ---------------------------------------------------------------------------

// agentA2AExecutor adapts [Agent] to [a2asrv.AgentExecutor].
//
// Execute extracts the text content of the incoming A2A message, calls [Agent.Run],
// and yields the result as a single [a2a.Message] reply. This makes each A2A call
// equivalent to one synchronous agent run. The caller (the built-in JSON-RPC handler)
// handles streaming by subscribing to the event sequence.
//
// For async or multi-turn task handling, implement [a2asrv.AgentExecutor] directly and
// supply it via a custom [http.Handler] instead of using [Agent.RunA2A].
type agentA2AExecutor struct {
	agent *Agent
}

var _ a2asrv.AgentExecutor = (*agentA2AExecutor)(nil)

// Execute implements [a2asrv.AgentExecutor].
//
// The executor has two paths selected at call time based on whether the LLM
// client supports streaming ([interfaces.LLMClient.IsStreamSupported]):
//
// Non-streaming path (LLM does not support streaming):
// Text is collected from the incoming message, passed to [Agent.Run], and the
// complete result is yielded as a single [a2a.Message] with role ROLE_AGENT.
//
// Streaming path (LLM.IsStreamSupported):
// Emits [a2a.NewSubmittedTask] + [a2a.TaskStateWorking] then calls [Agent.Stream].
// Each [events.AgentTextMessageContentEvent] delta is forwarded as an
// [a2a.TaskArtifactUpdateEvent] (first delta creates the artifact; subsequent
// deltas append to it). On [events.AgentRunFinishedEvent] a TASK_STATE_COMPLETED
// status is emitted and the iterator ends. On [events.AgentRunErrorEvent] a
// TASK_STATE_FAILED status is emitted. This lets A2A clients that call
// SendStreamingMessage receive token-level deltas over SSE, while clients that
// call SendMessage still get a fully-assembled task result (the SDK collects all
// events internally for non-streaming callers).
func (e *agentA2AExecutor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		inputText := collectMessageText(execCtx.Message)

		streaming := e.agent.LLMClient != nil && e.agent.LLMClient.IsStreamSupported()

		ctx, sp := e.agent.tracer.StartSpan(ctx, "a2a.execute",
			interfaces.Attribute{Key: "agent.name", Value: e.agent.Name},
			interfaces.Attribute{Key: "task.id", Value: execCtx.TaskID},
			interfaces.Attribute{Key: "streaming", Value: streaming},
			interfaces.Attribute{Key: "input.length", Value: len(inputText)},
		)
		defer sp.End()

		if !streaming {
			// Non-streaming: one blocking Run, one Message reply.
			agentRun, err := e.agent.Run(ctx, inputText, nil)
			if err != nil {
				sp.RecordError(err)
				errMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(err.Error()))
				yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, errMsg), nil)
				return
			}
			result, err := agentRun.Get(ctx)
			if err != nil {
				sp.RecordError(err)
				errMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(err.Error()))
				yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, errMsg), nil)
				return
			}
			yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(result.Content)), nil)
			return
		}

		// Streaming: create task, mark working, then stream deltas.
		if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
			return
		}
		if !yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateWorking, nil), nil) {
			return
		}

		agentStream, err := e.agent.Stream(ctx, inputText, nil)
		if err != nil {
			sp.RecordError(err)
			errMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(err.Error()))
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, errMsg), nil)
			return
		}
		streamCh, err := agentStream.Events(ctx)
		if err != nil {
			sp.RecordError(err)
			errMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(err.Error()))
			yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, errMsg), nil)
			return
		}

		var artifactID a2a.ArtifactID
		for ev := range streamCh {
			switch e := ev.(type) {
			case *events.AgentTextMessageContentEvent:
				if e.Delta == "" {
					continue
				}
				part := a2a.NewTextPart(e.Delta)
				if artifactID == "" {
					artifact := a2a.NewArtifactEvent(execCtx, part)
					artifactID = artifact.Artifact.ID
					if !yield(artifact, nil) {
						return
					}
				} else {
					if !yield(a2a.NewArtifactUpdateEvent(execCtx, artifactID, part), nil) {
						return
					}
				}
			case *events.AgentRunFinishedEvent:
				yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil)
				return
			case *events.AgentRunErrorEvent:
				sp.RecordError(fmt.Errorf("%s", e.Message))
				errMsg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(e.Message))
				yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateFailed, errMsg), nil)
				return
			}
		}
		// Channel closed without an explicit RUN_FINISHED — treat as completed.
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil)
	}
}

// Cancel implements [a2asrv.AgentExecutor].
//
// Emits a [a2a.TaskStatusUpdateEvent] with [a2a.TaskStateCanceled].
// Agent.Run is synchronous and cannot be interrupted mid-flight; cancellation is
// acknowledged after the current run completes or if the task had not started yet.
func (e *agentA2AExecutor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// collectMessageText concatenates the text from all text-kind [a2a.Part]s in a message,
// separated by newlines. Non-text parts and empty strings are skipped.
// Returns an empty string if msg is nil or has no parts.
func collectMessageText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range msg.Parts {
		if p == nil {
			continue
		}
		txt := p.Text()
		if txt == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(txt)
	}
	return sb.String()
}

// toSDKSkill converts an [interfaces.A2ASkillSpec] to an [a2a.AgentSkill].
// Used when populating agent cards from already-resolved skill specs.
func toSDKSkill(s interfaces.A2ASkillSpec) a2a.AgentSkill {
	return a2a.AgentSkill{
		ID:          s.ID,
		Name:        s.Name,
		Description: s.Description,
		Tags:        s.Tags,
		InputModes:  s.InputModes,
		OutputModes: s.OutputModes,
		Examples:    s.Examples,
	}
}

// ---------------------------------------------------------------------------
// Bearer token authentication
// ---------------------------------------------------------------------------

// a2aSecuritySchemeName is the key used in the agent card's SecuritySchemes map
// and SecurityRequirements when bearer authentication is enabled.
const a2aSecuritySchemeName a2a.SecuritySchemeName = "bearer"

// bearerAuthInterceptor implements [a2asrv.CallInterceptor] to enforce static
// Bearer token authentication on all inbound A2A JSON-RPC requests.
//
// Token values are compared using [crypto/subtle.ConstantTimeCompare] to
// prevent timing-based token enumeration attacks. An authenticated [a2asrv.User]
// is attached to the [a2asrv.CallContext] on success so downstream code (e.g.
// the task store authenticator) can associate tasks with callers.
type bearerAuthInterceptor struct {
	// tokens is the set of accepted bearer tokens, populated from
	// [A2AServerConfig.BearerTokens] at server start.
	tokens []string
}

var _ a2asrv.CallInterceptor = (*bearerAuthInterceptor)(nil)

// Before implements [a2asrv.CallInterceptor].
// Extracts the "Authorization" header, verifies the Bearer token, and either
// attaches an authenticated [a2asrv.User] to the call context or rejects the
// request with [a2a.ErrUnauthenticated].
func (b *bearerAuthInterceptor) Before(ctx context.Context, callCtx *a2asrv.CallContext, _ *a2asrv.Request) (context.Context, any, error) {
	vals, ok := callCtx.ServiceParams().Get("authorization")
	if ok && len(vals) > 0 {
		raw := vals[0]
		if after, cut := strings.CutPrefix(raw, "Bearer "); cut {
			for _, t := range b.tokens {
				if subtle.ConstantTimeCompare([]byte(after), []byte(t)) == 1 {
					callCtx.User = a2asrv.NewAuthenticatedUser("bearer", nil)
					return ctx, nil, nil
				}
			}
		}
	}
	return ctx, nil, a2a.ErrUnauthenticated
}

// After implements [a2asrv.CallInterceptor] (no-op).
func (b *bearerAuthInterceptor) After(_ context.Context, _ *a2asrv.CallContext, _ *a2asrv.Response) error {
	return nil
}
