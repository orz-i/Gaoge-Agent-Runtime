package a2a

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

func TestHostedAgentCardSupportsConditionalCaching(t *testing.T) {
	t.Parallel()
	host := newA2AHostedTestHost(t, &testHostedAgent{})

	request := httptest.NewRequest(http.MethodGet, a2asrv.WellKnownAgentCardPath, nil)
	response := httptest.NewRecorder()
	host.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("agent card status = %d", response.Code)
	}
	etag := response.Header().Get("ETag")
	if etag == "" || response.Header().Get("Last-Modified") == "" ||
		response.Header().Get("Cache-Control") != "public, max-age=300" {
		t.Fatalf("agent card cache headers = %#v", response.Header())
	}

	conditional := httptest.NewRequest(http.MethodGet, a2asrv.WellKnownAgentCardPath, nil)
	conditional.Header.Set("If-None-Match", etag)
	notModified := httptest.NewRecorder()
	host.Handler().ServeHTTP(notModified, conditional)
	if notModified.Code != http.StatusNotModified || notModified.Body.Len() != 0 {
		t.Fatalf("conditional response status=%d body=%q", notModified.Code, notModified.Body.String())
	}
}

func TestHostRejectsUnsupportedProtocolVersionBeforeDispatch(t *testing.T) {
	t.Parallel()
	host := newA2AHostedTestHost(t, &testHostedAgent{})
	request := httptest.NewRequest(http.MethodGet, "/tasks/not-present", nil)
	request.Header.Set(a2asdk.SvcParamVersion, "9.9")
	response := httptest.NewRecorder()

	host.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "VERSION_NOT_SUPPORTED") {
		t.Fatalf("version response status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestHostedSubscriptionReplaysThenStreamsToTerminal(t *testing.T) {
	t.Parallel()
	agent := hostedAgentFunc(func(_ context.Context, request HostedRequest, sink HostedSink) error {
		if request.MessageView.TaskID == "" {
			return sink(HostedEvent{
				Kind: HostedEventStatus, Status: HostedStatusInputRequired,
				MessageID: "input-prompt", Text: "More input is required",
			})
		}
		return sink(HostedEvent{Kind: HostedEventStatus, Status: HostedStatusCompleted})
	})
	server := newA2AProductionTestServer(t, newTestHostedTaskStore(), agent)
	client := newHostedAuthClient(t, server.Client(), "alice")
	discovery, err := client.Discover(t.Context(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	interaction, err := client.SendMessage(t.Context(), discovery, SendRequest{
		MessageID: "subscription-start", Text: "start",
	})
	if err != nil || interaction.Task == nil || interaction.Task.Terminal ||
		interaction.Task.State != string(a2asdk.TaskStateInputRequired) {
		t.Fatalf("initial interaction=%#v err=%v", interaction, err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type observedEvent struct {
		event StreamEvent
		err   error
	}
	observed := make(chan observedEvent, 8)
	go func() {
		defer close(observed)
		for event, streamErr := range client.SubscribeToTask(ctx, discovery, interaction.Task.ID) {
			observed <- observedEvent{event: event, err: streamErr}
			if streamErr != nil {
				return
			}
		}
	}()

	first := <-observed
	if first.err != nil || first.event.Kind != StreamEventTask || first.event.Task == nil || first.event.Task.Terminal {
		t.Fatalf("initial subscription event=%#v err=%v", first.event, first.err)
	}
	completed, err := client.SendMessage(t.Context(), discovery, SendRequest{
		MessageID: "subscription-complete", TaskID: interaction.Task.ID,
		ContextID: interaction.Task.ContextID, Text: "complete",
	})
	if err != nil || completed.Task == nil || !completed.Task.Terminal {
		t.Fatalf("completion=%#v err=%v", completed, err)
	}

	terminal := false
	for item := range observed {
		if item.err != nil {
			t.Fatal(item.err)
		}
		if item.event.Kind == StreamEventMessage {
			t.Fatalf("subscription leaked inbound message: %#v", item.event)
		}
		if item.event.Task != nil && item.event.Task.Terminal {
			terminal = true
		}
	}
	if !terminal {
		t.Fatal("subscription ended without a terminal task event")
	}
}
