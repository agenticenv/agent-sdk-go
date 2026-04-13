// Package client implements [interfaces.MCPClient]. Application code configures MCP using
// [github.com/agenticenv/agent-sdk-go/pkg/mcp.MCPStdio] or [github.com/agenticenv/agent-sdk-go/pkg/mcp.MCPStreamableHTTP]
// (see [github.com/agenticenv/agent-sdk-go/pkg/agent.WithMCPConfig]); this package performs the wire-up.
package client

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

// Client implements [interfaces.MCPClient] using a long-lived [*sdkmcp.Client] and a short-lived
// [*sdkmcp.ClientSession] per operation (Connect → RPC → Close), so no session is stored on this struct.
type Client struct {
	mu             sync.Mutex
	name           string
	transportCfg   types.MCPTransportConfig // rebuilt into a new sdkmcp.Transport each RPC (one Connect per transport)
	mcpClient      *sdkmcp.Client
	logger         logger.Logger // from [BuildConfig] inside [NewClient]
	rpcTimeout     time.Duration // per-attempt deadline (connect + RPC)
	maxRPCAttempts int           // connect+RPC tries per operation
	toolFilter     types.MCPToolFilter
}

// NewClient returns a [Client] with name used for [interfaces.MCPClient.Name].
// transportCfg is the same value you set on [github.com/agenticenv/agent-sdk-go/pkg/agent.MCPConfig].Transport (e.g. [github.com/agenticenv/agent-sdk-go/pkg/mcp.MCPStdio], [github.com/agenticenv/agent-sdk-go/pkg/mcp.MCPStreamableHTTP]).
// opts are optional [Option]s (e.g. [WithLogger], [WithLogLevel], [WithToolFilter]); see [BuildConfig] for defaults.
func NewClient(name string, transportCfg types.MCPTransportConfig, opts ...Option) (*Client, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("mcp client: name is empty")
	}
	if transportCfg == nil {
		return nil, errors.New("mcp client: transport config is nil")
	}
	if err := transportCfg.Validate(); err != nil {
		return nil, fmt.Errorf("mcp client: %w", err)
	}
	cfg, err := BuildConfig(opts...)
	if err != nil {
		return nil, err
	}
	if _, err := transportFromConfig(transportCfg); err != nil {
		cfg.Logger.Warn(context.Background(), "mcp client: transport validation failed", slog.String("client", name), slog.Any("err", err))
		return nil, err
	}
	impl := &sdkmcp.Implementation{Name: name, Version: "1.0.0"}
	// Capabilities: minimal client. KeepAlive defaults to 0 → no background pings (we close the session after each RPC).
	sdkOpts := &sdkmcp.ClientOptions{
		Capabilities: &sdkmcp.ClientCapabilities{},
	}
	if sl, ok := cfg.Logger.(*logger.SlogLogger); ok {
		if sg := sl.Slog(); sg != nil {
			sdkOpts.Logger = sg
		}
	}
	maxAtt := cfg.RetryAttempts
	if maxAtt < 1 {
		maxAtt = 1
	}
	cfg.Logger.Debug(context.Background(), "mcp client: created",
		slog.String("client", name),
		slog.String("transport", string(transportCfg.Kind())),
		slog.Duration("timeout", cfg.Timeout),
		slog.Int("maxAttempts", maxAtt))
	return &Client{
		name:           name,
		transportCfg:   transportCfg,
		mcpClient:      sdkmcp.NewClient(impl, sdkOpts),
		logger:         cfg.Logger,
		rpcTimeout:     cfg.Timeout,
		maxRPCAttempts: maxAtt,
		toolFilter:     cfg.ToolFilter,
	}, nil
}

var _ interfaces.MCPClient = (*Client)(nil)

// Name implements [interfaces.MCPClient].
func (c *Client) Name() string {
	if c == nil {
		return ""
	}
	return c.name
}

// Ping implements [interfaces.MCPClient]: connect (full MCP handshake), protocol ping, then close the session.
func (c *Client) Ping(ctx context.Context) error {
	if c == nil {
		return errors.New("mcp client: nil")
	}
	start := time.Now()
	c.logger.Debug(ctx, "mcp client: ping start", slog.String("client", c.name))
	err := c.withSession(ctx, "ping", func(sctx context.Context, sess *sdkmcp.ClientSession) error {
		return sess.Ping(sctx, nil)
	})
	if err != nil {
		c.logger.Warn(ctx, "mcp client: ping failed", slog.String("client", c.name), slog.Duration("elapsed", time.Since(start)), slog.Any("err", err))
		return err
	}
	c.logger.Debug(ctx, "mcp client: ping ok", slog.String("client", c.name), slog.Duration("elapsed", time.Since(start)))
	return nil
}

