package restate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/events"
	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/google/uuid"
	restatesdk "github.com/restatedev/sdk-go"
	restateingress "github.com/restatedev/sdk-go/ingress"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	defaultIngressHTTPTimeout     = 30 * time.Second
	defaultIngressHTTPAttempts    = 3
	defaultIngressRetryInitial    = 200 * time.Millisecond
	defaultIngressRetryMaxBackoff = 2 * time.Second
	defaultIngressRetryFactor     = 2.0
)

// retryableHTTPError is returned by doIngressHTTP when the response status is retryable
// (429, 502, 503, 504). withIngressRetry detects this type to decide whether to retry.
type retryableHTTPError struct {
	status int
	body   string
}

func (e *retryableHTTPError) Error() string {
	return fmt.Sprintf("restate: retryable http status %d: %s", e.status, e.body)
}

// invocationLookupRequest is the POST body for Restate's /restate/lookup endpoint.
// Used by lookupInvocationID to resolve the native invocationId from an idempotency key.
type invocationLookupRequest struct {
	Target         string `json:"target"`
	Service        string `json:"service"`
	Handler        string `json:"handler"`
	IdempotencyKey string `json:"idempotencyKey"`
}

// invocationLookupResponse is the response body from Restate's /restate/lookup endpoint.
type invocationLookupResponse struct {
	InvocationID string `json:"invocationId"`
}

// httpResult bundles the response metadata and body bytes from a single doIngressHTTP attempt.
// The body is fully read and the response body is closed before this is returned.
type httpResult struct {
	resp *http.Response
	body []byte
}

// approvalReply is sent from an approval handler goroutine back to the runApprovalLoop
// poll goroutine once the user has made an approval decision.
type approvalReply struct {
	approvalToken string
	status        types.ApprovalStatus
}

// ─── HTTP client construction ─────────────────────────────────────────────────

// newIngressHTTPClient builds an HTTP client for Restate ingress and admin calls.
// No Client.Timeout so long-running Attach can block; short RPCs apply per-attempt
// timeouts via withIngressRetry. When the tracer is an OTel tracer, the transport is
// wrapped for distributed trace propagation.
func newIngressHTTPClient(tracer interfaces.Tracer) *http.Client {
	base := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	var transport http.RoundTripper = base
	if ot, ok := tracer.(interfaces.OTelTracer); ok && ot != nil && ot.OTelTracer() != nil {
		transport = otelhttp.NewTransport(base)
	}
	return &http.Client{Transport: transport}
}

// ─── Retry helpers ────────────────────────────────────────────────────────────

// withIngressRetry retries transient failures for short ingress RPCs (Send, Output, Cancel, lookup).
// Not for long-running Attach — pass the caller's context with a single attempt instead.
func withIngressRetry[T any](ctx context.Context, rt *RestateRuntime, op func(context.Context) (T, error)) (T, error) {
	var zero T
	attempts := rt.ingressHTTPAttempts()
	timeout := rt.ingressHTTPTimeout()
	backoff := defaultIngressRetryInitial

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, timeout)
		result, err := op(attemptCtx)
		cancel()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if !isTransientIngressErr(err) || attempt >= attempts {
			break
		}
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(backoff):
		}
		next := time.Duration(float64(backoff) * defaultIngressRetryFactor)
		if next > defaultIngressRetryMaxBackoff {
			backoff = defaultIngressRetryMaxBackoff
		} else {
			backoff = next
		}
	}
	return zero, lastErr
}

func isRetryableHTTPStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func isTransientIngressErr(err error) bool {
	if err == nil {
		return false
	}
	var re *retryableHTTPError
	if errors.As(err, &re) {
		return true
	}
	// net.Error covers *net.OpError, *url.Error, and other network-level failures.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// context.DeadlineExceeded is retryable when it comes from a per-attempt timeout
	// (applied by withIngressRetry itself), not the parent context.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}

// ─── Ingress operations ───────────────────────────────────────────────────────

// sendAgentLoop submits an AgentLoop Run or Stream invocation via ingress Send
// and returns the SDK run ID and Restate invocation ID.
func (rt *RestateRuntime) sendAgentLoop(
	ctx context.Context,
	req *sdkruntime.RunRequest,
	handler string,
	streaming bool,
) (runID, invocationID string, err error) {
	if req == nil {
		return "", "", fmt.Errorf("restate: nil RunRequest")
	}
	if err := rt.ensureIngress(); err != nil {
		return "", "", err
	}

	runID = uuid.New().String()
	rt.logger.Debug(ctx, "runtime agent loop send",
		slog.String("scope", "runtime"),
		slog.String("agent", rt.AgentSpec.Name),
		slog.String("handler", handler),
		slog.String("runID", runID),
		slog.Bool("streaming", streaming),
		slog.Int("inputLen", len(req.UserPrompt)))

	eventTypes := req.EventTypes
	if !streaming && rt.approvalHandler != nil && len(eventTypes) == 0 {
		eventTypes = []events.AgentEventType{events.AgentEventTypeCustom}
	}
	if streaming && len(eventTypes) == 0 {
		eventTypes = []events.AgentEventType{events.AgentEventAll}
	}

	rt.tools.stash.Store(runID, stagedRun{
		tools:            req.Tools,
		eventTypes:       eventTypes,
		maxSubAgentDepth: req.MaxSubAgentDepth,
	})

	// Resolve memory scope on the client before Send (same as Temporal). Context values
	// like tenant/user ID do not survive into the Restate handler.
	memoryScope, memErr := rt.ResolveMemoryScope(ctx)
	if memErr != nil {
		rt.logger.Warn(ctx, "runtime memory scope resolve failed, continuing with empty scope",
			slog.String("scope", "runtime"),
			slog.Any("error", memErr))
		memoryScope = interfaces.MemoryScope{}
	}

	loopReq := AgentLoopRequest{
		agentLoopCore: agentLoopCore{
			RunID:            runID,
			UserPrompt:       req.UserPrompt,
			ConversationID:   req.ConversationID,
			EventTypes:       eventTypes,
			EventTopic:       runID,
			MaxSubAgentDepth: req.MaxSubAgentDepth,
			SubAgentRoutes:   buildSubAgentRoutes(req.SubAgents),
			MemoryScope:      memoryScope,
		},
		AgentName:        strings.TrimSpace(rt.AgentSpec.Name),
		LLMStreamEnabled: req.EnableLLMStream,
		StreamHandler:    streaming,
	}

	sendResp, err := withIngressRetry(ctx, rt, func(attemptCtx context.Context) (restateingress.SendResponse[*AgentLoopResponse], error) {
		return restateingress.Service[AgentLoopRequest, *AgentLoopResponse](
			rt.ingressClient, rt.agentLoopServiceName, handler,
		).Send(attemptCtx, loopReq, restatesdk.WithIdempotencyKey(runID))
	})
	if err != nil {
		rt.tools.stash.Delete(runID)
		rt.logger.Error(ctx, "runtime agent loop ingress send failed",
			slog.String("scope", "runtime"),
			slog.String("handler", handler),
			slog.String("runID", runID),
			slog.Any("error", err))
		return "", "", fmt.Errorf("restate: invoke %s/%s: %w", rt.agentLoopServiceName, handler, err)
	}

	rt.logger.Debug(ctx, "runtime agent loop ingress accepted",
		slog.String("scope", "runtime"),
		slog.String("handler", handler),
		slog.String("runID", runID),
		slog.String("invocationID", sendResp.Id()))
	return runID, sendResp.Id(), nil
}

// lookupInvocationID resolves Restate's invocationId for a prior Send identified by runID
// (used as the idempotency key). Returns ErrRunNotFound when Restate has no matching record.
func (rt *RestateRuntime) lookupInvocationID(ctx context.Context, handler, runID string) (string, error) {
	lookupURL := strings.TrimRight(rt.config.Ingress.URL, "/") + "/restate/lookup"

	body, err := json.Marshal(invocationLookupRequest{
		Target:         "idempotentInvocation",
		Service:        rt.agentLoopServiceName,
		Handler:        handler,
		IdempotencyKey: runID,
	})
	if err != nil {
		return "", fmt.Errorf("restate: marshal lookup request: %w", err)
	}

	resp, respBody, err := rt.doIngressHTTP(ctx, func(attemptCtx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, lookupURL, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("restate: create lookup request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if key := rt.config.Ingress.AuthKey; key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		return req, nil
	})
	if err != nil {
		return "", fmt.Errorf("restate: lookup invocation: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var out invocationLookupResponse
		if err := json.Unmarshal(respBody, &out); err != nil {
			return "", fmt.Errorf("restate: decode lookup response: %w", err)
		}
		if strings.TrimSpace(out.InvocationID) == "" {
			return "", fmt.Errorf("restate: lookup returned empty invocationId")
		}
		return out.InvocationID, nil
	case http.StatusNotFound:
		return "", types.ErrRunNotFound
	default:
		return "", fmt.Errorf("restate: lookup %s/%s run %q: status %d: %s",
			rt.agentLoopServiceName, handler, runID, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
}

// resolveRunningInvocation verifies an invocation is in-progress and returns its invocationId.
// It probes Output first (single round-trip for completed/missing runs), then falls back
// to the lookup endpoint to retrieve the invocationId only when the run is still active.
func (rt *RestateRuntime) resolveRunningInvocation(ctx context.Context, handler, runID string) (string, error) {
	_, probeErr := withIngressRetry(ctx, rt, func(attemptCtx context.Context) (struct{}, error) {
		_, err := restateingress.ServiceInvocationByIdempotencyKey[*AgentLoopResponse](
			rt.ingressClient, rt.agentLoopServiceName, handler, runID,
		).Output(attemptCtx)
		return struct{}{}, err
	})
	if probeErr == nil {
		return "", types.ErrRunAlreadyCompleted
	}
	var notFound *restateingress.InvocationNotFoundError
	if errors.As(probeErr, &notFound) {
		return "", types.ErrRunNotFound
	}
	var notReady *restateingress.InvocationNotReadyError
	if !errors.As(probeErr, &notReady) {
		return "", fmt.Errorf("restate: probe %s/%s run %q: %w", rt.agentLoopServiceName, handler, runID, probeErr)
	}
	// Run is in progress — fetch the invocationId for handle construction.
	return rt.lookupInvocationID(ctx, handler, runID)
}

// approve resolves a pending tool approval by POSTing to Restate's awakeable resolve endpoint.
// approvalToken is the awakeable id emitted by the agent loop in a CUSTOM event.
func (rt *RestateRuntime) approve(ctx context.Context, approvalToken string, status types.ApprovalStatus) error {
	if status != types.ApprovalStatusApproved && status != types.ApprovalStatusRejected {
		return fmt.Errorf("restate: invalid approval status %q", status)
	}
	approvalToken = strings.TrimSpace(approvalToken)
	if approvalToken == "" {
		return fmt.Errorf("restate: empty approval token")
	}

	payload, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("restate: marshal approval status: %w", err)
	}
	resolveURL := strings.TrimRight(rt.config.Ingress.URL, "/") +
		"/restate/awakeables/" + url.PathEscape(approvalToken) + "/resolve"

	resp, body, err := rt.doIngressHTTP(ctx, func(attemptCtx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, resolveURL, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("restate: create awakeable resolve request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if key := rt.config.Ingress.AuthKey; key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		return req, nil
	})
	if err != nil {
		return fmt.Errorf("restate: resolve awakeable: %w", err)
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted, http.StatusNoContent:
		return nil
	case http.StatusNotFound, http.StatusConflict:
		return types.ErrApprovalAlreadyResolved
	default:
		return fmt.Errorf("restate: resolve awakeable %q: status %d: %s",
			approvalToken, resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// cancelInvocation cancels a Restate invocation by invoking AgentLoop/Cancel.
// That handler calls restatesdk.CancelInvocation — no admin URL is required.
func (rt *RestateRuntime) cancelInvocation(ctx context.Context, runID, invocationID string) error {
	if err := rt.ensureIngress(); err != nil {
		return err
	}
	invocationID = strings.TrimSpace(invocationID)
	if invocationID == "" {
		return fmt.Errorf("restate: empty invocationId")
	}
	_, err := withIngressRetry(ctx, rt, func(attemptCtx context.Context) (restatesdk.Void, error) {
		return restateingress.Service[CancelRequest, restatesdk.Void](
			rt.ingressClient, rt.agentLoopServiceName, agentLoopCancelHandler,
		).Request(attemptCtx, CancelRequest{
			RunID:        runID,
			InvocationID: invocationID,
		})
	})
	if err != nil {
		return fmt.Errorf("restate: invoke %s/%s: %w", rt.agentLoopServiceName, agentLoopCancelHandler, err)
	}
	return nil
}

// doIngressHTTP performs an HTTP request with per-attempt timeout and retries.
// newRequest must build a fresh *http.Request each attempt (body may be consumed on retry).
func (rt *RestateRuntime) doIngressHTTP(ctx context.Context, newRequest func(context.Context) (*http.Request, error)) (*http.Response, []byte, error) {
	if rt.httpClient == nil {
		return nil, nil, fmt.Errorf("restate: http client not configured")
	}
	out, err := withIngressRetry(ctx, rt, func(attemptCtx context.Context) (httpResult, error) {
		req, err := newRequest(attemptCtx)
		if err != nil {
			return httpResult{}, err
		}
		resp, err := rt.httpClient.Do(req)
		if err != nil {
			return httpResult{}, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return httpResult{}, fmt.Errorf("restate: read response: %w", readErr)
		}
		if isRetryableHTTPStatus(resp.StatusCode) {
			return httpResult{}, &retryableHTTPError{status: resp.StatusCode, body: string(body)}
		}
		return httpResult{resp: resp, body: body}, nil
	})
	if err != nil {
		return nil, nil, err
	}
	return out.resp, out.body, nil
}

// ─── Approval poll loop ───────────────────────────────────────────────────────

// runApprovalLoop polls AgentEventLog for CUSTOM approval events and dispatches each
// to approvalHandler in a separate goroutine (so the poll loop is never blocked).
// Used on the Run path when WithApprovalHandler is set; Stream uses Events + Approve instead.
func (rt *RestateRuntime) runApprovalLoop(ctx context.Context, runID string, handle *runHandle) {
	if rt.approvalHandler == nil || handle == nil {
		return
	}

	offset := int64(0)
	replyCh := make(chan approvalReply, 8)

	for {
		select {
		case <-handle.Done():
			return
		case <-ctx.Done():
			return
		case reply := <-replyCh:
			if err := rt.approve(ctx, reply.approvalToken, reply.status); err != nil && !errors.Is(err, types.ErrApprovalAlreadyResolved) {
				rt.logger.Error(ctx, "runtime approval completion failed",
					slog.String("scope", "runtime"), slog.Any("error", err))
			}
		default:
		}

		pullCtx, cancel := context.WithTimeout(ctx, rt.ingressHTTPTimeout())
		resp, err := restateingress.Object[PullRequest, PullResponse](
			rt.ingressClient, rt.eventLogServiceName, runID, "Pull",
		).Request(pullCtx, PullRequest{Offset: &offset, Wait: false})
		cancel()

		if err != nil {
			select {
			case <-handle.Done():
				return
			case <-ctx.Done():
				return
			default:
			}
			// Post-completion Clear (EventLog.TTL) elapsed — stop polling.
			if isAgentEventLogCleared(err) {
				return
			}
			rt.logger.Warn(ctx, "runtime approval pull failed",
				slog.String("scope", "runtime"),
				slog.String("runID", runID),
				slog.Any("error", err))
			select {
			case <-handle.Done():
				return
			case <-ctx.Done():
				return
			case <-time.After(pubsubPullInterval):
			}
			continue
		}

		if len(resp.Messages) == 0 {
			select {
			case <-handle.Done():
				return
			case <-ctx.Done():
				return
			case <-time.After(pubsubPullInterval):
			}
			offset = resp.NextOffset
			continue
		}

		for _, raw := range resp.Messages {
			ev, decErr := events.EventFromJSON(raw)
			if decErr != nil || ev == nil || ev.Type() != events.AgentEventTypeCustom {
				continue
			}
			custom, ok := ev.(*events.AgentCustomEvent)
			if !ok {
				continue
			}
			apprReq, token, prepErr := prepareApprovalFromCustomEvent(custom)
			if prepErr != nil {
				if errors.Is(prepErr, ErrNotApprovalCustomEvent) {
					continue
				}
				rt.logger.Error(ctx, "runtime approval event decode failed",
					slog.String("scope", "runtime"), slog.Any("error", prepErr))
				continue
			}

			// Capture loop variables for the goroutine.
			capturedToken := token
			apprReq.Respond = func(status types.ApprovalStatus) error {
				if status != types.ApprovalStatusRejected && status != types.ApprovalStatusApproved {
					return errors.New("invalid approval status")
				}
				select {
				case replyCh <- approvalReply{approvalToken: capturedToken, status: status}:
				default:
					go func() { replyCh <- approvalReply{approvalToken: capturedToken, status: status} }()
				}
				return nil
			}

			// Each handler call runs in its own goroutine so the poll loop stays responsive.
			capturedReq := apprReq
			approvalTimeout := rt.approvalTaskTimeout()
			go func() {
				approvalCtx, approvalCancel := context.WithTimeout(ctx, approvalTimeout)
				defer approvalCancel()
				rt.approvalHandler(approvalCtx, capturedReq)
			}()
		}
		offset = resp.NextOffset
	}
}
