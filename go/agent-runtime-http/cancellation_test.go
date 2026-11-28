package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type recordingCanceller struct {
	runtime  *kernel.Runtime
	calls    int
	revision uint64
}

func (c *recordingCanceller) Cancel(ctx context.Context, id string, revision uint64, reason string) (kernel.Snapshot, error) {
	c.calls++
	c.revision = revision
	return c.runtime.Cancel(ctx, id, revision, reason)
}

func TestCancelUsesAuthorizedOwnerAndSharedSnapshotWithoutPublishingFeed(t *testing.T) {
	_, runtime, feed, actor := newRunFeedHTTPTest(t)
	createHTTPTestRun(t, runtime, actor, "cancel-owner")
	owner := &recordingCanceller{runtime: runtime}
	for _, authorized := range []bool{false, true} {
		principal := actor
		if !authorized {
			principal.ActorID = "other"
		}
		router := gin.New()
		NewModule(NewHandler(Dependencies{Runtime: runtime, Feed: feed, Canceller: owner,
			Shared: NewShared(runFeedPrincipal{actor: principal}, nil, runtime, nil),
		})).RegisterRoutes(router.Group("/v1"))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/runs/cancel-owner/cancel", strings.NewReader(`{"expectedRevision":1,"reason":"stop"}`)))
		if !authorized {
			if response.Code != http.StatusNotFound || owner.calls != 0 {
				t.Fatalf("unauthorized control: %d %+v", response.Code, owner)
			}
			continue
		}
		var snapshot RunSnapshotResponse
		if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil || response.Code != http.StatusOK ||
			snapshot.Run.Status != kernel.RunStatusCancelled || snapshot.EventHead == 0 || owner.calls != 1 || owner.revision != 1 {
			t.Fatalf("owner control: %s %+v %v", response.Body, owner, err)
		}
	}
	// HTTP does not mint its own terminal feed sequence outside committed facts.
	subscription, err := feed.Subscribe(t.Context(), "cancel-owner", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if len(subscription.Replay) != 0 {
		t.Fatalf("fabricated cancellation event: %+v", subscription.Replay)
	}
}

func TestRemoteCancelNeverFallsBackToKernel(t *testing.T) {
	_, runtime, _, actor := newRunFeedHTTPTest(t)
	created, err := runtime.Create(t.Context(), kernel.CreateRequest{ID: "remote", Kind: handoff.RemoteAgentRunKind,
		Actor: actor, Thread: kernel.ThreadRef{Kind: "test", ID: "thread"}, Goal: "remote", State: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	NewModule(NewHandler(Dependencies{Runtime: runtime, Shared: NewShared(runFeedPrincipal{actor: actor}, nil, runtime, nil)})).RegisterRoutes(router.Group("/v1"))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/runs/remote/cancel", strings.NewReader(`{"expectedRevision":1}`)))
	current, err := runtime.Load(t.Context(), created.Run.ID)
	if response.Code != http.StatusServiceUnavailable || err != nil || current.Run.Revision != created.Run.Revision {
		t.Fatalf("remote was locally cancelled: %s %+v %v", response.Body, current.Run, err)
	}
}
