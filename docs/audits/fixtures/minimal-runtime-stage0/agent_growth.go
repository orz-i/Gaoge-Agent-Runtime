package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"log"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"
)

type stepModel struct{ steps, calls int }

func (m *stepModel) Generate(ctx context.Context, req model.Request) (model.Response, error) {
	if err := ctx.Err(); err != nil {
		return model.Response{}, err
	}
	m.calls++
	if m.calls <= m.steps {
		return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("step-%d", m.calls), ToolKey: "observe", Arguments: json.RawMessage(fmt.Sprintf(`{"index":%d}`, m.calls))}}, Usage: &model.Usage{InputTokens: 8, OutputTokens: 4}}, nil
	}
	return model.Response{Content: "complete", Usage: &model.Usage{InputTokens: 8, OutputTokens: 4}}, nil
}
func main() {
	if len(os.Args) < 3 {
		log.Fatal("usage: bench <steps> <path>")
	}
	n, err := strconv.Atoi(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	out := os.Args[2]
	if profilePath := os.Getenv("GAOGE_STAGE0_CPU_PROFILE"); profilePath != "" {
		file, createErr := os.Create(profilePath)
		if createErr != nil {
			log.Fatal(createErr)
		}
		if startErr := pprof.StartCPUProfile(file); startErr != nil {
			_ = file.Close()
			log.Fatal(startErr)
		}
		defer func() { pprof.StopCPUProfile(); _ = file.Close() }()
	}
	reg, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "observe", Name: "Observe", InputSchema: json.RawMessage(`{"type":"object","properties":{"index":{"type":"integer"}},"required":["index"]}`)}, Handler: tools.HandlerFunc(func(ctx context.Context, r tools.ExecutionRequest) (tools.ExecutionResult, error) {
		return tools.ExecutionResult{Content: json.RawMessage(fmt.Sprintf(`{"step":%s,"text":%q}`, strings.TrimSuffix(strings.TrimPrefix(string(r.Call.Arguments), `{"index":`), "}"), strings.Repeat("x", 64))), Receipt: tools.Receipt{ExecutionID: r.Call.ID, Disposition: "committed"}}, nil
	})}})
	if err != nil {
		log.Fatal("registry", err)
	}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		log.Fatal(err)
	}
	m := &stepModel{steps: n}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: m, Catalog: reg, Executor: reg, Limits: agent.Limits{MaxLLMCalls: n + 4, MaxToolCalls: n + 4}})
	if err != nil {
		log.Fatal(err)
	}
	start := time.Now()
	snap, err := runner.StartRun(context.Background(), agent.StartRequest{ID: fmt.Sprintf("run-%d", n), Actor: kernel.ActorRef{TenantID: "local", ActorID: "profile-1"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"}, Goal: "inspect varying inputs", ToolKeys: []string{"observe"}})
	elapsed := time.Since(start)
	if err != nil {
		log.Fatalf("steps=%d err=%v status=%s calls=%d revision=%d", n, err, snap.Run.Status, m.calls, snap.Run.Revision)
	}
	view, err := agent.ViewState(snap)
	if err != nil {
		log.Fatal(err)
	}
	if snap.Run.Status != kernel.RunStatusCompleted {
		log.Fatalf("steps=%d status=%s calls=%d revision=%d", n, snap.Run.Status, m.calls, snap.Run.Revision)
	}
	if err := os.WriteFile(out, snap.State, 0600); err != nil {
		log.Fatal(err)
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	fmt.Printf("steps=%d wall_ms=%d model_calls=%d revision=%d messages=%d invocations=%d state_bytes=%d heap_alloc_bytes=%d total_alloc_mb=%.1f event_head=%d\n", n, elapsed.Milliseconds(), m.calls, snap.Run.Revision, len(view.Messages), len(view.ModelInvocations), len(snap.State), mem.HeapAlloc, float64(mem.TotalAlloc)/1048576, snap.EventHead)
	if heapPath := os.Getenv("GAOGE_STAGE0_HEAP_PROFILE"); heapPath != "" {
		file, createErr := os.Create(heapPath)
		if createErr != nil {
			log.Fatal(createErr)
		}
		if writeErr := pprof.WriteHeapProfile(file); writeErr != nil {
			_ = file.Close()
			log.Fatal(writeErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			log.Fatal(closeErr)
		}
	}
}
