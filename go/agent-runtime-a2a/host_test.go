package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

const (
	testHostedPublicURL = "https://agent.example/a2a"
	testHostedMediaType = "text/plain"
)

type testHostedAgent struct {
	requests []HostedRequest
	cancels  []HostedCancelRequest
}

func (agent *testHostedAgent) Execute(_ context.Context, request HostedRequest, sink HostedSink) error {
	agent.requests = append(agent.requests, request)
	if err := sink(HostedEvent{Kind: HostedEventArtifact, ArtifactID: "artifact-1", Text: "chunk-1", Name: "answer"}); err != nil {
		return err
	}
	return sink(HostedEvent{
		Kind: HostedEventArtifact, ArtifactID: "artifact-1", Append: true, LastChunk: true,
		Parts: []ContentPart{{Kind: ContentPartData, Data: json.RawMessage(`{"chunk":2}`), MediaType: "application/json"}},
	})
}

func (agent *testHostedAgent) Cancel(_ context.Context, request HostedCancelRequest) error {
	agent.cancels = append(agent.cancels, request)
	return nil
}

func TestHostServesA2A10HTTPJSON(t *testing.T) {
	t.Parallel()
	agent := &testHostedAgent{}
	host := newA2AHostedTestHost(t, agent)
	server := httptest.NewServer(host.Handler())
	t.Cleanup(server.Close)
	assertHostedAgentCard(t, server)
	client := newA2ATestClient(t, server.Client())
	discovery := hostedTestDiscovery(server.URL)
	interaction, err := client.SendMessage(t.Context(), discovery, SendRequest{MessageID: "hosted-message", Text: "hello hosted"})
	assertHostedExecution(t, interaction, err, agent)
}

func assertHostedAgentCard(t *testing.T, server *httptest.Server) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/.well-known/agent-card.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Errorf("close agent card response: %v", closeErr)
		}
	}()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("agent card status = %d", response.StatusCode)
	}
}

func assertHostedExecution(t *testing.T, interaction Interaction, err error, agent *testHostedAgent) {
	t.Helper()
	if err != nil || interaction.Task == nil || !interaction.Task.Terminal {
		t.Fatalf("interaction=%#v err=%v", interaction, err)
	}
	if len(agent.requests) != 1 || !json.Valid(agent.requests[0].Message) || agent.requests[0].TaskID == "" {
		t.Fatalf("hosted requests = %#v", agent.requests)
	}
	if agent.requests[0].MessageView.ID == "" || len(agent.requests[0].MessageView.Parts) != 1 ||
		interaction.Task == nil || len(interaction.Task.Artifacts) != 1 || len(interaction.Task.Artifacts[0].Parts) != 2 ||
		interaction.Task.Artifacts[0].Parts[1].Kind != ContentPartData {
		t.Fatalf("hosted rich projection request=%#v interaction=%#v", agent.requests[0], interaction)
	}
}

func TestHostStreamsHostedAgentEvents(t *testing.T) {
	t.Parallel()
	agent := &testHostedAgent{}
	host := newA2AHostedTestHost(t, agent)
	server := httptest.NewServer(host.Handler())
	t.Cleanup(server.Close)
	client := newA2ATestClient(t, server.Client())

	events := make([]StreamEvent, 0, 8)
	for event, err := range client.SendStreamingMessage(t.Context(), hostedTestDiscovery(server.URL), SendRequest{
		MessageID: "hosted-stream", Text: "stream hosted",
	}) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	artifactCount := 0
	for _, event := range events {
		if event.Kind == StreamEventArtifact {
			artifactCount++
		}
	}
	if artifactCount != 2 || len(events) < 4 || events[len(events)-1].Kind != StreamEventStatus ||
		events[len(events)-1].Task == nil || !events[len(events)-1].Task.Terminal {
		t.Fatalf("stream events = %#v", events)
	}
}

