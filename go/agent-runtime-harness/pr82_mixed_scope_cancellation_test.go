package harness

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestIndependentNativeAncestorAcrossApplicationGap(t *testing.T) {
	for _, mode := range []string{"invocation", "subtask"} {
		t.Run(mode, func(t *testing.T) {
			f := newPR82HandoffFixture(t)
			f.runner.cancellation = f.runtime
			f.runner.relationReader = f.runner.relations.(RunRelationSource)
			sibling := auditMixedNativeChild(t, f, f.startedItemID, "unrelated-native-sibling")
			parent := auditMixedNativeChild(t, f, f.startedItemID, "native-parent")
			if e := f.runner.recordInvocationItem(t.Context(), parent); e != nil {
				t.Fatal(e)
			}
			parentItem := invocationLifecycleItemID(parent, parent.Status, parent.Revision)
			raw, hash, e := marshalInvocationValue(applicationInvocationInput{Goal: "intermediate", Input: []byte(`{}`)})
			if e != nil {
				t.Fatal(e)
			}
			app, _, _, e := f.runner.beginChildInvocation(t.Context(), f.turn.ID, parentItem, "app-gap", "app.gap", "1", ExecutionApplication, raw, hash)
			if e != nil {
				t.Fatal(e)
			}
			app.Status = InvocationRunning
			app, e = f.store.UpdateInvocation(t.Context(), app, app.Revision)
			if e != nil {
				t.Fatal(e)
			}
			if e = f.runner.recordInvocationItem(t.Context(), app); e != nil {
				t.Fatal(e)
			}
			appItem := invocationLifecycleItemID(app, app.Status, app.Revision)
			leaf := auditMixedNativeChild(t, f, appItem, "native-grandchild")
			if _, e = f.runner.completeApplicationInvocation(t.Context(), f.turn, app, nil); e != nil {
				t.Fatal(e)
			}
			if mode == "invocation" {
				_, e = f.runner.CancelInvocation(t.Context(), parent.ID, "stop subtree")
			} else {
				s, le := f.runner.Load(t.Context(), f.turn.ID)
				if le != nil {
					t.Fatal(le)
				}
				id := ""
				for _, task := range s.Subtasks {
					if task.RunID == parent.ExecutionRefID {
						id = task.ID
					}
				}
				if id == "" {
					t.Fatal("no parent subtask")
				}
				_, e = f.runner.CancelSubtask(t.Context(), f.turn.ID, id, "stop subtree")
			}
			s, le := f.runtime.Load(t.Context(), leaf.ExecutionRefID)
			if le != nil {
				t.Fatal(le)
			}
			items, ie := listAllItems(t.Context(), f.store, f.turn.ID)
			if ie != nil {
				t.Fatal(ie)
			}
			if e == nil && s.Run.Status != kernel.RunStatusCancelled {
				t.Fatalf("successful %s cancellation leaves descendant %s; activeIntent=%v", mode, s.Run.Status, ActiveCancellationFence(items))
			}
			if e != nil {
				t.Fatalf("unexpected cancellation error %v", e)
			}
			unrelated, siblingErr := f.runtime.Load(t.Context(), sibling.ExecutionRefID)
			if siblingErr != nil || unrelated.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("unrelated sibling changed: status=%s err=%v", unrelated.Run.Status, siblingErr)
			}
			if ActiveCancellationFence(items) {
				t.Fatal("successful verified cleanup left its intent open")
			}
		})
	}
}
