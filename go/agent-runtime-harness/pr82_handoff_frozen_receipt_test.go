package harness

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
)

func pr82FrozenReceiptDelegation(role bool) handoff.Delegation {
	delegation := handoff.Delegation{
		ID: "pr82-delegation", MemberID: "analyst", MemberRevision: "member-v2",
		ChildRunID: "pr82-delegated-run", Goal: "review", Status: handoff.StatusQueued,
		Instructions: "read-only review", Model: "frozen-model",
		ModelOptions:     json.RawMessage(`{"temperature":0,"top_p":0.5}`),
		ToolKeys:         []string{"hosted.search", "local.lookup"},
		InitialToolKeys:  []string{"local.lookup"},
		HostedToolGrants: []agent.HostedToolGrant{{Key: "hosted.search", DefinitionVersion: "catalog-v2"}},
		Limits:           agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 4},
	}
	if role {
		delegation.RoleID, delegation.RoleRevision, delegation.RoleName = "analyst", 5, "Research Analyst"
	}
	return delegation
}

// A reply with the same Member/Role/Run identity must not certify a different
// frozen execution policy. Retaining the grant prevents a second remote Start.
func TestPR82HandoffFrozenExecutionScopeMismatchRetainsGrant(t *testing.T) {
	cases := []struct {
		name    string
		alter   func(*handoff.Delegation)
		inPlace bool
	}{
		{"instructions", func(d *handoff.Delegation) { d.Instructions = "unrestricted review" }, false},
		{"limits", func(d *handoff.Delegation) { d.Limits.MaxLLMCalls = 100 }, false},
		{"model", func(d *handoff.Delegation) { d.Model = "different-model" }, false},
		{"model-options", func(d *handoff.Delegation) { d.ModelOptions = json.RawMessage(`{"temperature":1,"top_p":0.5}`) }, false},
		{"tool-keys", func(d *handoff.Delegation) { d.ToolKeys = append(d.ToolKeys, "local.write") }, false},
		{"initial-tool-keys", func(d *handoff.Delegation) { d.InitialToolKeys = []string{"hosted.search"} }, false},
		{"hosted-grants", func(d *handoff.Delegation) { d.HostedToolGrants[0].DefinitionVersion = "catalog-v3" }, false},
		{"in-place-tool-key", func(d *handoff.Delegation) { d.ToolKeys[0] = "local.write" }, true},
		{"in-place-hosted-grant", func(d *handoff.Delegation) { d.HostedToolGrants[0].DefinitionVersion = "catalog-v3" }, true},
	}
	for _, role := range []bool{false, true} {
		mode := "member-only"
		if role {
			mode = "role"
		}
		t.Run(mode, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					f := newPR82HandoffFixture(t)
					requested := pr82FrozenReceiptDelegation(role)
					if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, requested, f.startedItemID); err != nil {
						t.Fatal(err)
					}
					f.handoff.result = func(d handoff.Delegation) (handoff.Delegation, error) {
						if !tc.inPlace {
							// Also prove a returned, independently allocated slice is
							// rejected, not just an in-place mutation of an input.
							d.HostedToolGrants = append([]agent.HostedToolGrant(nil), d.HostedToolGrants...)
						}
						tc.alter(&d)
						d.Status = handoff.StatusRunning
						return d, handoff.ErrChildPending
					}
					_, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, requested, f.startedItemID)
					if !errors.Is(err, ErrExecutionOutcomeUnknown) {
						t.Errorf("mismatched frozen %s must remain unknown: %v", tc.name, err)
					}
					items, listErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
					if listErr != nil || !PendingExecutionDispatch(items, "") {
						t.Errorf("mismatched receipt settled physical grant: %v", listErr)
					}
					if _, retryErr := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, requested, f.startedItemID); !errors.Is(retryErr, ErrConflict) {
						t.Errorf("unknown remote start was redispatched: %v", retryErr)
					}
					if f.handoff.calls != 1 {
						t.Errorf("Handoff calls = %d, want 1", f.handoff.calls)
					}
				})
			}
		})
	}
}

