package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const discoveryChosenToolKey = "mcp.synthetic_02"

type testToolDiscovery struct {
	returned       []string
	calls          int
	candidateCount int
	onSearch       func()
}

func (search *testToolDiscovery) Search(_ context.Context, request agent.ToolDiscoveryRequest) ([]string, error) {
	search.calls++
	search.candidateCount = len(request.Candidates)
	if search.onSearch != nil {
		search.onSearch()
	}
	if request.RunID == "" || request.Query != "look up documents" || request.MaxResults != 5 {
		return nil, agent.ErrToolDiscoveryInvalid
	}
	// Returned Key list is never an authorization source.
	return append([]string(nil), search.returned...), nil
}

type searchThenUseModel struct {
	t                      *testing.T
	calls                  int
	initialDefinitionBytes int
	loadedDefinitionBytes  int
}

func (stub *searchThenUseModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	stub.calls++
	switch stub.calls {
	case 1:
		if len(request.Tools) != 1 || request.Tools[0].Key != agent.ToolDiscoveryKey {
			stub.t.Fatalf("Tool Search did not defer local schemas: %+v", request.Tools)
		}
		bytes, _ := json.Marshal(request.Tools)
		stub.initialDefinitionBytes = len(bytes)
		return model.Response{ToolCalls: []tools.Call{{
			ID: "search-call-1", ToolKey: agent.ToolDiscoveryKey,
			Arguments: json.RawMessage(`{"query":"look up documents"}`),
		}}}, nil
	case 2:
		if len(request.Tools) != 2 ||
			!slices.ContainsFunc(request.Tools, func(item tools.Definition) bool { return item.Key == discoveryChosenToolKey }) ||
			!slices.ContainsFunc(request.Tools, func(item tools.Definition) bool { return item.Key == agent.ToolDiscoveryKey }) {
			stub.t.Fatalf("searched Tool missing from next model request: %+v", request.Tools)
		}
		last := request.Messages[len(request.Messages)-1]
		if last.Role != model.RoleTool || last.ToolCallID != "search-call-1" ||
			last.Content != `{"toolKeys":["mcp.synthetic_02"]}` {
			stub.t.Fatalf("search receipt/transcript not durable: %+v", last)
		}
		bytes, _ := json.Marshal(request.Tools)
		stub.loadedDefinitionBytes = len(bytes)
		return model.Response{ToolCalls: []tools.Call{{
			ID: "business-call-1", ToolKey: discoveryChosenToolKey, Arguments: json.RawMessage(`{}`),
		}}}, nil
	case 3:
		if len(request.Tools) != 2 || request.Messages[len(request.Messages)-1].Content != `{"ok":true}` {
			stub.t.Fatalf("Tool output/replay failed: %+v", request)
		}
		return model.Response{Content: "answer based on retrieved documents"}, nil
	default:
		stub.t.Fatalf("unbounded model loop: %d", stub.calls)
		return model.Response{}, nil
	}
}

type rejectUnloadedModel struct{}

func (rejectUnloadedModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{ToolCalls: []tools.Call{{
		ID: "illegal-tool", ToolKey: discoveryChosenToolKey, Arguments: json.RawMessage(`{}`),
	}}}, nil
}

type searchOnlyModel struct{}

func (searchOnlyModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{ToolCalls: []tools.Call{{
		ID: "malicious-search", ToolKey: agent.ToolDiscoveryKey,
		Arguments: json.RawMessage(`{"query":"look up documents"}`),
	}}}, nil
}

func discoveryTestRegistry(t *testing.T, count int, toolExecutions *int) (*tools.Registry, []string) {
	t.Helper()
	registrations := make([]tools.Registration, 0, count)
	keys := make([]string, 0, count)
	for index := range count {
		key := fmt.Sprintf("mcp.synthetic_%02d", index)
		keys = append(keys, key)
		registrations = append(registrations, tools.Registration{
			Definition: tools.Definition{
				Key: key, Name: fmt.Sprintf("synthetic_search_%02d", index),
				Description: "A synthetic local MCP tool for benchmark and authorization conformance.",
				InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			},
			Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				*toolExecutions++
				return tools.ExecutionResult{
					Content: json.RawMessage(`{"ok":true}`),
					Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"},
				}, nil
			}),
		})
	}
	registry, err := tools.NewRegistry(registrations)
	if err != nil {
		t.Fatal(err)
	}
	return registry, keys
}

