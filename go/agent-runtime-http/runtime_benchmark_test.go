package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

func BenchmarkHTTPGetRunMemory(b *testing.B) {
	engine, _ := benchmarkHTTPEngine(b, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/runs/benchmark-run", nil)
		engine.ServeHTTP(response, request)
		if response.Code != stdhttp.StatusOK {
			b.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func BenchmarkHTTPListRunEventsMemory(b *testing.B) {
	engine, _ := benchmarkHTTPEngine(b, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		response := httptest.NewRecorder()
		request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/runs/benchmark-run/events?afterSeq=50&limit=25", nil)
		engine.ServeHTTP(response, request)
		if response.Code != stdhttp.StatusOK {
			b.Fatalf("status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func benchmarkHTTPEngine(b *testing.B, eventCount int) (*gin.Engine, *kernel.Runtime) {
	b.Helper()
	gin.SetMode(gin.TestMode)
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "actor"}
	runtime, err := kernel.New(kernel.Dependencies{
		Store: memory.NewStore(), Clock: httpBenchmarkClock{}, IDs: &httpBenchmarkIDs{},
	})
	if err != nil {
		b.Fatal(err)
	}
	snapshot, err := runtime.Create(context.Background(), kernel.CreateRequest{
		ID: "benchmark-run", Kind: kernel.RunKind("benchmark"), Actor: actor,
		Thread: kernel.ThreadRef{Kind: "benchmark", ID: "thread"},
		Goal:   "benchmark HTTP", State: json.RawMessage(`{"step":0}`),
	})
	if err != nil {
		b.Fatal(err)
	}
	for index := range eventCount {
		snapshot, err = runtime.Apply(context.Background(), snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning,
			State:  json.RawMessage(`{"step":1}`),
			Events: []kernel.EventDraft{{Type: "benchmark.event", Message: strconv.Itoa(index)}},
		})
		if err != nil {
			b.Fatal(err)
		}
	}
	query, err := workbench.NewQuery(runtime, runtime, nil)
	if err != nil {
		b.Fatal(err)
	}
	engine := gin.New()
	NewModule(NewHandler(Dependencies{
		Runtime: runtime, Workbench: query,
		Shared: NewShared(httpBenchmarkPrincipal{actor: actor}, nil, runtime, nil),
	})).RegisterRoutes(engine.Group("/api/v1"))
	return engine, runtime
}

type httpBenchmarkPrincipal struct {
	actor kernel.ActorRef
}

func (principal httpBenchmarkPrincipal) ResolvePrincipal(*gin.Context) (kernel.ActorRef, error) {
	return principal.actor, nil
}

type httpBenchmarkClock struct{}

func (httpBenchmarkClock) Now() time.Time {
	return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
}

type httpBenchmarkIDs struct{ next atomic.Uint64 }

func (ids *httpBenchmarkIDs) NewID(prefix string) (string, error) {
	return prefix + "-" + strconv.FormatUint(ids.next.Add(1), 10), nil
}