// ListTools implements [interfaces.MCPClient] via tools/list (paginated).
func (c *Client) ListTools(ctx context.Context) ([]interfaces.ToolSpec, error) {
	if c == nil {
		return nil, errors.New("mcp client: nil")
	}
	start := time.Now()
	var out []interfaces.ToolSpec
	var pageCount int
	c.logger.Debug(ctx, "mcp client: list_tools start", slog.String("client", c.name))
	err := c.withSession(ctx, "list_tools", func(sctx context.Context, sess *sdkmcp.ClientSession) error {
		params := &sdkmcp.ListToolsParams{}
		for {
			pageCount++
			res, err := sess.ListTools(sctx, params)
			if err != nil {
				return err
			}
			for _, t := range res.Tools {
				if t == nil {
					continue
				}
				out = append(out, toolToSpec(t))
			}
			if res.NextCursor == "" {
				return nil
			}
			c.logger.Debug(sctx, "mcp client: list_tools page", slog.String("client", c.name), slog.Int("page", pageCount), slog.Int("toolsInPage", len(res.Tools)), slog.Bool("morePages", true))
			params.Cursor = res.NextCursor
		}
	})
	if err != nil {
		c.logger.Warn(ctx, "mcp client: list_tools failed", slog.String("client", c.name), slog.Duration("elapsed", time.Since(start)), slog.Int("pages", pageCount), slog.Any("err", err))
		return nil, err
	}
	out = c.toolFilter.Apply(out)
	if len(out) == 0 {
		c.logger.Info(ctx, "mcp client: list_tools empty", slog.String("client", c.name), slog.Duration("elapsed", time.Since(start)), slog.Int("pages", pageCount))
	} else {
		c.logger.Debug(ctx, "mcp client: list_tools ok", slog.String("client", c.name), slog.Duration("elapsed", time.Since(start)), slog.Int("tools", len(out)), slog.Int("pages", pageCount))
	}
	return out, nil
}

// CallTool implements [interfaces.MCPClient] via tools/call.
func (c *Client) CallTool(ctx context.Context, tool string, input json.RawMessage) (json.RawMessage, error) {
	if c == nil {
		return nil, errors.New("mcp client: nil")
	}
	start := time.Now()
	c.logger.Debug(ctx, "mcp client: call_tool start", slog.String("client", c.name), slog.String("tool", tool), slog.Int("inputLen", len(input)))
	args := any(map[string]any{})
	if len(input) > 0 {
		var decoded any
		if err := json.Unmarshal(input, &decoded); err != nil {
			c.logger.Warn(ctx, "mcp client: call_tool bad arguments json", slog.String("client", c.name), slog.String("tool", tool), slog.Any("err", err))
			return nil, fmt.Errorf("mcp tools/call arguments: %w", err)
		}
		if decoded != nil {
			args = decoded
		}
	}
	var raw json.RawMessage
	err := c.withSession(ctx, "call_tool", func(sctx context.Context, sess *sdkmcp.ClientSession) error {
		res, err := sess.CallTool(sctx, &sdkmcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			return err
		}
		b, err := json.Marshal(res)
		if err != nil {
			c.logger.Error(sctx, "mcp client: call_tool marshal result failed", slog.String("client", c.name), slog.String("tool", tool), slog.Any("err", err))
			return err
		}
		raw = b
		return nil
	})
	if err != nil {
		c.logger.Warn(ctx, "mcp client: call_tool failed", slog.String("client", c.name), slog.String("tool", tool), slog.Duration("elapsed", time.Since(start)), slog.Any("err", err))
		return nil, err
	}
	c.logger.Debug(ctx, "mcp client: call_tool ok", slog.String("client", c.name), slog.String("tool", tool), slog.Duration("elapsed", time.Since(start)), slog.Int("resultLen", len(raw)))
	return raw, nil
}

// Close implements [interfaces.MCPClient]: clears the MCP client so further RPCs fail; there is no persistent session to tear down.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mcpClient = nil
	c.logger.Info(context.Background(), "mcp client: closed", slog.String("client", c.name))
	return nil
}

