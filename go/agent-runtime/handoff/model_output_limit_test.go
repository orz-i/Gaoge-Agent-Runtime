package handoff_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestHandoffModelOutputLimitInheritsAndCannotWidenParent(t *testing.T) {
	for _, test := range []struct {
		name      string
		requested int64
		want      int64
		invalid   bool
	}{
		{name: "inherit", want: 64},
		{name: "tighten", requested: 32, want: 32},
		{name: "widen", requested: 65, invalid: true},
		{name: "negative", requested: -1, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, runner, provider := deferredFixture(t, nil)
			origin := verifiedHandoffParent()
			parent, err := runner.PrepareRun(t.Context(), agent.StartRequest{
				ID: origin.Run.ID, Actor: origin.Run.Actor, Thread: origin.Run.Thread,
				Goal: "coordinate", MaxModelOutputTokens: 64,
			})
			if err != nil {
				t.Fatal(err)
			}
			coordinator, err := handoff.New(handoff.Config{Local: runner, PrepareLocal: true})
			if err != nil {
				t.Fatal(err)
			}
			delegation := handoff.Delegation{
				ID: "bounded", MemberID: "local", ChildRunID: "bounded-child",
				Goal: "bounded task", Status: handoff.StatusQueued, MaxModelOutputTokens: test.requested,
			}
			got, err := coordinator.StartOrLoad(t.Context(), parent, delegation)
			if test.invalid {
				if !errors.Is(err, handoff.ErrInvalidDelegation) || got.ID != "" || provider.calls != 0 {
					t.Fatalf("invalid child output ceiling was admitted: %+v %v", got, err)
				}
				return
			}
			if !errors.Is(err, handoff.ErrChildPending) || got.MaxModelOutputTokens != test.want || provider.calls != 0 {
				t.Fatalf("frozen child ceiling lost: %+v %v", got, err)
			}
			child, err := runtime.Load(t.Context(), delegation.ChildRunID)
			if err != nil {
				t.Fatal(err)
			}
			view, err := agent.ViewState(child)
			if err != nil || view.MaxModelOutputTokens != test.want || !handoff.VerifiedChildRun(got, parent.Run, child) {
				t.Fatalf("child did not persist its effective ceiling: %+v %v", view, err)
			}
			reloaded, err := coordinator.StartOrLoad(t.Context(), parent, delegation)
			if !errors.Is(err, handoff.ErrChildPending) || reloaded.MaxModelOutputTokens != test.want {
				t.Fatalf("reloading did not preserve the same inherited ceiling: %+v %v", reloaded, err)
			}
			var state map[string]json.RawMessage
			if err = json.Unmarshal(child.State, &state); err != nil {
				t.Fatal(err)
			}
			for _, forged := range []json.RawMessage{json.RawMessage(`0`), json.RawMessage(`128`)} {
				state["maxModelOutputTokens"] = forged
				child.State, err = json.Marshal(state)
				if err != nil {
					t.Fatal(err)
				}
				if handoff.VerifiedChildRun(got, parent.Run, child) {
					t.Fatalf("same-ID child with another output ceiling was accepted: %s", forged)
				}
			}
		})
	}
}

func TestHandoffModelOutputLimitCannotIgnoreInvalidAgentParentState(t *testing.T) {
	runtime, runner, _ := deferredFixture(t, nil)
	origin := verifiedHandoffParent()
	parent, err := runner.PrepareRun(t.Context(), agent.StartRequest{
		ID: origin.Run.ID, Actor: origin.Run.Actor, Thread: origin.Run.Thread,
		Goal: "coordinate", MaxModelOutputTokens: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := handoff.New(handoff.Config{Local: runner, PrepareLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	parent.State = json.RawMessage(`{"maxModelOutputTokens":64}`)
	delegation := handoff.Delegation{
		ID: "invalid-parent", MemberID: "local", ChildRunID: "invalid-parent-child",
		Goal: "bounded task", Status: handoff.StatusQueued,
	}
	if got, err := coordinator.StartOrLoad(t.Context(), parent, delegation); !errors.Is(err, handoff.ErrInvalidDelegation) || got.ID != "" {
		t.Fatalf("unknown Agent parent ceiling became an unbounded child: %+v %v", got, err)
	}
	if _, err := runtime.Load(t.Context(), delegation.ChildRunID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("invalid Agent parent created a child: %v", err)
	}
	if got, err := coordinator.Refresh(t.Context(), parent, delegation); !errors.Is(err, handoff.ErrInvalidDelegation) || got.ID != "" {
		t.Fatalf("unknown Agent parent ceiling authorized Refresh: %+v %v", got, err)
	}
}
