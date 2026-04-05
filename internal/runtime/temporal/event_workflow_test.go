package temporal

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/agenticenv/agent-sdk-go/internal/eventbus"
	"github.com/agenticenv/agent-sdk-go/internal/types"
	"github.com/agenticenv/agent-sdk-go/pkg/logger"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/testsuite"
)

func TestEventPublishActivity_PublishesToEventBus(t *testing.T) {
	l := logger.NoopLogger()
	bus := eventbus.NewInmem(l)
	rt := &TemporalRuntime{
		TemporalRuntimeConfig: TemporalRuntimeConfig{logger: l},
		eventbus:              bus,
	}
	ctx := context.Background()
	chName := "agent_event_unit_test"
	subCh, closeFn, err := bus.Subscribe(ctx, chName)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeFn(); err != nil {
			t.Errorf("close subscription: %v", err)
		}
	}()

	actEnv := newActivityTestEnv(t)
	actEnv.RegisterActivity(rt.EventPublishActivity)
	ev := &types.AgentEvent{Type: types.AgentEventContent, Content: "hello-event"}
	if _, err := actEnv.ExecuteActivity(rt.EventPublishActivity, chName, ev); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-subCh:
		var got types.AgentEvent
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got.Type != types.AgentEventContent || got.Content != "hello-event" {
			t.Fatalf("decoded event = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for published event")
	}
}

func TestEventPublishActivity_NilEventErrors(t *testing.T) {
	l := logger.NoopLogger()
	rt := &TemporalRuntime{
		TemporalRuntimeConfig: TemporalRuntimeConfig{logger: l},
		eventbus:              eventbus.NewInmem(l),
	}
	actEnv := newActivityTestEnv(t)
	actEnv.RegisterActivity(rt.EventPublishActivity)
	_, err := actEnv.ExecuteActivity(rt.EventPublishActivity, "ch", nil)
	if err == nil {
		t.Fatal("expected error for nil event")
	}
}

func TestSubscribeToAgentEvents_RoundTrip(t *testing.T) {
	l := logger.NoopLogger()
	bus := eventbus.NewInmem(l)
	rt := &TemporalRuntime{
		TemporalRuntimeConfig: TemporalRuntimeConfig{logger: l},
		eventbus:              bus,
	}
	ctx := context.Background()
	chName := "agent_event_sub_test"
	evCh, closeFn, err := rt.subscribeToAgentEvents(ctx, chName)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeFn(); err != nil {
			t.Errorf("close: %v", err)
		}
	}()

	payload, err := json.Marshal(&types.AgentEvent{Type: types.AgentEventContent, Content: "payload"})
	if err != nil {
		t.Fatal(err)
	}
	if err := bus.Publish(ctx, chName, payload); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-evCh:
		if ev == nil || ev.Content != "payload" || ev.Type != types.AgentEventContent {
			t.Fatalf("event = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for decoded event (goroutine must unblock unbuffered send)")
	}
}

func TestAgentEventWorkflow_CompleteSignalExits(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)

	env.RegisterWorkflow(rt.AgentEventWorkflow)
	env.RegisterActivity(rt.EventPublishActivity)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(eventWorkflowCompleteSignal, nil)
	}, time.Millisecond)

	env.ExecuteWorkflow(rt.AgentEventWorkflow)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
}

func TestAgentEventWorkflow_UpdateTriggersEventPublishActivity(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	rt := testRuntimeForWorkflow(t)

	var gotChannel string
	var gotEvent *types.AgentEvent
	env.RegisterWorkflow(rt.AgentEventWorkflow)
	env.OnActivity(rt.EventPublishActivity, mock.Anything, mock.Anything, mock.Anything).Return(
		func(ctx context.Context, channel string, event *types.AgentEvent) error {
			gotChannel = channel
			gotEvent = event
			return nil
		},
	)

	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflowNoRejection(agentEventName, "upd-1", t, &AgentEventUpdate{
			AgentName:        rt.agentName,
			LocalChannelName: "agent_event_mock_run",
			Event:            &types.AgentEvent{Type: types.AgentEventContent, Content: "via-update"},
		})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(eventWorkflowCompleteSignal, nil)
	}, 50*time.Millisecond)

	env.ExecuteWorkflow(rt.AgentEventWorkflow)

	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	require.Equal(t, "agent_event_mock_run", gotChannel)
	require.NotNil(t, gotEvent)
	require.Equal(t, types.AgentEventContent, gotEvent.Type)
	require.Equal(t, "via-update", gotEvent.Content)
}