func TestRuntimeToolSearchFindsOnlyAuthorizedSchemasAndPersistsResults(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	executions := 0
	registry, keys := discoveryTestRegistry(t, 32, &executions)
	search := &testToolDiscovery{returned: []string{discoveryChosenToolKey}}
	mock := &searchThenUseModel{t: t}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: mock, Catalog: registry, Executor: registry, ToolDiscovery: search,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("run_search_32", "req_search_32", "Find documents", keys...)
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("discovery loop failed: %+v err=%v", snapshot.Run, err)
	}
	if search.calls != 1 || search.candidateCount != 32 || mock.calls != 3 || executions != 1 {
		t.Fatalf("search=%+v model=%d businessExecutions=%d", search, mock.calls, executions)
	}
	if mock.initialDefinitionBytes == 0 || mock.loadedDefinitionBytes == 0 {
		t.Fatalf("missing tool schema bytes: first=%d loaded=%d", mock.initialDefinitionBytes, mock.loadedDefinitionBytes)
	}
	var state map[string]json.RawMessage
	if err = json.Unmarshal(snapshot.State, &state); err != nil {
		t.Fatal(err)
	}
	var durable struct {
		LoadedKeys []string `json:"loadedKeys"`
		Receipts   []struct {
			CallID       string   `json:"callID"`
			SnapshotHash string   `json:"snapshotHash"`
			LoadedKeys   []string `json:"loadedKeys"`
		} `json:"receipts"`
	}
	if err = json.Unmarshal(state["discovery"], &durable); err != nil {
		t.Fatal(err)
	}
	if len(durable.LoadedKeys) != 1 || durable.LoadedKeys[0] != discoveryChosenToolKey ||
		len(durable.Receipts) != 1 || durable.Receipts[0].CallID != "search-call-1" ||
		durable.Receipts[0].SnapshotHash == "" {
		t.Fatalf("discovery state/receipt not persisted: %+v", durable)
	}
	if strings.Contains(string(state["discovery"]), "searchQuery") ||
		strings.Contains(string(state["discovery"]), "look up documents") {
		t.Fatal("search query leaked into discovery receipt")
	}
}

func TestRuntimeToolSearchCannotExecuteAuthorizedButUnloadedTool(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	executions := 0
	registry, keys := discoveryTestRegistry(t, 16, &executions)
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: rejectUnloadedModel{}, Catalog: registry, Executor: registry,
		ToolDiscovery: &testToolDiscovery{returned: []string{discoveryChosenToolKey}},
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), startRequest("run_unloaded", "req_unloaded", "Do not execute", keys...))
	if err == nil || snapshot.Run.Status != kernel.RunStatusFailed || executions != 0 {
		t.Fatalf("unloaded tool executed: %+v err=%v executions=%d", snapshot.Run, err, executions)
	}
}

func TestRuntimeToolSearchRejectsUnauthorizedAndDuplicateSearchResults(t *testing.T) {
	t.Parallel()
	for _, returned := range [][]string{{"mcp.not_authorized"}, {discoveryChosenToolKey, discoveryChosenToolKey}} {
		runtime, _ := newTestRuntimeAndApprovals(t)
		executions := 0
		registry, keys := discoveryTestRegistry(t, 16, &executions)
		runner, err := agent.NewRunner(agent.Dependencies{
			Runtime: runtime, Model: searchOnlyModel{}, Catalog: registry, Executor: registry,
			ToolDiscovery: &testToolDiscovery{returned: returned},
		})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := runner.StartRun(t.Context(), startRequest("run_invalid_search", "req_invalid_search", "Search", keys...))
		if err == nil || snapshot.Run.Status != kernel.RunStatusFailed || executions != 0 {
			t.Fatalf("untrusted search results accepted: %+v err=%v executions=%d", snapshot.Run, err, executions)
		}
	}
}