func TestHostedMessageBecomesTerminalTaskStatus(t *testing.T) {
	t.Parallel()
	execContext := &a2asrv.ExecutorContext{TaskID: "task-1", ContextID: "context-1"}
	event, err := projectHostedMessage(execContext, HostedEvent{
		Kind: HostedEventMessage, MessageID: "message-1",
		Parts: []ContentPart{{Kind: ContentPartData, Data: json.RawMessage(`{"answer":42}`)}},
	})
	if err != nil || event.Status.State != a2asdk.TaskStateCompleted || event.Status.Message == nil ||
		len(event.Status.Message.Parts) != 1 {
		t.Fatalf("event=%#v err=%v", event, err)
	}
	authEvent, terminal, err := projectHostedStatus(execContext, HostedEvent{
		Kind: HostedEventStatus, Status: HostedStatusAuthRequired,
		MessageID: "auth-prompt", Text: "authenticate to continue",
	})
	status, ok := authEvent.(*a2asdk.TaskStatusUpdateEvent)
	if err != nil || !terminal || !ok || status.Status.Message == nil || status.Status.Message.ID != "auth-prompt" {
		t.Fatalf("auth event=%#v terminal=%v err=%v", authEvent, terminal, err)
	}
}

func TestHostCanReturnDirectMessageWithoutCreatingTask(t *testing.T) {
	t.Parallel()
	agent := hostedAgentFunc(func(_ context.Context, _ HostedRequest, sink HostedSink) error {
		return sink(HostedEvent{
			Kind: HostedEventDirectMessage, MessageID: "direct-message", Text: "direct response",
		})
	})
	host := newA2AHostedTestHost(t, agent)
	server := httptest.NewServer(host.Handler())
	t.Cleanup(server.Close)

	client := newA2ATestClient(t, server.Client())
	interaction, err := client.SendMessage(t.Context(), hostedTestDiscovery(server.URL), SendRequest{
		MessageID: "direct-request", Text: "respond directly",
	})
	if err != nil || interaction.Message == nil || interaction.Task != nil || interaction.Message.ID != "direct-message" ||
		len(interaction.Message.Parts) != 1 || interaction.Message.Parts[0].Text != "direct response" {
		t.Fatalf("interaction=%#v err=%v", interaction, err)
	}
}

func TestDirectMessageCannotFollowTaskEvents(t *testing.T) {
	t.Parallel()
	execContext := &a2asrv.ExecutorContext{TaskID: "task-1", ContextID: "context-1"}
	execution := hostedExecution{taskStarted: true}
	err := execution.captureDirectMessage(execContext, HostedEvent{
		Kind: HostedEventDirectMessage, MessageID: "direct-message", Text: "invalid",
	})
	if !errors.Is(err, ErrInvalidHostedEvent) {
		t.Fatalf("error = %v", err)
	}
}

type hostedAgentFunc func(context.Context, HostedRequest, HostedSink) error

func (function hostedAgentFunc) Execute(ctx context.Context, request HostedRequest, sink HostedSink) error {
	return function(ctx, request, sink)
}

func (hostedAgentFunc) Cancel(context.Context, HostedCancelRequest) error { return nil }

func newA2AHostedTestHost(t *testing.T, agent HostedAgent) *Host {
	t.Helper()
	host, err := NewHost(HostDependencies{
		Card: HostedCard{
			PublicURL: testHostedPublicURL, Name: "hosted-agent", Description: "hosted fixture", Version: testAgentVersion,
			DefaultInputModes: []string{testHostedMediaType}, DefaultOutputModes: []string{testHostedMediaType},
			Skills: []RemoteAgentSkill{{ID: "hosted", Name: "Hosted", Description: "Hosted skill", Tags: []string{"test"}}},
		},
		Agent: agent,
	})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func hostedTestDiscovery(endpoint string) Discovery {
	return Discovery{Descriptor: RemoteAgentDescriptor{
		Name: "hosted-agent", PreferredURL: endpoint, ProtocolVersion: ProtocolVersion,
		ProtocolBinding: "HTTP+JSON",
	}}
}