// withSession builds a fresh transport from transportCfg each attempt, connects with a per-attempt
// context.Deadline, runs fn(sctx, sess), and closes the session. Failures are retried up to maxRPCAttempts-1
// times (CallTool may run side effects more than once if a later attempt succeeds).
// The mutex is held for the whole operation so concurrent tool calls on one [Client] do not overlap RPCs.
func (c *Client) withSession(parent context.Context, op string, fn func(sctx context.Context, sess *sdkmcp.ClientSession) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mcpClient == nil {
		c.logger.Warn(parent, "mcp client: operation on closed client", slog.String("client", c.name), slog.String("op", op))
		return errors.New("mcp client: closed")
	}
	maxAtt := c.maxRPCAttempts
	if maxAtt < 1 {
		maxAtt = 1
	}
	var lastErr error
	for attempt := 1; attempt <= maxAtt; attempt++ {
		tr, err := transportFromConfig(c.transportCfg)
		if err != nil {
			c.logger.Warn(parent, "mcp client: transport build failed", slog.String("client", c.name), slog.String("op", op), slog.Any("err", err))
			return err
		}
		attemptCtx, cancel := context.WithTimeout(parent, c.rpcTimeout)
		sess, err := c.mcpClient.Connect(attemptCtx, tr, nil)
		if err != nil {
			cancel()
			lastErr = err
			c.logger.Warn(parent, "mcp client: connect failed", slog.String("client", c.name), slog.String("op", op), slog.Int("attempt", attempt), slog.Int("maxAttempts", maxAtt), slog.Duration("deadline", c.rpcTimeout), slog.Any("err", err))
			if attempt == maxAtt {
				c.logger.Error(parent, "mcp client: connect exhausted retries", slog.String("client", c.name), slog.String("op", op), slog.Int("attempts", maxAtt), slog.Any("err", lastErr))
				return lastErr
			}
			delay := mcpRetryDelay(attempt)
			c.logger.Info(parent, "mcp client: will retry after connect failure", slog.String("client", c.name), slog.String("op", op), slog.Int("nextAttempt", attempt+1), slog.Duration("backoff", delay))
			if err := waitMCPRetry(parent, delay); err != nil {
				c.logger.Warn(parent, "mcp client: retry cancelled", slog.String("client", c.name), slog.String("op", op), slog.Any("err", err))
				return err
			}
			continue
		}
		if attempt > 1 {
			c.logger.Info(parent, "mcp client: connected after retry", slog.String("client", c.name), slog.String("op", op), slog.Int("attempt", attempt))
		} else {
			c.logger.Debug(parent, "mcp client: connected", slog.String("client", c.name), slog.String("op", op))
		}
		runErr := fn(attemptCtx, sess)
		closeErr := sess.Close()
		cancel()
		if runErr == nil {
			if closeErr != nil {
				c.logger.Warn(parent, "mcp client: session close error", slog.String("client", c.name), slog.String("op", op), slog.Any("err", closeErr))
			}
			return nil
		}
		_ = closeErr
		lastErr = runErr
		c.logger.Warn(parent, "mcp client: rpc failed", slog.String("client", c.name), slog.String("op", op), slog.Int("attempt", attempt), slog.Int("maxAttempts", maxAtt), slog.Any("err", runErr))
		if attempt == maxAtt {
			c.logger.Error(parent, "mcp client: rpc exhausted retries", slog.String("client", c.name), slog.String("op", op), slog.Int("attempts", maxAtt), slog.Any("err", lastErr))
			return lastErr
		}
		delay := mcpRetryDelay(attempt)
		c.logger.Info(parent, "mcp client: will retry after rpc failure", slog.String("client", c.name), slog.String("op", op), slog.Int("nextAttempt", attempt+1), slog.Duration("backoff", delay))
		if err := waitMCPRetry(parent, delay); err != nil {
			c.logger.Warn(parent, "mcp client: retry cancelled", slog.String("client", c.name), slog.String("op", op), slog.Any("err", err))
			return err
		}
	}
	return lastErr
}

func mcpRetryDelay(attempt int) time.Duration {
	d := time.Duration(attempt) * 50 * time.Millisecond
	if d > 2*time.Second {
		return 2 * time.Second
	}
	return d
}

// waitMCPRetry sleeps for d or returns parent.Err() if parent is done.
func waitMCPRetry(parent context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-parent.Done():
		return parent.Err()
	case <-t.C:
		return nil
	}
}

// toolToSpec maps [sdkmcp.Tool] onto [interfaces.ToolSpec] with the fields the LLM path uses today.
// [sdkmcp.Tool] also carries title, annotations, output schema, etc.; extend [interfaces.ToolSpec] and this mapping when needed.
func toolToSpec(t *sdkmcp.Tool) interfaces.ToolSpec {
	params := inputSchemaToJSONSchema(t.InputSchema)
	if params == nil {
		params = interfaces.JSONSchema{"type": "object"}
	}
	return interfaces.ToolSpec{
		Name:        t.Name,
		Description: t.Description,
		Parameters:  params,
	}
}