type simpleTextModel struct {
	t         *testing.T
	wantTools int
}

func (stub simpleTextModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if len(request.Tools) != stub.wantTools {
		stub.t.Fatalf("small eager request changed: %+v", request.Tools)
	}
	return model.Response{Content: "done"}, nil
}

func TestRuntimeToolSearchKeepsSmallToolSetsEager(t *testing.T) {
	t.Parallel()
	for _, count := range []int{0, 5} {
		runtime, _ := newTestRuntimeAndApprovals(t)
		executions := 0
		registry, keys := discoveryTestRegistry(t, count, &executions)
		search := &testToolDiscovery{returned: []string{discoveryChosenToolKey}}
		runner, err := agent.NewRunner(agent.Dependencies{
			Runtime: runtime, Model: simpleTextModel{t: t, wantTools: count},
			Catalog: registry, Executor: registry, ToolDiscovery: search,
		})
		if err != nil {
			t.Fatal(err)
		}
		snapshot, err := runner.StartRun(t.Context(), startRequest(fmt.Sprintf("run_small_%d", count), "req", "Small task", keys...))
		if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted || search.calls != 0 {
			t.Fatalf("small tool set no longer eager: %+v err=%v", snapshot.Run, err)
		}
	}
}

// Changing the implementation behind the same canonical Key after a discovery
// receipt must block the next model request, not reuse an obsolete schema.
type changingDiscoveryCatalog struct {
	*tools.Registry
	changed bool
}

func (catalog *changingDiscoveryCatalog) Resolve(key string) (tools.Definition, bool) {
	definition, ok := catalog.Registry.Resolve(key)
	if ok && catalog.changed && key == discoveryChosenToolKey {
		definition.Description = "revoked or updated provider declaration"
	}
	return definition, ok
}
func TestRuntimeToolSearchDefinitionDriftFailsClosed(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	executions := 0
	registry, keys := discoveryTestRegistry(t, 16, &executions)
	catalog := &changingDiscoveryCatalog{Registry: registry}
	search := &testToolDiscovery{
		returned: []string{discoveryChosenToolKey},
		onSearch: func() { catalog.changed = true },
	}
	modelClient := searchOnlyModel{}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: modelClient, Catalog: catalog, Executor: registry, ToolDiscovery: search,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := runner.StartRun(t.Context(), startRequest("run_revoke_after_search", "req_revoke", "Search", keys...))
	if err == nil || snapshot.Run.Status != kernel.RunStatusFailed || snapshot.Run.ErrorCode != "agent.discovery_invalid" || executions != 0 {
		t.Fatalf("stale tool definitions not blocked: run=%+v err=%v exec=%d", snapshot.Run, err, executions)
	}
	if search.calls != 1 {
		t.Fatalf("unexpected re-search after revoke: %d", search.calls)
	}
}

type retryAfterSearchModel struct {
	t         *testing.T
	calls     int
	retriedID string
}

func (stub *retryAfterSearchModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	stub.calls++
	switch stub.calls {
	case 1:
		if len(request.Tools) != 1 || request.Tools[0].Key != agent.ToolDiscoveryKey {
			stub.t.Fatal("did not start with bounded search")
		}
		return model.Response{ToolCalls: []tools.Call{{
			ID: "search-for-retry", ToolKey: agent.ToolDiscoveryKey,
			Arguments: json.RawMessage(`{"query":"look up documents"}`),
		}}}, nil
	case 2:
		stub.retriedID = request.InvocationID
		return model.Response{}, model.NewRetryableError(errRetryableProvider)
	case 3:
		if stub.retriedID == "" || request.InvocationID != stub.retriedID ||
			len(request.Tools) != 2 ||
			!slices.ContainsFunc(request.Tools, func(t tools.Definition) bool { return t.Key == discoveryChosenToolKey }) {
			stub.t.Fatalf("resume changed invocation or lost loaded Tool: %s %+v", request.InvocationID, request.Tools)
		}
		return model.Response{Content: "completed after retry"}, nil
	default:
		stub.t.Fatal("unexpected extra model invocation")
		return model.Response{}, nil
	}
}

