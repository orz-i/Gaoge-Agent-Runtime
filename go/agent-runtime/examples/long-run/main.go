// long-run measures real SDK execution with a fixture model; no Provider calls.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

type settings struct{ steps, payloadBytes, replayBytes int }

type measurements struct {
	Profile                   string `json:"profile"`
	GoVersion                 string `json:"goVersion"`
	Platform                  string `json:"platform"`
	Backend                   string `json:"backend"`
	FixtureOnly               bool   `json:"fixtureOnly"`
	Steps                     int    `json:"steps"`
	PayloadBytes              int    `json:"toolTextBytes"`
	ReplayBytes               int    `json:"replayTextBytes"`
	StateWrites               int    `json:"aggregateStateWrites"`
	SubmittedStateBytes       int64  `json:"aggregateStateBytesSubmitted"`
	MaxStateBytes             int    `json:"maxAggregateStateBytes"`
	FinalStateBytes           int    `json:"finalAggregateStateBytes"`
	SampledHeapAllocBytes     uint64 `json:"sampledProcessHeapAllocBytes"`
	HeapSampleEveryMutations  int    `json:"heapSampleEveryMutations"`
	ElapsedMs                 int64  `json:"elapsedMs"`
	LoadAfterFaultMicros      int64  `json:"loadAfterFaultMicros"`
	ResumeMicros              int64  `json:"resumeRemainingWorkMicros"`
	FirstModelAfterResume     bool   `json:"firstModelAfterResumeObserved"`
	FirstModelAfterResumeUS   int64  `json:"firstModelAfterResumeMicros"`
	ModelCalls                int    `json:"modelCalls"`
	ToolExecutions            int    `json:"toolExecutions"`
	ConsumedRequestRetained   int    `json:"consumedRequestsRetained"`
	Recovery                  string `json:"recovery"`
	CommittedCallsNotReplayed bool   `json:"committedCallsNotReplayed"`
	DatabaseWALMeasured       bool   `json:"databaseWALMeasured"`
}

var errCommittedReceiptACKLost = errors.New("fixture committed Tool receipt ACK lost")

type measuredStore struct {
	*memory.Store
	mu         sync.Mutex
	metrics    measurements
	completed  int
	faultAfter int
	faulted    bool
}

func (store *measuredStore) observe(snapshot kernel.Snapshot) {
	store.metrics.StateWrites++
	store.metrics.SubmittedStateBytes += int64(len(snapshot.State))
	store.metrics.MaxStateBytes = max(store.metrics.MaxStateBytes, len(snapshot.State))
	if store.metrics.StateWrites%64 == 1 {
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		store.metrics.SampledHeapAllocBytes = max(store.metrics.SampledHeapAllocBytes, stats.Alloc)
	}
}

func (store *measuredStore) Create(ctx context.Context, record kernel.Record, events []kernel.EventDraft) (kernel.Snapshot, error) {
	result, err := store.Store.Create(ctx, record, events)
	if err == nil {
		store.mu.Lock()
		store.observe(result)
		store.mu.Unlock()
	}
	return result, err
}

// The native Runner uses this fenced port, not the unfenced Store.Apply port.
func (store *measuredStore) ApplyExecution(ctx context.Context, lease kernel.ExecutionLease, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	result, err := store.Store.ApplyExecution(ctx, lease, revision, mutation)
	if err != nil {
		return result, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.observe(result)
	for _, event := range mutation.Events {
		if event.Type == "tool.completed" {
			store.completed++
			if store.completed == store.faultAfter && !store.faulted {
				store.faulted = true
				return kernel.Snapshot{}, errCommittedReceiptACKLost
			}
		}
	}
	return result, nil
}

func main() {
	config := settings{}
	flag.IntVar(&config.steps, "steps", 50, "fixture reads, 2..500")
	flag.IntVar(&config.payloadBytes, "payload-bytes", 256, "text bytes per read, 0..65536")
	flag.IntVar(&config.replayBytes, "replay-bytes", 0, "synthetic opaque text bytes per response, 0..65536")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := measure(ctx, config)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fixture measurement failed:", err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		os.Exit(1)
	}
}

