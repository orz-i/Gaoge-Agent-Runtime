package agent_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestLexicalToolDiscoveryDeterministicAuthorizedOnly(t *testing.T) {
	t.Parallel()
	search := agent.LexicalToolDiscovery{}
	candidates := []agent.ToolDiscoveryCandidate{
		{Key: "mcp.server-a.search", Fingerprint: strings.Repeat("a", 64)},
		{Key: "mcp.server-b.search", Fingerprint: strings.Repeat("b", 64)},
		{Key: "mcp.server-c.publish", Fingerprint: strings.Repeat("c", 64)},
	}
	request := agent.ToolDiscoveryRequest{
		RunID: "run-test", Model: "test-model", Query: "search",
		Candidates: candidates, MaxResults: 5,
		Definitions: []agent.ToolDiscoveryDescriptor{
			{Key: "mcp.server-a.search", Name: "search", Description: "Search documents"},
			{Key: "mcp.server-b.search", Name: "search", Description: "Search documents"},
			{Key: "mcp.server-c.publish", Name: "publish", Description: "Write a report"},
		},
	}
	result, err := search.Search(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result, []string{"mcp.server-a.search", "mcp.server-b.search"}) {
		t.Fatalf("same-named MCP Tools lost stable identity: %#v", result)
	}
	request.MaxResults = 1
	result, err = search.Search(t.Context(), request)
	if err != nil || !slices.Equal(result, []string{"mcp.server-a.search"}) {
		t.Fatalf("bounded deterministic ordering failed: %#v %v", result, err)
	}
	request.MaxResults = 5
	request.Query = "publish"
	result, err = search.Search(t.Context(), request)
	if err != nil || !slices.Equal(result, []string{"mcp.server-c.publish"}) {
		t.Fatalf("query matching failed: %#v %v", result, err)
	}
}

func TestLexicalToolDiscoveryFailsClosedForUntrustedDefinitions(t *testing.T) {
	t.Parallel()
	valid := agent.ToolDiscoveryRequest{
		RunID: "run-test", Model: "m", Query: "search",
		Candidates:  []agent.ToolDiscoveryCandidate{{Key: "mcp.authorized.search", Fingerprint: "test"}},
		Definitions: []agent.ToolDiscoveryDescriptor{{Key: "mcp.authorized.search", Name: "search"}},
		MaxResults:  5,
	}
	search := agent.LexicalToolDiscovery{}
	for _, test := range []struct {
		name   string
		mutate func(*agent.ToolDiscoveryRequest)
	}{
		{"unlisted definition", func(r *agent.ToolDiscoveryRequest) {
			r.Definitions[0].Key = "mcp.unauthorized.search"
		}},
		{"empty candidate fingerprint", func(r *agent.ToolDiscoveryRequest) {
			r.Candidates[0].Fingerprint = ""
		}},
		{"duplicate definition", func(r *agent.ToolDiscoveryRequest) {
			r.Candidates = append(r.Candidates, agent.ToolDiscoveryCandidate{Key: "mcp.authorized.search", Fingerprint: "test"})
			r.Definitions = append(r.Definitions, r.Definitions[0])
		}},
		{"max results exceeds policy", func(r *agent.ToolDiscoveryRequest) { r.MaxResults = 6 }},
		{"unsearchable query", func(r *agent.ToolDiscoveryRequest) { r.Query = "---" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := valid
			input.Candidates = append([]agent.ToolDiscoveryCandidate(nil), valid.Candidates...)
			input.Definitions = append([]agent.ToolDiscoveryDescriptor(nil), valid.Definitions...)
			test.mutate(&input)
			if _, err := search.Search(t.Context(), input); err == nil {
				t.Fatal("untrusted candidate search was accepted")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := search.Search(ctx, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled search did not stop: %v", err)
	}
}

func TestRuntimeToolSearchAt128CandidateBoundary(t *testing.T) {
	t.Parallel()
	runtime, _ := newTestRuntimeAndApprovals(t)
	executions := 0
	registry, keys := discoveryTestRegistry(t, 128, &executions)
	search := &testToolDiscovery{returned: []string{discoveryChosenToolKey}}
	model := &searchThenUseModel{t: t}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: model, Catalog: registry, Executor: registry, ToolDiscovery: search,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := startRequest("run_128_tools", "request_128_tools", "Find documents", keys...)
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil || snapshot.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("128 tool run failed: %v (%s)", err, snapshot.Run.Status)
	}
	if search.candidateCount != 128 || executions != 1 || model.calls != 3 {
		t.Fatalf("128-tool search failed: searchCount=%d toolExecutions=%d modelCalls=%d", search.candidateCount, executions, model.calls)
	}
}

func BenchmarkLexicalToolDiscovery(b *testing.B) {
	for _, count := range []int{0, 5, 16, 32, 128} {
		b.Run(fmt.Sprintf("tools_%d", count), func(b *testing.B) {
			port := agent.LexicalToolDiscovery{}
			input := agent.ToolDiscoveryRequest{RunID: "bench", Model: "fixture", Query: "search documents", MaxResults: 5}
			for i := range count {
				key := fmt.Sprintf("mcp.synth_%03d", i)
				input.Candidates = append(input.Candidates, agent.ToolDiscoveryCandidate{Key: key, Fingerprint: "fixture-hash"})
				input.Definitions = append(input.Definitions, agent.ToolDiscoveryDescriptor{
					Key: key, Name: fmt.Sprintf("search_docs_%03d", i), Description: "Search and read synthetic documents.",
				})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Zero candidates correctly rejects a search; it measures the
				// no-candidate guard rather than an empty index lookup.
				_, err := port.Search(context.Background(), input)
				if count > 0 && err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