func TestRuntimeToolSearchPersistsReceiptAcrossRetryResume(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	executions := 0
	registry, keys := discoveryTestRegistry(t, 16, &executions)
	search := &testToolDiscovery{returned: []string{discoveryChosenToolKey}}
	provider := &retryAfterSearchModel{t: t}
	deps := agent.Dependencies{
		Runtime: runtime, Model: provider, Catalog: registry, Executor: registry, ToolDiscovery: search,
	}
	runner, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := runner.StartRun(t.Context(), startRequest("run_search_retry", "req_search_retry", "Find documents", keys...))
	if err == nil || pending.Run.Status != kernel.RunStatusRunning || search.calls != 1 {
		t.Fatalf("retryable failure not durable: status=%s err=%v search=%d", pending.Run.Status, err, search.calls)
	}
	var payload struct {
		Discovery struct {
			LoadedKeys []string `json:"loadedKeys"`
			Receipts   []struct {
				CallID string `json:"callID"`
			} `json:"receipts"`
		} `json:"discovery"`
	}
	if err = json.Unmarshal(pending.State, &payload); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(payload.Discovery.LoadedKeys, []string{discoveryChosenToolKey}) ||
		len(payload.Discovery.Receipts) != 1 || payload.Discovery.Receipts[0].CallID != "search-for-retry" {
		t.Fatalf("search receipt absent before provider retry: %+v", payload.Discovery)
	}
	// Reconstruction of the SDK Runner (same Kernel Store) must not rerun
	// search, must retain the frozen grant and exact pending Invocation ID.
	restarted, err := agent.NewRunner(deps)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || search.calls != 1 ||
		provider.calls != 3 || executions != 0 {
		t.Fatalf("resume reauthorized/replayed search: run=%+v err=%v calls=%d", completed.Run, err, search.calls)
	}
}

// The baseline compares SDK-local JSON declarations only, never upstream
// provider token usage or task-level quality. Both Runs have identical,
// already-authorized Tool Keys and the same deterministic fixture model.
type eagerDeclarationCapture struct{ bytes int }

func (capture *eagerDeclarationCapture) Generate(_ context.Context, request model.Request) (model.Response, error) {
	body, err := json.Marshal(request.Tools)
	if err != nil {
		return model.Response{}, err
	}
	capture.bytes = len(body)
	return model.Response{Content: "done"}, nil
}
func TestRuntimeToolSearchShrinksInitialGatewayToolDeclarationProjection(t *testing.T) {
	t.Parallel()
	n := 128
	registry, _ := discoveryTestRegistry(t, n, new(int))
	keys := make([]string, n)
	for i := range n {
		keys[i] = fmt.Sprintf("mcp.synthetic_%02d", i)
	}
	base := &eagerDeclarationCapture{}
	runtime, _ := newTestRuntimeAndApprovals(t)
	eager, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: base, Catalog: registry, Executor: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = eager.StartRun(t.Context(), startRequest("run_eager_128", "eager_128", "find documents", keys...))
	if err != nil {
		t.Fatal(err)
	}
	searchModel := &searchThenUseModel{t: t}
	rt, _ := newTestRuntimeAndApprovals(t)
	search := &testToolDiscovery{returned: []string{discoveryChosenToolKey}}
	withSearch, err := agent.NewRunner(agent.Dependencies{
		Runtime: rt, Model: searchModel, Catalog: registry, Executor: registry,
		ToolDiscovery: search,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = withSearch.StartRun(t.Context(), startRequest("run_search_bytes_128", "search_bytes_128", "find documents", keys...))
	if err != nil {
		t.Fatal(err)
	}
	if base.bytes <= 0 || searchModel.initialDefinitionBytes <= 0 ||
		searchModel.initialDefinitionBytes >= base.bytes/2 {
		t.Fatalf("SDK tool JSON projection not reduced enough: eager=%d initial-search=%d", base.bytes, searchModel.initialDefinitionBytes)
	}
	t.Logf("synthetic Gateway Tool declaration bytes: eager=%d initial-search=%d (not Provider tokens)", base.bytes, searchModel.initialDefinitionBytes)
}