func measure(ctx context.Context, config settings) (measurements, error) {
	if config.steps < 2 || config.steps > 500 || config.payloadBytes < 0 || config.payloadBytes > 65536 || config.replayBytes < 0 || config.replayBytes > 65536 {
		return measurements{}, errors.New("invalid bounded fixture configuration")
	}
	if int64(config.steps)*int64(config.payloadBytes+3*config.replayBytes+1024) > 16<<20 {
		return measurements{}, errors.New("fixture payload estimate exceeds its 16 MiB safety cap")
	}
	store := &measuredStore{Store: memory.NewStore(), faultAfter: config.steps / 2,
		metrics: measurements{Profile: "unbounded-history-fixture-v1", GoVersion: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Backend: "memory", FixtureOnly: true, Steps: config.steps,
			PayloadBytes: config.payloadBytes, ReplayBytes: config.replayBytes, HeapSampleEveryMutations: 64,
			Recovery: "runner_reconstruction_same_memory_store"}}
	var mu sync.Mutex
	executions := make(map[int]int)
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{Key: "read", Name: "read", InputSchema: json.RawMessage(`{"type":"object","properties":{"step":{"type":"integer","minimum":1}},"required":["step"],"additionalProperties":false}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			var input struct{ Step int }
			if err := json.Unmarshal(request.Call.Arguments, &input); err != nil {
				return tools.ExecutionResult{}, err
			}
			mu.Lock()
			executions[input.Step]++
			mu.Unlock()
			content, _ := json.Marshal(map[string]any{"step": input.Step, "text": strings.Repeat("x", config.payloadBytes)})
			return tools.ExecutionResult{Content: content, Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
		}),
	}})
	if err != nil {
		return measurements{}, err
	}
	compose := func(probe *resumeProbe) (*kernel.Runtime, *agent.Runner, error) {
		rt, err := kernel.New(kernel.Dependencies{Store: store})
		if err != nil {
			return nil, nil, err
		}
		loop, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: fixtureModel{config: config, probe: probe}, Catalog: registry, Executor: registry,
			Limits: agent.Limits{MaxLLMCalls: config.steps + 1, MaxToolCalls: config.steps}})
		return rt, loop, err
	}
	_, loop, err := compose(nil)
	if err != nil {
		return measurements{}, err
	}
	started := time.Now()
	_, err = loop.StartRun(ctx, agent.StartRequest{ID: "measured-run", RequestID: "measured-request", Model: "fixture",
		Actor: kernel.ActorRef{TenantID: "fixture", ActorID: "operator"}, Thread: kernel.ThreadRef{Kind: "benchmark", ID: "long-run"},
		Goal: "Complete the bounded fixture read sequence", ToolKeys: []string{"read"}})
	if !errors.Is(err, errCommittedReceiptACKLost) || !store.faulted {
		return measurements{}, errors.New("the intended committed-receipt fault was not observed")
	}
	probe := &resumeProbe{}
	rt, loop, err := compose(probe)
	if err != nil {
		return measurements{}, err
	}
	loadStart := time.Now()
	current, err := rt.Load(ctx, "measured-run")
	loadMicros := time.Since(loadStart).Microseconds()
	if err != nil {
		return measurements{}, err
	}
	resumeStart := time.Now()
	completed, err := loop.Resume(ctx, current.Run.ID, current.Run.Revision)
	resumeMicros := time.Since(resumeStart).Microseconds()
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		return measurements{}, fmt.Errorf("fixture resume did not complete: %w", err)
	}
	view, err := agent.ViewState(completed)
	if err != nil {
		return measurements{}, err
	}
	store.mu.Lock()
	result := store.metrics
	store.mu.Unlock()
	result.ElapsedMs, result.LoadAfterFaultMicros, result.ResumeMicros = time.Since(started).Milliseconds(), loadMicros, resumeMicros
	result.FirstModelAfterResume = !probe.first.IsZero()
	if result.FirstModelAfterResume {
		result.FirstModelAfterResumeUS = probe.first.Sub(resumeStart).Microseconds()
	}
	result.FinalStateBytes = len(completed.State)
	result.ModelCalls = view.Budget.Usage.LLMCalls
	result.CommittedCallsNotReplayed = len(executions) == config.steps
	for step, count := range executions {
		result.ToolExecutions += count
		result.CommittedCallsNotReplayed = result.CommittedCallsNotReplayed && step >= 1 && step <= config.steps && count == 1
	}
	for _, invocation := range view.ModelInvocations {
		if invocation.Status == agent.ModelInvocationConsumed && len(invocation.Request.Messages) != 0 {
			result.ConsumedRequestRetained++
		}
	}
	if !result.CommittedCallsNotReplayed || result.ModelCalls != config.steps+1 || result.ConsumedRequestRetained != 0 {
		return result, errors.New("fixture recovery or request-retention acceptance failed")
	}
	return result, nil
}

type resumeProbe struct {
	once  sync.Once
	first time.Time
}

type fixtureModel struct {
	config settings
	probe  *resumeProbe
}

func (client fixtureModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if client.probe != nil {
		client.probe.once.Do(func() { client.probe.first = time.Now() })
	}
	completed := 0
	for _, message := range request.Messages {
		if message.Role == model.RoleTool {
			completed++
		}
	}
	response := model.Response{Content: "fixture complete"}
	if completed < client.config.steps {
		args, _ := json.Marshal(map[string]int{"step": completed + 1})
		response.Content = ""
		response.ToolCalls = []tools.Call{{ID: fmt.Sprintf("read-%d", completed+1), ToolKey: "read", Arguments: args}}
	}
	if client.config.replayBytes > 0 {
		data, _ := json.Marshal(strings.Repeat("r", client.config.replayBytes))
		response.ProviderReplay = &model.ProviderReplay{Provider: "fixture", Format: "fixture.v1", Data: data}
	}
	return response, nil
}
