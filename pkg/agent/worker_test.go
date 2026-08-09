package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/golang/mock/gomock"

	rtmocks "github.com/agenticenv/agent-sdk-go/internal/runtime/mocks"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
)

func TestNewAgentWorker_requiresTemporal(t *testing.T) {
	_, err := NewAgentWorker(WithName("w"), WithLLMClient(testLLM(t)))
	if err == nil || !strings.Contains(err.Error(), "AgentWorker requires a Temporal backend") {
		t.Fatalf("got %v", err)
	}
}

func TestAgentWorker_Start_WhenRuntimeNotWorkerRuntime(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	rt := rtmocks.NewMockRuntime(ctrl)
	aw := &AgentWorker{
		agentConfig: agentConfig{
			Name:      "w",
			logger:    logger.DefaultLogger("error"),
			LLMClient: testLLM(t),
		},
		runtime:   rt,
		taskQueue: "tq",
	}
	err := aw.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "WorkerRuntime") {
		t.Fatalf("got %v", err)
	}
}

func TestAgentWorker_Start_Stop_WithWorkerRuntime(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	wr := rtmocks.NewMockWorkerRuntime(ctrl)
	wr.EXPECT().Start(gomock.Any()).Return(nil)
	wr.EXPECT().Stop()

	aw := &AgentWorker{
		agentConfig: agentConfig{
			Name:   "w",
			logger: logger.DefaultLogger("error"),
		},
		runtime:   wr,
		taskQueue: "tq",
	}
	if err := aw.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	aw.Stop()
}