// Allocated-but-empty slices become nil when the Coordinator clones its
// Delegation; that representation change does not alter execution authority.
func TestPR82HandoffFrozenExecutionScopeAllowsEmptySliceProjection(t *testing.T) {
	f := newPR82HandoffFixture(t)
	requested := f.delegation()
	requested.ModelOptions = json.RawMessage{}
	requested.ToolKeys = []string{}
	requested.InitialToolKeys = []string{}
	requested.HostedToolGrants = []agent.HostedToolGrant{}
	if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, requested, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	f.handoff.result = func(d handoff.Delegation) (handoff.Delegation, error) {
		d.ModelOptions, d.ToolKeys, d.InitialToolKeys, d.HostedToolGrants = nil, nil, nil, nil
		d.Status = handoff.StatusRunning
		return d, handoff.ErrChildPending
	}
	if _, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, requested, f.startedItemID); !errors.Is(err, handoff.ErrChildPending) || errors.Is(err, ErrExecutionOutcomeUnknown) {
		t.Fatalf("semantically empty slices changed frozen scope: %v", err)
	}
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil || PendingExecutionDispatch(items, "") {
		t.Fatalf("matching execution with empty slices left a grant: %v", err)
	}
}

// Even a caller with a matching Run/Member ID cannot change the persisted
// execution policy before dispatch. No grant or remote effect is admitted.
func TestPR82HandoffFrozenExecutionScopeRejectsChangedRequestBeforeGrant(t *testing.T) {
	f := newPR82HandoffFixture(t)
	requested := pr82FrozenReceiptDelegation(true)
	if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, requested, f.startedItemID); err != nil {
		t.Fatal(err)
	}
	altered := requested
	altered.Model = "different-model"
	if _, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, altered, f.startedItemID); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request bypassed durable frozen Input: %v", err)
	}
	items, err := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
	if err != nil || PendingExecutionDispatch(items, "") || f.handoff.calls != 0 {
		t.Fatalf("rejected request admitted effects: err=%v calls=%d", err, f.handoff.calls)
	}
}

func TestPR82HandoffFrozenExecutionScopeAllowsOnlyOutcomeProjection(t *testing.T) {
	cases := []struct {
		name   string
		status handoff.Status
		err    error
	}{
		{"running", handoff.StatusRunning, handoff.ErrChildPending},
		{"completed", handoff.StatusCompleted, nil},
		{"failed", handoff.StatusFailed, handoff.ErrChildFailed},
	}
	for _, role := range []bool{false, true} {
		mode := "member-only"
		if role {
			mode = "role"
		}
		t.Run(mode, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					f := newPR82HandoffFixture(t)
					requested := pr82FrozenReceiptDelegation(role)
					if err := f.runner.prepareHandoffChild(t.Context(), f.turn, f.parent, requested, f.startedItemID); err != nil {
						t.Fatal(err)
					}
					f.handoff.result = func(d handoff.Delegation) (handoff.Delegation, error) {
						d.Status = tc.status
						d.Result = json.RawMessage(`{"result":"verified"}`)
						d.ErrorCode, d.Error = "remote.reason", "remote diagnostic"
						return d, tc.err
					}
					result, err := f.runner.dispatchHandoff(t.Context(), f.turn, f.parentRun, requested, f.startedItemID)
					if !errors.Is(err, tc.err) || errors.Is(err, ErrExecutionOutcomeUnknown) || result.Status != tc.status ||
						string(result.Result) != `{"result":"verified"}` || result.ErrorCode != "remote.reason" {
						t.Errorf("verified outcome was rejected: result=%+v err=%v", result, err)
					}
					items, listErr := f.store.ListItems(t.Context(), f.turn.ID, 0, 500)
					if listErr != nil || PendingExecutionDispatch(items, "") {
						t.Errorf("verified receipt left pending grant: %v", listErr)
					}
					if f.handoff.calls != 1 {
						t.Errorf("Handoff calls = %d, want 1", f.handoff.calls)
					}
				})
			}
		})
	}
}
