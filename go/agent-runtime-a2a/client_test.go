package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

const (
	testAgentName    = "fixture-agent"
	testAgentVersion = "1.0.0"
	testMessageID    = "message-1"
)

type testA2AObserver struct {
	name   string
	events []plugin.Event
}

func TestClientStreamsA2A10HTTPJSON(t *testing.T) {
	t.Parallel()
	server, _ := newA2ATestServer(t, false)
	client := newA2ATestClient(t, server.Client())
	discovery, err := client.Discover(t.Context(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]StreamEvent, 0, 4)
	for event, streamErr := range client.SendStreamingMessage(t.Context(), discovery, SendRequest{
		MessageID: testMessageID + "-stream", Text: "stream",
	}) {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		events = append(events, event)
	}
	if len(events) < 2 || events[0].Kind != StreamEventTask || events[len(events)-1].Kind != StreamEventStatus ||
		events[len(events)-1].Task == nil || !events[len(events)-1].Task.Terminal {
		t.Fatalf("stream events = %#v", events)
	}
}

func TestClientSubscribesToRunningTask(t *testing.T) {
	t.Parallel()
	server, _ := newA2ATestServer(t, true)
	client := newA2ATestClient(t, server.Client())
	discovery, err := client.Discover(t.Context(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	interaction, err := client.SendMessage(t.Context(), discovery, SendRequest{
		MessageID: testMessageID + "-subscribe", Text: "work",
	})
	if err != nil || interaction.Task == nil || interaction.Task.State != string(a2asdk.TaskStateInputRequired) {
		t.Fatalf("interaction=%#v err=%v", interaction, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var streamErr error
	for _, err = range client.SubscribeToTask(ctx, discovery, interaction.Task.ID) {
		streamErr = err
		break
	}
	if !errors.Is(streamErr, context.Canceled) {
		t.Fatalf("subscription cancellation error = %v", streamErr)
	}
}

func assertA2AObserverSafe(
	t *testing.T,
	observer *testA2AObserver,
	endpoint string,
	secret string,
	want []string,
) {
	t.Helper()
	got := make([]string, 0, len(observer.events))
	for _, event := range observer.events {
		got = append(got, event.Type+":"+event.Status)
	}
	if len(got) != len(want) {
		t.Fatalf("observer events = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("observer events = %#v, want %#v", got, want)
		}
	}
	raw, err := json.Marshal(observer.events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), endpoint) || strings.Contains(string(raw), secret) {
		t.Fatalf("protocol observer leaked endpoint or secret: %s", raw)
	}
}

func (observer *testA2AObserver) Name() string { return observer.name }

func (observer *testA2AObserver) Observe(_ context.Context, event plugin.Event) {
	observer.events = append(observer.events, event)
}

func TestClientDiscoversA2A10HTTPJSONAndSendsMessage(t *testing.T) {
	t.Parallel()
	server, card := newA2ATestServer(t, false)
	observer := &testA2AObserver{name: "a2a-observer"}
	client := newA2ATestClient(t, server.Client(), observer)
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
	assertA2AObserverSafe(t, observer, server.URL, "a2a-observer-secret", []string{
		eventDiscovery + ":started", eventDiscovery + ":completed",
		eventMessage + ":started", eventMessage + ":completed",
		eventTaskGet + ":started", eventTaskGet + ":completed",
	})
}

func TestClientSendsRichContentAndListsTasks(t *testing.T) {
	t.Parallel()
	server, _ := newA2ATestServer(t, false)
	client := newA2ATestClient(t, server.Client())
	discovery, err := client.Discover(t.Context(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	historyLength := 10
	interaction, err := client.SendMessage(t.Context(), discovery, SendRequest{
		MessageID: testMessageID + "-rich",
		Parts: []ContentPart{
			{Kind: ContentPartText, Text: "analyze"},
			{Kind: ContentPartData, Data: json.RawMessage(`{"priority":2}`), MediaType: "application/json"},
			{Kind: ContentPartRaw, Raw: []byte("bytes"), Filename: "input.bin", MediaType: "application/octet-stream"},
			{Kind: ContentPartURL, URL: "https://files.example/input.txt", Filename: "input.txt", MediaType: "text/plain"},
		},
		AcceptedOutputModes: []string{"text/plain", "application/json"}, HistoryLength: &historyLength,
	})
	if err != nil || interaction.Task == nil || len(interaction.Task.History) == 0 {
		t.Fatalf("interaction=%#v err=%v", interaction, err)
	}
	message := interaction.Task.History[0]
	if len(message.Parts) != 4 || message.Parts[1].Kind != ContentPartData ||
		string(message.Parts[1].Data) != `{"priority":2}` || message.Parts[2].Filename != "input.bin" {
		t.Fatalf("rich message projection = %#v", message)
	}
	page, err := client.ListTasks(t.Context(), discovery, ListTasksRequest{
		PageSize: 10, HistoryLength: &historyLength, IncludeArtifacts: true,
	})
	if err != nil || len(page.Tasks) == 0 || page.TotalSize < 1 || page.Tasks[0].ID == "" || !json.Valid(page.Raw) {
		t.Fatalf("page=%#v err=%v", page, err)
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
		Name: testAgentName, Version: testAgentVersion,
		SupportedInterfaces: []*a2asdk.AgentInterface{{
			URL: server.URL, ProtocolBinding: a2asdk.TransportProtocolJSONRPC, ProtocolVersion: a2asdk.Version,
		}},
	}
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	observer := &testA2AObserver{name: "a2a-failure-observer"}
	client := newA2ATestClient(t, server.Client(), observer)
	if _, err := client.Discover(t.Context(), server.URL); !errors.Is(err, ErrUnsupportedProtocol) {
		t.Fatalf("protocol error = %v", err)
	}
	assertA2AObserverSafe(t, observer, server.URL, "a2a-observer-secret", []string{
		eventDiscovery + ":started", eventDiscovery + ":failed",
	})
}

func TestProjectDiscoveryRejectsOversizedRemoteSkillSet(t *testing.T) {
	t.Parallel()
	selected := &a2asdk.AgentInterface{
		URL: testShadowRemoteURL, ProtocolBinding: a2asdk.TransportProtocolHTTPJSON, ProtocolVersion: a2asdk.Version,
	}
	card := &a2asdk.AgentCard{
		Name: testAgentName, Version: testAgentVersion, SupportedInterfaces: []*a2asdk.AgentInterface{selected},
		Skills: make([]a2asdk.AgentSkill, maxAgentSkills+1),
	}
	if _, err := projectDiscovery(card, selected); !errors.Is(err, ErrDiscoveryLimit) {
		t.Fatalf("discovery limit error = %v", err)
	}
}

func newA2ATestClient(t *testing.T, httpClient *http.Client, observers ...plugin.Observer) *Client {
	t.Helper()
	transport, err := NewTransport(TransportDependencies{
		HTTPClient:        httpClient,
		EndpointValidator: EndpointValidatorFunc(func(string) error { return nil }),
		Headers: HeaderProviderFunc(func(context.Context) (http.Header, error) {
			return http.Header{
				"X-Gaoge-Test": []string{"true"}, "A2A-Version": []string{"legacy"},
				"Authorization": []string{"Bearer a2a-observer-secret"},
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientDependencies{Transport: transport, Observers: observers})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func newA2ATestServer(t *testing.T, requireInput bool) (*httptest.Server, *a2asdk.AgentCard) {
	t.Helper()
	requestHandler := a2asrv.NewHandler(
		testExecutor{requireInput: requireInput},
		a2asrv.WithCallInterceptors(testAuthInterceptor{}),
	)
	mux := http.NewServeMux()
	var card *a2asdk.AgentCard
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewAgentCardHandler(a2asrv.AgentCardProducerFn(
		func(context.Context) (*a2asdk.AgentCard, error) { return card, nil },
	)))
	mux.Handle("/", requireA2AVersion(t, a2asrv.NewRESTHandler(requestHandler)))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	card = &a2asdk.AgentCard{
		Name: testAgentName, Description: "A2A fixture", Version: testAgentVersion,
		Capabilities:      a2asdk.AgentCapabilities{Streaming: true},
		DefaultInputModes: []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
		SupportedInterfaces: []*a2asdk.AgentInterface{
			{URL: server.URL, ProtocolBinding: a2asdk.TransportProtocolJSONRPC, ProtocolVersion: a2asdk.Version},
			{URL: server.URL, ProtocolBinding: a2asdk.TransportProtocolHTTPJSON, ProtocolVersion: a2asdk.Version},
		},
		Skills: []a2asdk.AgentSkill{{ID: "echo", Name: "Echo", Description: "Echo a message", Tags: []string{"test"}}},
		SecuritySchemes: a2asdk.NamedSecuritySchemes{
			"bearer": a2asdk.HTTPAuthSecurityScheme{Scheme: "Bearer", BearerFormat: "JWT"},
		},
		SecurityRequirements: a2asdk.SecurityRequirementsOptions{
			{a2asdk.SecuritySchemeName("bearer"): a2asdk.SecuritySchemeScopes{}},
		},
		Signatures: []a2asdk.AgentCardSignature{{Protected: "eyJhbGciOiJFZERTQSJ9", Signature: "fixture-signature"}},
	}
	return server, card
}

type testAuthInterceptor struct {
	a2asrv.PassthroughCallInterceptor
}

func (testAuthInterceptor) Before(
	ctx context.Context,
	call *a2asrv.CallContext,
	_ *a2asrv.Request,
) (context.Context, any, error) {
	call.User = a2asrv.NewAuthenticatedUser("fixture-user", nil)
	return ctx, nil, nil
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
		!strings.Contains(string(discovery.CapabilitiesJSON), `"streaming":true`) ||
		len(discovery.SecuritySchemes) != 1 || discovery.SecuritySchemes[0].Name != "bearer" ||
		discovery.SecuritySchemes[0].Type != "http" || len(discovery.Signatures) != 1 ||
		!json.Valid(discovery.CardJSON) || !json.Valid(discovery.SecurityRequirementsJSON) {
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
