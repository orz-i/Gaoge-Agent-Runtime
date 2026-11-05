package harness_test

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/team"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
)

type forgedInvocationReceiptStore struct {
	harness.Store
	mutation string
	creates  int
}

// Return a deliberately dishonest CreateInvocation receipt, while retaining
// the original immutable row. This is the same trust-boundary variant as the
// independent audit: an inconsistent Store must not grant physical effects.
func (s *forgedInvocationReceiptStore) CreateInvocation(ctx context.Context, value harness.Invocation) (harness.Invocation, bool, error) {
	s.creates++
	original, fresh, err := s.Store.CreateInvocation(ctx, value)
	if err != nil {
		return original, fresh, err
	}
	switch s.mutation {
	case "turn-id":
		original.TurnID = "ht_unrelated"
	case "invocation-id":
		original.ID = "hiv_unrelated"
	case "run-id":
		original.ExecutionRefID = "hr_unrelated"
	case "class":
		original.ExecutionClass = harness.ExecutionWorkflow
	case "capability":
		original.CapabilityKey = "runtime.alternate"
	case "definition":
		original.DefinitionVersion = "different"
	case "input":
		original.Input = []byte(`{"goal":"different"}`)
	case "input-hash":
		original.InputHash = "untrusted"
	case "attempt":
		original.Attempt = 2
	case "parent":
		original.ParentItemID = "foreign-parent"
	case "fresh-status":
		if fresh {
			original.Status = harness.InvocationCompleted
		}
	case "fresh-revision":
		if fresh {
			original.Revision = 2
		}
	case "foreign-persisted":
		foreignTurn, getErr := s.GetTurn(ctx, value.TurnID)
		if getErr != nil {
			return harness.Invocation{}, false, getErr
		}
		foreignTurn.ID = "ht_other_invocation_owner"
		foreignTurn.HostTurn.ID = "another-turn"
		if _, _, getErr = s.CreateTurn(ctx, foreignTurn); getErr != nil {
			return harness.Invocation{}, false, getErr
		}
		foreign := value
		foreign.TurnID = foreignTurn.ID
		foreign.ID, getErr = harness.AgentInvocationID(foreign.TurnID, foreign.TurnID)
		if getErr != nil {
			return harness.Invocation{}, false, getErr
		}
		foreign.CapabilityKey = harness.CapabilityAgent
		foreign.ExecutionClass = harness.ExecutionAgent
		foreign.ExecutionRefID = harness.AgentExecutionRefID(foreign.ID)
		returnValue, _, getErr := s.Store.CreateInvocation(ctx, foreign)
		return returnValue, fresh, getErr
	}
	return original, fresh, nil
}

func TestHardcutCreateInvocationReceiptCannotSubstituteExecutionAuthority(t *testing.T) {
	for _, kind := range []string{
		"turn-id", "invocation-id", "run-id", "class", "capability",
		"definition", "input", "input-hash", "attempt", "parent",
		"fresh-status", "fresh-revision", "foreign-persisted",
	} {
		t.Run(kind, func(t *testing.T) {
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			client := &physicalStartCaptureModel{}
			loop, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: client})
			if err != nil {
				t.Fatal(err)
			}
			store := &forgedInvocationReceiptStore{Store: harness.NewMemoryStore(), mutation: kind}
			runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loop, Store: store})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.StartAgentTurn(t.Context(), testStartRequest())
			if !errors.Is(err, harness.ErrConflict) || len(client.requests) != 0 {
				t.Fatalf("CreateInvocation %s returned err=%v model_calls=%d; need ErrConflict before effect", kind, err, len(client.requests))
			}
		})
	}
}

type countedTeamPhysicalStart struct {
	completedTeamFeature
	calls int
}

func (feature *countedTeamPhysicalStart) StartRun(ctx context.Context, request team.StartRequest) (kernel.Snapshot, error) {
	feature.calls++
	return feature.completedTeamFeature.StartRun(ctx, request)
}

type countedWorkflowPhysicalStart struct {
	completedWorkflowFeature
	calls int
}

func (feature *countedWorkflowPhysicalStart) StartRun(ctx context.Context, request workflow.StartRequest) (kernel.Snapshot, error) {
	feature.calls++
	return feature.completedWorkflowFeature.StartRun(ctx, request)
}

func TestHardcutForeignInvocationReceiptBlocksCompositeStart(t *testing.T) {
	for _, name := range []string{"Team", "Workflow"} {
		t.Run(name, func(t *testing.T) {
			runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
			if err != nil {
				t.Fatal(err)
			}
			loop, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: directModel{}})
			if err != nil {
				t.Fatal(err)
			}
			store := &forgedInvocationReceiptStore{Store: harness.NewMemoryStore(), mutation: "foreign-persisted"}
			teams := &countedTeamPhysicalStart{completedTeamFeature: completedTeamFeature{runtime: runtime}}
			workflows := &countedWorkflowPhysicalStart{completedWorkflowFeature: completedWorkflowFeature{runtime: runtime}}
			runner, err := harness.NewRunner(harness.Dependencies{Runtime: runtime, Agent: loop, Store: store,
				Teams: teams, Workflows: workflows})
			if err != nil {
				t.Fatal(err)
			}
			if name == "Team" {
				_, err = runner.StartTeamTurn(t.Context(), harness.TeamTurnRequest{StartRequest: testStartRequest(),
					Mode: team.ExecutionSequential, Members: []team.Member{{ID: "writer", Goal: "write"}}})
			} else {
				_, err = runner.StartWorkflowTurn(t.Context(), harness.WorkflowTurnRequest{StartRequest: testStartRequest(),
					Definition: workflow.Definition{}})
			}
			if !errors.Is(err, harness.ErrConflict) || teams.calls != 0 || workflows.calls != 0 {
				t.Fatalf("foreign Invocation reached %s physical execution: err=%v team_calls=%d workflow_calls=%d",
					name, err, teams.calls, workflows.calls)
			}
		})
	}
}
