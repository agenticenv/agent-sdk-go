package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/golang/mock/gomock"

	"github.com/agenticenv/agent-sdk-go/internal/runtime"
	rtmocks "github.com/agenticenv/agent-sdk-go/internal/runtime/mocks"
	"github.com/agenticenv/agent-sdk-go/pkg/interfaces"
)

func TestWithLLMSelector_ValidationAndFingerprint(t *testing.T) {
	if _, err := buildAgentConfig([]Option{
		WithName("sel"),
		WithLLMClient(testLLM(t)),
		WithLLMSelector(LLMSelectorConfig{}),
	}); err == nil || !strings.Contains(err.Error(), "Select is required") {
		t.Fatalf("nil Select: %v", err)
	}
	if _, err := buildAgentConfig([]Option{
		WithName("sel"),
		WithLLMClient(testLLM(t)),
		WithLLMSelector(LLMSelectorConfig{
			Select: func(context.Context, LLMSelectInfo) (string, error) { return "", nil },
			Mode:   LLMSelectMode(9),
		}),
	}); err == nil || !strings.Contains(err.Error(), "invalid mode") {
		t.Fatalf("invalid mode: %v", err)
	}

	if got := llmSelectorFingerprint(nil); got != "" {
		t.Fatalf("nil fingerprint = %q", got)
	}
	perRun := &LLMSelectorConfig{Select: func(context.Context, LLMSelectInfo) (string, error) { return "", nil }}
	perCall := &LLMSelectorConfig{
		Select: func(context.Context, LLMSelectInfo) (string, error) { return "local", nil },
		Mode:   LLMSelectPerCall,
	}
	if llmSelectorFingerprint(perRun) == "" {
		t.Fatal("a set selector must change the fingerprint")
	}
	if llmSelectorFingerprint(perRun) == llmSelectorFingerprint(perCall) {
		t.Fatal("mode must change the fingerprint")
	}
	otherBody := &LLMSelectorConfig{Select: func(context.Context, LLMSelectInfo) (string, error) { return "x", nil }}
	if llmSelectorFingerprint(perRun) != llmSelectorFingerprint(otherBody) {
		t.Fatal("the function body is not part of the fingerprint")
	}

	plain, err := buildAgentConfig([]Option{WithName("plain"), WithLLMClient(testLLM(t))})
	if err != nil {
		t.Fatal(err)
	}
	withSel, err := buildAgentConfig([]Option{
		WithName("plain"),
		WithLLMClient(testLLM(t)),
		WithLLMSelector(LLMSelectorConfig{Select: perRun.Select}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if agentConfigFingerprint(plain) == agentConfigFingerprint(withSel) {
		t.Fatal("caller and worker fingerprints must differ when only one side sets a selector")
	}
}

func TestRunAndStream_LLMClientOption(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockRT := rtmocks.NewMockRuntime(ctrl)
	expectRuntimeRunCompleted(ctrl, mockRT, "run-1", &AgentRunResult{Content: "ok"}, func(req *runtime.RunRequest) {
		if req.LLMClient != "local" {
			t.Errorf("Run LLMClient = %q", req.LLMClient)
		}
	})
	expectRuntimeStreamHandle(ctrl, mockRT, "stream-1", func(req *runtime.RunRequest) {
		if req.LLMClient != "local" {
			t.Errorf("Stream LLMClient = %q", req.LLMClient)
		}
	})

	a := testAgentWithRuntime(mockRT)
	a.namedLLMClients = map[string]interfaces.LLMClient{"local": testLLM(t)}

	if _, err := a.Run(context.Background(), "hi", &AgentRunOptions{LLMClient: "missing"}); err == nil || !strings.Contains(err.Error(), `LLMClient "missing" is not in WithNamedLLMClients`) {
		t.Fatalf("unknown run client: %v", err)
	}
	if _, err := a.Run(context.Background(), "hi", &AgentRunOptions{LLMClient: "local"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Stream(context.Background(), "hi", &AgentStreamOptions{LLMClient: "local"}); err != nil {
		t.Fatal(err)
	}
}
