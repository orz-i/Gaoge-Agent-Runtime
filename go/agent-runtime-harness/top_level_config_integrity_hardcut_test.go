package harness_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type configMutationCaptureModel struct{ requests []model.Request }

func (m *configMutationCaptureModel) Generate(_ context.Context, r model.Request) (model.Response, error) {
	m.requests = append(m.requests, model.CloneRequest(r))
	return model.Response{Content: "ok"}, nil
}

type configMutationStore struct {
	harness.Store
	mutate            func(*harness.ConfigSnapshot)
	invocationCreates int
}

func (s *configMutationStore) PutConfigSnapshot(ctx context.Context, v harness.ConfigSnapshot) (harness.ConfigSnapshot, bool, error) {
	s.mutate(&v)
	return s.Store.PutConfigSnapshot(ctx, v)
}
func (s *configMutationStore) CreateInvocation(ctx context.Context, v harness.Invocation) (harness.Invocation, bool, error) {
	s.invocationCreates++
	return s.Store.CreateInvocation(ctx, v)
}
func TestHardcutConfigSnapshotRejectsUnsealedMutations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*harness.ConfigSnapshot)
	}{
		{"model", func(c *harness.ConfigSnapshot) { c.Model = "provider-unapproved" }},
		{"admission-hash", func(c *harness.ConfigSnapshot) { c.AdmissionHash = strings.Repeat("a", 64) }},
		{"tools", func(c *harness.ConfigSnapshot) { c.ToolKeys = []string{"internal.admin"} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			modelClient := &configMutationCaptureModel{}
			loop, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: modelClient})
			if err != nil {
				t.Fatal(err)
			}
			poisoned := &configMutationStore{Store: harness.NewMemoryStore(), mutate: tc.mutate}
			runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loop, Store: poisoned})
			if err != nil {
				t.Fatal(err)
			}
			req := testStartRequest()
			req.Config.Model = "model-authorized"
			_, err = runner.StartAgentTurn(t.Context(), req)
			if !errors.Is(err, harness.ErrConflict) || poisoned.invocationCreates != 0 || len(modelClient.requests) != 0 {
				t.Fatalf("CONFIG_IMMUTABILITY: accepted %s with stale ID/ContentHash after altered frozen payload; err=%v invocationCreates=%d modelCalls=%d", tc.name, err, poisoned.invocationCreates, len(modelClient.requests))
			}
		})
	}
}
