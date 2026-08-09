package restate

import (
	"context"
	"strings"
	"testing"
	"time"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
)

type stubLLM struct{}

func (stubLLM) Generate(context.Context, *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	return &interfaces.LLMResponse{Content: "ok"}, nil
}
func (stubLLM) GenerateStream(context.Context, *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, nil
}
func (stubLLM) GetModel() string                    { return "stub" }
func (stubLLM) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (stubLLM) IsStreamSupported() bool             { return false }

func validRestateConfig() *RestateConfig {
	return &RestateConfig{
		Ingress:  IngressConfig{URL: "http://localhost:8080"},
		Endpoint: EndpointConfig{},
	}
}

func TestValidateRestateConfig_defaults(t *testing.T) {
	cfg := validRestateConfig()
	if err := validateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint.ListenAddress != defaultEndpointListenAddress {
		t.Fatalf("listen: got %q", cfg.Endpoint.ListenAddress)
	}
	if cfg.Ingress.HTTPTimeout != defaultIngressHTTPTimeout {
		t.Fatalf("timeout: got %v", cfg.Ingress.HTTPTimeout)
	}
	if cfg.Ingress.HTTPMaxAttempts != defaultIngressHTTPAttempts {
		t.Fatalf("attempts: got %d", cfg.Ingress.HTTPMaxAttempts)
	}
	if cfg.EventLog.DisableClear {
		t.Fatal("expected cleanup enabled by default")
	}
	if cfg.EventLog.TTL != defaultEventLogTTL {
		t.Fatalf("event log TTL: got %v", cfg.EventLog.TTL)
	}
}

func TestValidateRestateConfig_errors(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*RestateConfig)
		want string
	}{
		{"empty url", func(c *RestateConfig) { c.Ingress.URL = "" }, "Ingress.URL is required"},
		{"bad scheme", func(c *RestateConfig) { c.Ingress.URL = "ftp://x" }, "http or https"},
		{"no host", func(c *RestateConfig) { c.Ingress.URL = "http://" }, "host"},
		{"neg timeout", func(c *RestateConfig) { c.Ingress.HTTPTimeout = -1 }, "HTTPTimeout"},
		{"neg attempts", func(c *RestateConfig) { c.Ingress.HTTPMaxAttempts = -1 }, "HTTPMaxAttempts"},
		{"neg event log TTL", func(c *RestateConfig) { c.EventLog.TTL = -1 }, "EventLog.TTL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validRestateConfig()
			tc.mut(cfg)
			err := validateConfig(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestValidateRestateConfig_trimsIdentityKeys(t *testing.T) {
	cfg := validRestateConfig()
	cfg.Endpoint.IdentityPublicKeys = []string{"  k1  ", "", "k2"}
	if err := validateConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if len(cfg.Endpoint.IdentityPublicKeys) != 2 ||
		cfg.Endpoint.IdentityPublicKeys[0] != "k1" ||
		cfg.Endpoint.IdentityPublicKeys[1] != "k2" {
		t.Fatalf("keys: %#v", cfg.Endpoint.IdentityPublicKeys)
	}
}

func TestBuildRestateRuntime_requiresConfigAndLLM(t *testing.T) {
	_, err := newRuntimeFromOptions()
	if err == nil || !strings.Contains(err.Error(), "restate config is required") {
		t.Fatalf("got %v", err)
	}
	_, err = newRuntimeFromOptions(WithRestateConfig(validRestateConfig()))
	if err == nil || !strings.Contains(err.Error(), "llm client is required") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildRestateRuntime_ok(t *testing.T) {
	rt, err := newRuntimeFromOptions(
		WithRestateConfig(validRestateConfig()),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: stubLLM{}},
		}),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "a"}),
		WithLogger(logger.NoopLogger()),
		WithToolExecutionMode(types.AgentToolExecutionModeSequential),
		WithToolsResolver(func(context.Context) ([]interfaces.Tool, error) { return nil, nil }),
		WithApprovalHandler(func(context.Context, *types.ApprovalRequest) {}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if rt.AgentSpec.Name != "a" {
		t.Fatalf("name: %q", rt.AgentSpec.Name)
	}
	if rt.ToolExecutionMode != types.AgentToolExecutionModeSequential {
		t.Fatalf("mode: %v", rt.ToolExecutionMode)
	}
	if rt.approvalHandler == nil || rt.tools.resolver == nil {
		t.Fatal("expected approval handler and tools resolver")
	}
	if rt.config.Ingress.HTTPTimeout != 30*time.Second {
		t.Fatalf("default timeout applied: %v", rt.config.Ingress.HTTPTimeout)
	}
}

func TestOptions_WithTracerMetrics(t *testing.T) {
	rt, err := newRuntimeFromOptions(
		WithRestateConfig(validRestateConfig()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "a"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
		WithTracer(observability.DefaultNoopTracer),
		WithMetrics(observability.DefaultNoopMetrics),
	)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Tracer == nil || rt.Metrics == nil {
		t.Fatal("expected tracer and metrics")
	}
}
