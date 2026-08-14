package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

type runFeedPrincipal struct{ actor kernel.ActorRef }

func (principal runFeedPrincipal) ResolvePrincipal(*gin.Context) (kernel.ActorRef, error) {
	return principal.actor, nil
}

func TestStreamRunFeedReplaysAfterSequenceThroughTerminal(t *testing.T) {
	engine, runtime, feed, actor := newRunFeedHTTPTest(t)
	createHTTPTestRun(t, runtime, actor, "run-feed")
	publishHTTPTestEvent(t, feed, "run-feed", runfeed.Draft{Type: runfeed.EventRunStarted})
	publishHTTPTestEvent(t, feed, "run-feed", runfeed.Draft{Type: runfeed.EventModelDelta, Delta: "hello"})
	publishHTTPTestEvent(t, feed, "run-feed", runfeed.Draft{Type: runfeed.EventRunCompleted, Terminal: true})

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/runs/run-feed/feed?afterSeq=1", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("response = %d, %q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	body := recorder.Body.String()
	if strings.Contains(body, "id: 1\n") || !strings.Contains(body, "id: 2\n") || !strings.Contains(body, "id: 3\n") ||
		!strings.Contains(body, `"delta":"hello"`) || !strings.Contains(body, `"terminal":true`) {
		t.Fatalf("unexpected SSE body: %s", body)
	}
}

func TestStreamRunFeedDisconnectDoesNotCancelRun(t *testing.T) {
	engine, runtime, _, actor := newRunFeedHTTPTest(t)
	createHTTPTestRun(t, runtime, actor, "run-disconnect")
	requestContext, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequestWithContext(requestContext, http.MethodGet, "/api/v1/runs/run-disconnect/feed", nil)
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		engine.ServeHTTP(recorder, request)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not release disconnected subscriber")
	}
	snapshot, err := runtime.Load(t.Context(), "run-disconnect")
	if err != nil || snapshot.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("disconnect changed run = %#v, %v", snapshot.Run, err)
	}
}

func TestStreamRunFeedHidesAnotherActorsRun(t *testing.T) {
	engine, runtime, _, _ := newRunFeedHTTPTest(t)
	createHTTPTestRun(t, runtime, kernel.ActorRef{TenantID: "tenant", ActorID: "other"}, "run-hidden")
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/runs/run-hidden/feed", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func newRunFeedHTTPTest(t *testing.T) (*gin.Engine, *kernel.Runtime, *runfeed.Feed, kernel.ActorRef) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{
		Retention: time.Minute, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	engine := gin.New()
	NewModule(NewHandler(Dependencies{
		Runtime: runtime, Feed: feed, PrincipalResolver: runFeedPrincipal{actor: actor},
	})).RegisterRoutes(engine.Group("/api/v1"))
	return engine, runtime, feed, actor
}

func createHTTPTestRun(t *testing.T, runtime *kernel.Runtime, actor kernel.ActorRef, runID string) {
	t.Helper()
	_, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: runID, Kind: kernel.RunKind("http_test"), Actor: actor,
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "conversation-1"},
		Goal:   "test", State: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func publishHTTPTestEvent(t *testing.T, feed *runfeed.Feed, runID string, draft runfeed.Draft) {
	t.Helper()
	if _, err := feed.Publish(t.Context(), runID, draft); err != nil {
		t.Fatal(err)
	}
}
