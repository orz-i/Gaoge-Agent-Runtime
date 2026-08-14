package a2a

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

const (
	testAgentName = "fixture-agent"
	testMessageID = "message-1"
)

func TestClientDiscoversA2A10HTTPJSONAndSendsMessage(t *testing.T) {
	t.Parallel()
	server, card := newA2ATestServer(t, false)
	client := newA2ATestClient(t, server.Client())
	discovery, err := client.Discover(t.Context(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	assertA2ATestDiscovery(t, discovery, card)
	interaction, err := client.SendMessage(t.Context(), discovery, SendRequest{MessageID: testMessageID, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if interaction.Task == nil || interaction.Task.State != string(a2asdk.TaskStateCompleted) || !interaction.Task.Terminal {
		t.Fatalf("unexpected interaction: %#v", interaction)
	}
	loaded, err := client.GetTask(t.Context(), discovery, interaction.Task.ID)
	if err != nil || loaded.ID != interaction.Task.ID || loaded.State != string(a2asdk.TaskStateCompleted) {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestClientCancelTaskUsesExactA2AVersion(t *testing.T) {
	t.Parallel()
	server, _ := newA2ATestServer(t, true)
	client := newA2ATestClient(t, server.Client())
	discovery, err := client.Discover(t.Context(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	interaction, err := client.SendMessage(t.Context(), discovery, SendRequest{MessageID: testMessageID, Text: "work"})
	if err != nil || interaction.Task == nil || interaction.Task.State != string(a2asdk.TaskStateInputRequired) {
		t.Fatalf("interaction=%#v err=%v", interaction, err)
	}
	canceled, err := client.CancelTask(t.Context(), discovery, interaction.Task.ID)
	if err != nil || canceled.State != string(a2asdk.TaskStateCanceled) || !canceled.Terminal {
		t.Fatalf("canceled=%#v err=%v", canceled, err)
	}
}

func TestClientRejectsAgentCardWithoutA2A10HTTPJSON(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	card := &a2asdk.AgentCard{
		Name: testAgentName, Version: "1.0.0",
		SupportedInterfaces: []*a2asdk.AgentInterface{{
			URL: server.URL, ProtocolBinding: a2asdk.TransportProtocolJSONRPC, ProtocolVersion: a2asdk.Version,
		}},
	}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	client := newA2ATestClient(t, server.Client())
	if _, err := client.Discover(t.Context(), server.URL); !errors.Is(err, ErrUnsupportedProtocol) {
		t.Fatalf("protocol error = %v", err)
	}
}

func newA2ATestClient(t *testing.T, httpClient *http.Client) *Client {
	t.Helper()
	transport, err := NewTransport(TransportDependencies{
		HTTPClient:        httpClient,
		EndpointValidator: EndpointValidatorFunc(func(string) error { return nil }),
		Headers: HeaderProviderFunc(func(context.Context) (http.Header, error) {
			return http.Header{"X-Gaoge-Test": []string{"true"}, "A2A-Version": []string{"legacy"}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientDependencies{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newA2ATestServer(t *testing.T, requireInput bool) (*httptest.Server, *a2asdk.AgentCard) {
	t.Helper()
	requestHandler := a2asrv.NewHandler(testExecutor{requireInput: requireInput})
	mux := http.NewServeMux()
	var card *a2asdk.AgentCard
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewAgentCardHandler(a2asrv.AgentCardProducerFn(
		func(context.Context) (*a2asdk.AgentCard, error) { return card, nil },
	)))
	mux.Handle("/", requireA2AVersion(t, a2asrv.NewRESTHandler(requestHandler)))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	card = &a2asdk.AgentCard{
		Name: testAgentName, Description: "A2A fixture", Version: "1.0.0",
		Capabilities:      a2asdk.AgentCapabilities{Streaming: true},
		DefaultInputModes: []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
		SupportedInterfaces: []*a2asdk.AgentInterface{
			{URL: server.URL, ProtocolBinding: a2asdk.TransportProtocolJSONRPC, ProtocolVersion: a2asdk.Version},
			{URL: server.URL, ProtocolBinding: a2asdk.TransportProtocolHTTPJSON, ProtocolVersion: a2asdk.Version},
		},
		Skills: []a2asdk.AgentSkill{{ID: "echo", Name: "Echo", Description: "Echo a message", Tags: []string{"test"}}},
	}
	return server, card
}

func requireA2AVersion(t *testing.T, next http.Handler) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if version := request.Header.Get("A2A-Version"); version != ProtocolVersion {
			t.Errorf("A2A-Version = %q, want %q", version, ProtocolVersion)
			http.Error(writer, "bad protocol version", http.StatusBadRequest)
			return
		}
		if request.Header.Get("X-Gaoge-Test") != "true" {
			t.Error("host request header missing")
		}
		next.ServeHTTP(writer, request)
	})
}

func assertA2ATestDiscovery(t *testing.T, discovery Discovery, card *a2asdk.AgentCard) {
	t.Helper()
	if discovery.Descriptor.Name != testAgentName || discovery.Descriptor.ProtocolVersion != ProtocolVersion ||
		discovery.Descriptor.ProtocolBinding != string(a2asdk.TransportProtocolHTTPJSON) ||
		discovery.Descriptor.PreferredURL != card.SupportedInterfaces[1].URL {
		t.Fatalf("unexpected descriptor: %#v", discovery.Descriptor)
	}
	if len(discovery.Skills) != 1 || discovery.Skills[0].ID != "echo" ||
		!strings.Contains(string(discovery.CapabilitiesJSON), `"streaming":true`) {
		t.Fatalf("unexpected discovery: %#v", discovery)
	}
}

type testExecutor struct {
	requireInput bool
}

func (executor testExecutor) Execute(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2asdk.Event, error] {
	return func(yield func(a2asdk.Event, error) bool) {
		if execCtx.StoredTask == nil && !yield(a2asdk.NewSubmittedTask(execCtx, execCtx.Message), nil) {
			return
		}
		state := a2asdk.TaskStateCompleted
		if executor.requireInput {
			state = a2asdk.TaskStateInputRequired
		}
		yield(a2asdk.NewStatusUpdateEvent(execCtx, state, nil), nil)
	}
}

func (testExecutor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2asdk.Event, error] {
	return func(yield func(a2asdk.Event, error) bool) {
		yield(a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateCanceled, nil), nil)
	}
}
