package temporal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	sdkruntime "github.com/agenticenv/agent-sdk-go/internal/runtime"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/agenticenv/agent-sdk-go/pkg/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
	temporalmocks "go.temporal.io/sdk/mocks"
)

type stubLLM struct{}

func (stubLLM) Generate(ctx context.Context, req *interfaces.LLMRequest) (*interfaces.LLMResponse, error) {
	return &interfaces.LLMResponse{}, nil
}
func (stubLLM) GenerateStream(ctx context.Context, req *interfaces.LLMRequest) (interfaces.LLMStream, error) {
	return nil, errors.New("stub")
}
func (stubLLM) GetModel() string                    { return "stub" }
func (stubLLM) GetProvider() interfaces.LLMProvider { return interfaces.LLMProviderOpenAI }
func (stubLLM) IsStreamSupported() bool             { return false }

type spanBridge struct {
	s trace.Span
}

func (b spanBridge) End() { b.s.End() }

func (b spanBridge) SetAttribute(key string, value any) {
	b.s.SetAttributes(attribute.String(key, fmt.Sprint(value)))
}

func (b spanBridge) RecordError(err error) { b.s.RecordError(err) }

// testOTelTracer implements [interfaces.Tracer] and [interfaces.OTelTracer] without dialing OTLP.
type testOTelTracer struct {
	inner trace.Tracer
}

func newTestOTelTracer() *testOTelTracer {
	return &testOTelTracer{inner: noop.NewTracerProvider().Tracer("temporal-config-test")}
}

func (t *testOTelTracer) StartSpan(ctx context.Context, name string, attrs ...interfaces.Attribute) (context.Context, interfaces.Span) {
	ctx, s := t.inner.Start(ctx, name)
	return ctx, spanBridge{s: s}
}

func (t *testOTelTracer) OTelTracer() trace.Tracer { return t.inner }

func (t *testOTelTracer) Shutdown(context.Context) error { return nil }

func TestNewTemporalTracingInterceptor_nilTracer(t *testing.T) {
	i, err := newTemporalTracingInterceptor(nil)
	if err != nil {
		t.Fatal(err)
	}
	if i != nil {
		t.Fatalf("want nil interceptor for nil tracer, got %T", i)
	}
}

func TestNewTemporalTracingInterceptor_otelTracer_nonNil(t *testing.T) {
	i, err := newTemporalTracingInterceptor(newTestOTelTracer())
	if err != nil {
		t.Fatal(err)
	}
	if i == nil {
		t.Fatal("want non-nil interceptor for OTel-capable tracer")
	}
}

func TestBuildTemporalRuntime_userProvidedTemporalClient_otelTracer_warns(t *testing.T) {
	var buf bytes.Buffer
	log := logger.NewWriterLogger(&buf, "warn", "text", false)
	tc := temporalmocks.NewClient(t)

	_, err := buildTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithLogger(log),
		WithTracer(newTestOTelTracer()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "x"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "OTel interceptor manually") {
		t.Fatalf("expected manual OTel interceptor warning; got:\n%s", buf.String())
	}
}

func TestBuildTemporalRuntime_userProvidedTemporalClient_defaultTracer_noManualInterceptorWarn(t *testing.T) {
	var buf bytes.Buffer
	log := logger.NewWriterLogger(&buf, "warn", "text", false)
	tc := temporalmocks.NewClient(t)

	_, err := buildTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithLogger(log),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "x"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "OTel interceptor manually") {
		t.Fatalf("unexpected warning with default noop tracer:\n%s", buf.String())
	}
}

func TestBuildTemporalRuntime_userProvidedTemporalClient_explicitNoopTracer_noManualInterceptorWarn(t *testing.T) {
	var buf bytes.Buffer
	log := logger.NewWriterLogger(&buf, "warn", "text", false)
	tc := temporalmocks.NewClient(t)

	_, err := buildTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithLogger(log),
		WithTracer(observability.DefaultNoopTracer),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "x"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "OTel interceptor manually") {
		t.Fatalf("unexpected warning for explicit noop tracer:\n%s", buf.String())
	}
}

func TestBuildTemporalRuntime_RequiresTemporalOrClient(t *testing.T) {
	// Neither WithTemporalConfig nor WithTemporalClient: must fail fast without dialing a server.
	options := []Option{
		WithLogger(logger.NoopLogger()),
		WithInstanceId("test"),
		WithRemoteWorker(false),
		WithPolicyFingerprint("test"),
		WithMCPFingerprint("test"),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "test"}),
		WithAgentConfig(sdkruntime.AgentConfig{
			LLM: sdkruntime.AgentLLM{Client: stubLLM{}},
		}),
	}
	_, err := buildTemporalRuntime(options...)
	if err == nil || !strings.Contains(err.Error(), "temporal config or client is required") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildTemporalRuntime_RequiresLLMClient(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	_, err := buildTemporalRuntime(
		WithTemporalClient(tc, "tq"),
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "x"}),
		WithAgentConfig(sdkruntime.AgentConfig{}),
	)
	if err == nil || !strings.Contains(err.Error(), "llm client is required") {
		t.Fatalf("got %v", err)
	}
}

func TestBuildTemporalRuntime_InstanceIdSuffix(t *testing.T) {
	tc := temporalmocks.NewClient(t)
	rt, err := buildTemporalRuntime(
		WithTemporalClient(tc, "myq"),
		WithInstanceId("pod1"),
		WithLogger(logger.NoopLogger()),
		WithAgentSpec(sdkruntime.AgentSpec{Name: "x"}),
		WithAgentConfig(sdkruntime.AgentConfig{LLM: sdkruntime.AgentLLM{Client: stubLLM{}}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if rt.taskQueue != "myq-pod1" {
		t.Fatalf("taskQueue = %q, want myq-pod1", rt.taskQueue)
	}
}
