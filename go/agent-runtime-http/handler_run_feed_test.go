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

const testRunFeedTenantID = "tenant"

type runFeedPrincipal struct{ actor kernel.ActorRef }

func (principal runFeedPrincipal) ResolvePrincipal(*gin.Context) (kernel.ActorRef, error) {
	return principal.actor, nil
}

func TestGetRunReleasesFeedMetadataForAuthoritativeTerminalSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	actor := kernel.ActorRef{TenantID: testRunFeedTenantID, ActorID: "actor"}
	created, err := runtime.Create(t.Context(), kernel.CreateRequest{
		ID: "run-terminal-release", Kind: kernel.RunKind("http_test"), Actor: actor,
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "conversation-1"},
		Goal:   "test", State: json.RawMessage(`{"step":0}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.Apply(t.Context(), created.Run.ID, created.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusCompleted, State: json.RawMessage(`{"step":1}`),
		Result: &kernel.Result{ContentType: "text", Content: json.RawMessage(`"done"`)},
		Events: []kernel.EventDraft{{Type: "run.completed"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &releaseRecordingRunFeedStore{Store: memory.NewRunFeedStore()}
	feed, err := runfeed.New(store, runfeed.Options{Retention: time.Minute, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	publishHTTPTestEvent(t, feed, created.Run.ID, runfeed.Draft{Type: runfeed.EventRunStarted})
	engine := gin.New()
	NewModule(NewHandler(Dependencies{Runtime: runtime, Feed: feed})).RegisterRoutes(engine.Group("/api/v1"))

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/runs/run-terminal-release", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || len(store.releases) != 1 || store.releases[0] != created.Run.ID {
		t.Fatalf("terminal Run observation did not release feed metadata: status=%d releases=%#v body=%s", recorder.Code, store.releases, recorder.Body.String())
	}
}

type releaseRecordingRunFeedStore struct {
	runfeed.Store
	releases []string
}

func (store *releaseRecordingRunFeedStore) ReleaseTerminal(ctx context.Context, runID string, retention time.Duration) error {
	store.releases = append(store.releases, runID)
	return store.Store.ReleaseTerminal(ctx, runID, retention)
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
	createHTTPTestRun(t, runtime, kernel.ActorRef{TenantID: testRunFeedTenantID, ActorID: "other"}, "run-hidden")
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/runs/run-hidden/feed", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestWriteRunFeedCursorExpiredReturnsRecoveryHead(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)

	if !writeRunFeedCursorExpired(context, &runfeed.CursorExpiredError{AfterSeq: 2, HeadSeq: 7}) {
		t.Fatal("cursor expiry was not handled")
	}
	if recorder.Code != http.StatusConflict || recorder.Header().Get("X-Run-Feed-Head") != "7" ||
		!strings.Contains(recorder.Body.String(), `"code":"runfeed.cursor_expired"`) {
		t.Fatalf("cursor response = %d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
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
	actor := kernel.ActorRef{TenantID: testRunFeedTenantID, ActorID: "actor"}
	engine := gin.New()
	NewModule(NewHandler(Dependencies{
		Runtime: runtime, Feed: feed, Shared: NewShared(runFeedPrincipal{actor: actor}, nil),
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
