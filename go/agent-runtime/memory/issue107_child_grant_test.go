package memory_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func newIssue107Run(t *testing.T, rt *kernel.Runtime, id, parent string) kernel.Snapshot {
	t.Helper()
	snapshot, err := rt.Create(t.Context(), kernel.CreateRequest{
		ID: id, Kind: "workflow", ParentRunID: parent,
		Actor:     kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread:    kernel.ThreadRef{Kind: "conversation", ID: "thread"},
		RequestID: id, Goal: "original child authority", State: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestIssue107MemoryChildGrantAtomicParentTerminal(t *testing.T) {
	store := memory.NewStore()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	root := newIssue107Run(t, rt, "root", "")
	mid := newIssue107Run(t, rt, "subworkflow", root.Run.ID)
	child := newIssue107Run(t, rt, "agentchild", mid.Run.ID)
	grant, err := rt.BeginChildExecution(t.Context(), child.Run.ID, child.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{root.Run.ID, mid.Run.ID} {
		parent, loadErr := rt.Load(t.Context(), id)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		_, cancelErr := rt.Cancel(t.Context(), id, parent.Run.Revision, "terminal while Tool runs")
		if !errors.Is(cancelErr, kernel.ErrChildExecutionBusy) {
			t.Fatalf("ancestor=%s terminal CAS escaped pending Grant: %v", id, cancelErr)
		}
	}
	if err = store.FinishChildExecution(t.Context(), kernel.ChildExecutionGrant{ID: grant.ID, ChildRunID: "different", ExpectedRevision: grant.ExpectedRevision}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("forged child released original Grant: %v", err)
	}
	if err = store.FinishChildExecution(t.Context(), grant); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("unsafe original pre-callback release accepted: %v", err)
	}
	proof := featurecontrol.Seal(child.Run.ID, child.Run.Revision, "workflow_child_physical_settled", grant.ID, child.State, child.Checkpoint)
	if err = rt.MarkChildExecutionSettled(t.Context(), grant, proof); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishChildExecution(t.Context(), grant); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishChildExecution(t.Context(), grant); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("duplicate Grant settlement accepted: %v", err)
	}
	cancelled, err := rt.Cancel(t.Context(), root.Run.ID, root.Run.Revision, "after physical receipt")
	if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("verified settlement did not unlock ancestor: %+v err=%v", cancelled.Run, err)
	}
	if _, err = rt.BeginChildExecution(t.Context(), child.Run.ID, child.Run.Revision); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("terminated ancestor admitted child: %v", err)
	}
	if _, err = rt.Create(t.Context(), kernel.CreateRequest{
		ID: "late-child", Kind: "agent", ParentRunID: root.Run.ID,
		Actor: root.Run.Actor, Thread: root.Run.Thread, Goal: "late", State: []byte(`{}`),
	}); !errors.Is(err, kernel.ErrExecutionConflict) {
		t.Fatalf("late child created after parent Cancel: %v", err)
	}
}

func TestIssue107MemoryParentCancelChildBeginLinearizable(t *testing.T) {
	for iteration := 0; iteration < 80; iteration++ {
		t.Run(fmt.Sprint(iteration), func(t *testing.T) {
			localStore := memory.NewStore()
			rt, err := kernel.New(kernel.Dependencies{Store: localStore})
			if err != nil {
				t.Fatal(err)
			}
			root := newIssue107Run(t, rt, "root", "")
			child := newIssue107Run(t, rt, "child", root.Run.ID)
			var wg sync.WaitGroup
			gate := make(chan struct{})
			var grant kernel.ChildExecutionGrant
			var beginErr, cancelErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-gate
				grant, beginErr = rt.BeginChildExecution(context.Background(), child.Run.ID, child.Run.Revision)
			}()
			go func() {
				defer wg.Done()
				<-gate
				_, cancelErr = rt.Cancel(context.Background(), root.Run.ID, root.Run.Revision, "racing")
			}()
			close(gate)
			wg.Wait()
			if beginErr == nil && cancelErr == nil {
				t.Fatal("parent terminated and child began physical work concurrently")
			}
			if beginErr == nil {
				if !errors.Is(cancelErr, kernel.ErrChildExecutionBusy) {
					t.Fatalf("child won but parent Cancel=%v", cancelErr)
				}
				proof := featurecontrol.Seal(child.Run.ID, child.Run.Revision, "workflow_child_physical_settled", grant.ID, child.State, child.Checkpoint)
				if err = rt.MarkChildExecutionSettled(t.Context(), grant, proof); err != nil {
					t.Fatal(err)
				}
				if err = localStore.FinishChildExecution(t.Context(), grant); err != nil {
					t.Fatal(err)
				}
			} else if cancelErr == nil {
				if !errors.Is(beginErr, kernel.ErrExecutionConflict) {
					t.Fatalf("parent won but child Begin=%v", beginErr)
				}
			} else {
				t.Fatalf("neither branch made progress: begin=%v cancel=%v", beginErr, cancelErr)
			}
		})
	}
}

func TestIssue107MemoryChildExecutionConformance(t *testing.T) {
	conformance.RunChildExecutionSuite(t, func(*testing.T) kernel.Store { return memory.NewStore() })
}

func TestIssue107MemoryParentedWorkflowPhysicalGrantConformance(t *testing.T) {
	conformance.RunParentedWorkflowSuite(t, func(child *testing.T) kernel.Store { return memory.NewStore() })
}

func TestIssue107MemoryRootWorkflowPhysicalGrantConformance(t *testing.T) {
	conformance.RunRootWorkflowSuite(t, func(child *testing.T) kernel.Store { return memory.NewStore() })
}
