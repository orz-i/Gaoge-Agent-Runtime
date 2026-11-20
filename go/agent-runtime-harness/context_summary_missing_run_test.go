package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type summaryAdmissionContextFailure struct{ harness.Store }

func (store summaryAdmissionContextFailure) CommitContextCheckpoint(context.Context, harness.ContextCheckpointCommit) (harness.Turn, error) {
	return harness.Turn{}, errors.New("fixture Context admission failure")
}

func TestAgentSessionSummaryPreservesMissingRunResumeContracts(t *testing.T) {
	for _, mode := range []string{"accepted", "cancelled", "failed"} {
		for _, enabled := range []bool{false, true} {
			name := mode + "/ordinary"
			if enabled {
				name = mode + "/summary"
			}
			t.Run(name, func(t *testing.T) {
				backing, runs, client := harness.NewMemoryStore(), memory.NewStore(), &agentSessionModel{}
				var store harness.Store = backing
				if mode != "failed" {
					store = summaryAdmissionContextFailure{Store: backing}
				}
				options := agentSessionOptions(t, runs, store, client)
				if enabled {
					options.Session.Config.WorkingStateSummary = summaryTestPolicy()
				}
				if mode == "failed" {
					// This client has no structured-output capability. Native
					// preflight fails with proven no effect before Run creation.
					options.Session.Config.OutputContract = &model.OutputContract{
						Name: "business", Schema: json.RawMessage(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`),
					}
				}
				session, err := harness.OpenAgentSession(options)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = session.Prompt(t.Context(), "missing", "original task"); err == nil {
					t.Fatal("expected pre-Run admission failure")
				}
				before, err := session.LoadState(t.Context(), "missing")
				if err != nil {
					t.Fatal(err)
				}
				wantStatus := harness.TurnAccepted
				if mode == "failed" {
					wantStatus = harness.TurnFailed
				}
				if mode == "cancelled" {
					plain, err := agent.NewRunner(agent.Dependencies{Runtime: options.Runtime, Model: client})
					if err != nil {
						t.Fatal(err)
					}
					cancelHost, err := harness.NewRunner(harness.Dependencies{Runtime: options.Runtime, Store: backing, Agent: plain})
					if err != nil {
						t.Fatal(err)
					}
					if _, err = cancelHost.Cancel(t.Context(), before.Turn.ID, "stop before dispatch"); err != nil {
						t.Fatal(err)
					}
					wantStatus = harness.TurnCancelled
				}
				result, resumeErr := session.Resume(t.Context(), "missing")
				if mode == "accepted" {
					if !errors.Is(resumeErr, harness.ErrNotFound) || !errors.Is(resumeErr, kernel.ErrNotFound) {
						t.Fatalf("missing original Run lost the public ErrNotFound contract: %v", resumeErr)
					}
				} else if resumeErr != nil {
					t.Fatalf("terminal admission without a Run became unreadable: %v", resumeErr)
				}
				if result.Turn.ID != before.Turn.ID || result.Turn.Status != wantStatus || len(client.requests) != 0 {
					t.Fatalf("missing-Run Resume changed admission or dispatched effects: Turn=%+v calls=%d", result.Turn, len(client.requests))
				}
				root, ok := harness.TopLevelInvocation(result)
				if !ok {
					t.Fatal("missing original root")
				}
				if _, err := options.Runtime.Load(t.Context(), root.ExecutionRefID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("Resume recreated the absent original Run: %v", err)
				}
			})
		}
	}
}