func inputSchemaToJSONSchema(in any) interfaces.JSONSchema {
	if in == nil {
		return interfaces.JSONSchema{"type": "object"}
	}
	switch v := in.(type) {
	case map[string]any:
		if len(v) == 0 {
			return interfaces.JSONSchema{"type": "object"}
		}
		return v
	case json.RawMessage:
		var m map[string]any
		if err := json.Unmarshal(v, &m); err != nil || m == nil {
			return interfaces.JSONSchema{"type": "object"}
		}
		return m
	default:
		b, err := json.Marshal(in)
		if err != nil {
			return interfaces.JSONSchema{"type": "object"}
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil || m == nil {
			return interfaces.JSONSchema{"type": "object"}
		}
		return m
	}
}

// streamableHTTPClient builds an [http.Client] for [types.MCPStreamableHTTP] (none, bearer Token, OAuth client credentials, or Headers-only).
// The config must already have passed [types.MCPTransportConfig.Validate] (e.g. from [NewClient]).
func streamableHTTPClient(h types.MCPStreamableHTTP) (*http.Client, error) {
	baseRT := http.DefaultTransport
	if h.SkipTLSVerify {
		baseRT = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	var hdr http.Header
	for k, v := range h.Headers {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if hdr == nil {
			hdr = make(http.Header)
		}
		hdr.Add(k, v)
	}

	if h.OAuthClientCreds != nil {
		cc := h.OAuthClientCreds
		ctx := context.Background()
		if h.SkipTLSVerify {
			ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: baseRT})
		}
		hc := cc.Client(ctx)
		if hdr != nil {
			hc.Transport = &headerRoundTripper{base: hc.Transport, header: hdr}
		}
		return hc, nil
	}
	if strings.TrimSpace(h.Token) != "" {
		tok := strings.TrimSpace(h.Token)
		if hdr == nil {
			hdr = make(http.Header)
		}
		if hdr.Get("Authorization") == "" {
			hdr.Set("Authorization", "Bearer "+tok)
		}
		hc := &http.Client{Transport: &headerRoundTripper{base: baseRT, header: hdr}}
		return hc, nil
	}
	hc := &http.Client{}
	if hdr != nil {
		hc.Transport = &headerRoundTripper{base: baseRT, header: hdr}
	} else {
		hc.Transport = baseRT
	}
	return hc, nil
}

// transportFromConfig builds an [sdkmcp.Transport] from agent MCP transport settings.
// Callers must ensure [types.MCPTransportConfig.Validate] already succeeded for this value (e.g. [NewClient] does).
func transportFromConfig(transportCfg types.MCPTransportConfig) (sdkmcp.Transport, error) {
	if transportCfg == nil {
		return nil, fmt.Errorf("mcp transport config is nil")
	}
	switch transportCfg.Kind() {
	case types.MCPTransportTypeStdio:
		s, ok := transportCfg.(types.MCPStdio)
		if !ok {
			return nil, fmt.Errorf("mcp stdio transport: unexpected config type %T", transportCfg)
		}
		cmd := strings.TrimSpace(s.Command)
		if cmd == "" {
			return nil, fmt.Errorf("mcp stdio transport: Command is empty")
		}
		c := exec.Command(cmd, s.Args...)
		if len(s.Env) > 0 {
			c.Env = append(os.Environ(), mapToEnvSlice(s.Env)...)
		}
		return &sdkmcp.CommandTransport{Command: c}, nil

	case types.MCPTransportTypeStreamableHTTP:
		h, ok := transportCfg.(types.MCPStreamableHTTP)
		if !ok {
			return nil, fmt.Errorf("mcp streamable_http transport: unexpected config type %T", transportCfg)
		}
		ep := strings.TrimSpace(h.URL)
		if ep == "" {
			return nil, fmt.Errorf("mcp streamable_http transport: URL is empty")
		}
		hc, err := streamableHTTPClient(h)
		if err != nil {
			return nil, err
		}
		return &sdkmcp.StreamableClientTransport{
			Endpoint:   ep,
			HTTPClient: hc,
		}, nil

	case types.MCPTransportTypeLoopback:
		lb, ok := transportCfg.(types.MCPLoopback)
		if !ok {
			return nil, fmt.Errorf("mcp loopback transport: unexpected config type %T", transportCfg)
		}
		tr, ok := lb.Transport.(sdkmcp.Transport)
		if !ok || tr == nil {
			return nil, fmt.Errorf("mcp loopback transport: Transport must be a non-nil sdkmcp.Transport")
		}
		return tr, nil

	default:
		return nil, fmt.Errorf("unsupported mcp transport kind %q", transportCfg.Kind())
	}
}

func mapToEnvSlice(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

type headerRoundTripper struct {
	base   http.RoundTripper
	header http.Header
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := h.base
	if base == nil {
		base = http.DefaultTransport
	}
	r := req.Clone(req.Context())
	for k, vals := range h.header {
		for _, v := range vals {
			r.Header.Add(k, v)
		}
	}
	return base.RoundTrip(r)
}
