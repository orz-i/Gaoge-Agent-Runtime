package harness

import (
	"encoding/json"
	"testing"
)

func TestTerminalHandoffAuthorityRejectsAmbiguousOrChangedJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Invocation, []Item)
	}{
		{"child-input-duplicate-status", func(child *Invocation, _ []Item) {
			child.Input = append([]byte(`{"status":"completed",`), child.Input[1:]...)
		}},
		{"child-input-unknown-authority-field", func(child *Invocation, _ []Item) {
			child.Input = append([]byte(`{"otherAuthority":true,`), child.Input[1:]...)
		}},
		{"child-input-unsealed-root", func(child *Invocation, _ []Item) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(child.Input, &fields); err != nil {
				panic(err)
			}
			delete(fields, childRootKey)
			raw, err := json.Marshal(fields)
			if err != nil {
				panic(err)
			}
			child.Input = raw
		}},
		{"started-duplicate-status", func(_ *Invocation, items []Item) {
			items[0].Payload = append([]byte(`{"status":"completed",`), items[0].Payload[1:]...)
		}},
		{"started-unknown-field", func(_ *Invocation, items []Item) {
			items[0].Payload = append([]byte(`{"untrusted":"yes",`), items[0].Payload[1:]...)
		}},
		{"terminal-duplicate-status", func(_ *Invocation, items []Item) {
			items[1].Payload = append([]byte(`{"status":"running",`), items[1].Payload[1:]...)
		}},
		{"terminal-escaped-duplicate-status", func(_ *Invocation, items []Item) {
			items[1].Payload = append([]byte(`{"sta\u0074us":"running",`), items[1].Payload[1:]...)
		}},
		{"terminal-case-alias-status", func(_ *Invocation, items []Item) {
			items[1].Payload = append([]byte(`{"Status":"running",`), items[1].Payload[1:]...)
		}},
		{"terminal-unknown-field", func(_ *Invocation, items []Item) {
			items[1].Payload = append([]byte(`{"anotherOutcome":"completed",`), items[1].Payload[1:]...)
		}},
		{"terminal-invalid-status-type", func(_ *Invocation, items []Item) {
			items[1].Payload = append([]byte(`{"status":true,`), items[1].Payload[1:]...)
		}},
		{"terminal-duplicate-nested-result", func(_ *Invocation, items []Item) {
			var payload map[string]any
			if err := json.Unmarshal(items[1].Payload, &payload); err != nil {
				panic(err)
			}
			payload["result"] = json.RawMessage(`{"allowed":false,"allowed":true}`)
			encoded, err := json.Marshal(payload)
			if err != nil {
				panic(err)
			}
			items[1].Payload = encoded
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, child, original := originalTerminalHandoffFixture(t)
			items := append([]Item(nil), original...)
			tc.mutate(&child, items)
			// Rehash the altered self-consistent stored source so each
			// test reaches the strict semantic verifier, instead of
			// passing only because the old InputHash no longer matches.
			child.InputHash = hashInvocationBytes(child.Input)
			if VerifiedTerminalHandoffChild(root, child, items) ||
				ValidTurnTerminalDescendants(root, []Invocation{root, child}, items) {
				t.Fatalf("ambiguous Handoff authority was accepted: %s", tc.name)
			}
		})
	}
}

func TestTerminalHandoffAllowsNonAuthorityArtifactSiblingButNotSecondTerminal(t *testing.T) {
	root, child, items := originalTerminalHandoffFixture(t)
	artifact := items[1]
	artifact.ID = "non-authority-attachment"
	artifact.Kind = ItemArtifact
	artifact.Seq = 5
	// An Artifact child is permitted by the generic Item ledger, but it
	// cannot mint or contradict a terminal Handoff result. Handoff
	// completion counts ONLY terminal Delegation Items; other children
	// are application data, never alternative terminal votes.
	items = append(items, artifact)
	if !VerifiedTerminalHandoffChild(root, child, items) {
		t.Fatal("non-authority Artifact child blocked the original unique terminal Handoff")
	}
	contradictory := items[1]
	contradictory.ID = "second-terminal-delegation-vote"
	contradictory.Seq = 6
	items = append(items, contradictory)
	if VerifiedTerminalHandoffChild(root, child, items) {
		t.Fatal("second terminal Delegation child was counted as harmless Artifact")
	}
}

func TestTerminalHandoffSealedRootBindingIsPartOfOriginalAuthority(t *testing.T) {
	root, child, items := originalTerminalHandoffFixture(t)
	root.Attempt = 1
	seal := func(value childRootBinding) json.RawMessage {
		t.Helper()
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(child.Input, &fields); err != nil {
			t.Fatal(err)
		}
		bound, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		fields[childRootKey] = bound
		raw, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	expected := childRootBinding{InvocationID: root.ID, ExecutionRefID: root.ExecutionRefID, Attempt: root.Attempt}
	child.Input = seal(expected)
	child.InputHash = hashInvocationBytes(child.Input)
	if !VerifiedTerminalHandoffChild(root, child, items) {
		t.Fatal("legitimate first-party harnessRoot envelope was rejected")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*childRootBinding)
	}{
		{"other-root-run", func(v *childRootBinding) { v.ExecutionRefID = "foreign" }},
		{"other-root-attempt", func(v *childRootBinding) { v.Attempt++ }},
		{"other-root-invocation", func(v *childRootBinding) { v.InvocationID = "foreign" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			foreign := expected
			tc.mutate(&foreign)
			modified := child
			modified.Input = seal(foreign)
			modified.InputHash = hashInvocationBytes(modified.Input)
			if VerifiedTerminalHandoffChild(root, modified, items) {
				t.Fatalf("modified original parent binding was accepted: %s", tc.name)
			}
		})
	}
}

// Duplicate, root-repeated, or invalid ancestor Attempt bindings are
// outside the original execution authority, even with a matching root
// and an otherwise complete terminal Handoff Item chain. The same
// frozenChildRoot parser protects normal child admission and recovery.
func TestTerminalHandoffMalformedAncestorBindingsCannotSettleRoot(t *testing.T) {
	root, child, facts := originalTerminalHandoffFixture(t)
	root.Attempt = 1
	for _, tc := range []struct {
		name    string
		parents []childAttemptBinding
	}{
		{"duplicate-ancestor", []childAttemptBinding{
			{InvocationID: "ancestor", ExecutionRefID: "run-A", Attempt: 1},
			{InvocationID: "ancestor", ExecutionRefID: "run-B", Attempt: 2},
		}},
		{"repeated-root", []childAttemptBinding{
			{InvocationID: root.ID, ExecutionRefID: root.ExecutionRefID, Attempt: root.Attempt},
		}},
		{"invalid-ancestor-attempt", []childAttemptBinding{
			{InvocationID: "ancestor", ExecutionRefID: "run-A", Attempt: 0},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(child.Input, &fields); err != nil {
				t.Fatal(err)
			}
			binding := childRootBinding{
				InvocationID: root.ID, ExecutionRefID: root.ExecutionRefID,
				Attempt: root.Attempt, Parents: tc.parents,
			}
			encoded, err := json.Marshal(binding)
			if err != nil {
				t.Fatal(err)
			}
			fields[childRootKey] = encoded
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			changed := child
			changed.Input = raw
			changed.InputHash = hashInvocationBytes(raw)
			if VerifiedTerminalHandoffChild(root, changed, facts) ||
				ValidTurnTerminalDescendants(root, []Invocation{root, changed}, facts) {
				t.Fatal("unverified ancestor Attempt authorized terminal Handoff")
			}
		})
	}
}

// Parent-root ID equality alone is insufficient: an intermediate
// ancestor may have rotated to Attempt 2 after the original delegation
// froze its Attempt 1. The terminal Turn CAS must not infer the current
// tree is complete from a stale child owned by the earlier Attempt.
func TestTerminalHandoffFrozenIntermediateAttemptCannotCrossRetry(t *testing.T) {
	root, child, facts := originalTerminalHandoffFixture(t)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(child.Input, &fields); err != nil {
		t.Fatal(err)
	}
	binding := childRootBinding{
		InvocationID: root.ID, ExecutionRefID: root.ExecutionRefID,
		Attempt: root.Attempt, Parents: []childAttemptBinding{
			{InvocationID: "intermediate", ExecutionRefID: "old-parent-run", Attempt: 1},
		},
	}
	encoded, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	fields[childRootKey] = encoded
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	child.Input = raw
	child.InputHash = hashInvocationBytes(raw)
	if !VerifiedTerminalHandoffChild(root, child, facts) {
		t.Fatal("valid original historic binding rejected by basic syntax verifier")
	}
	parent := Invocation{
		ID: "intermediate", TurnID: root.TurnID, ParentItemID: "original-parent-item",
		ExecutionRefID: "old-parent-run", Attempt: 1, Status: InvocationCompleted,
	}
	if !ValidTurnTerminalDescendants(root, []Invocation{root, parent, child}, facts) {
		t.Fatal("same original frozen intermediate Attempt was rejected after successful child terminalization")
	}
	parent.ExecutionRefID = "new-parent-run"
	parent.Attempt = 2
	if ValidTurnTerminalDescendants(root, []Invocation{root, parent, child}, facts) {
		t.Fatal("P1: Accepted child of intermediate Attempt 1 authorized completion after parent rotated")
	}
	// A previously projected terminal Handoff child still belongs to its
	// *original* parent Attempt. Terminal status alone must not bypass
	// immutable ancestor binding after the parent rotates.
	completedChild := child
	completedChild.Status = InvocationCompleted
	if ValidTurnTerminalDescendants(root, []Invocation{root, parent, completedChild}, facts) {
		t.Fatal("P1: terminal Handoff child bypassed its stale intermediate Attempt fence")
	}
}

// An already-completed child is not automatically an original Handoff
// authority: its frozen Input can still have a foreign ChildRunID or
// ambiguous JSON status. The parent terminal CAS must verify the
// original source for every Handoff status, not only Accepted.
func TestTerminalHandoffAlreadyCompletedStillRequiresOriginalFrozenSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Invocation)
	}{
		{"foreign-child-run", func(child *Invocation) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(child.Input, &fields); err != nil {
				panic(err)
			}
			fields["childRunID"] = json.RawMessage(`"foreign-run"`)
			raw, err := json.Marshal(fields)
			if err != nil {
				panic(err)
			}
			child.Input = raw
		}},
		{"duplicate-json-status", func(child *Invocation) {
			child.Input = append([]byte(`{"status":"completed",`), child.Input[1:]...)
		}},
		{"unsealed-original-source", func(child *Invocation) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(child.Input, &fields); err != nil {
				panic(err)
			}
			delete(fields, childRootKey)
			raw, err := json.Marshal(fields)
			if err != nil {
				panic(err)
			}
			child.Input = raw
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, child, facts := originalTerminalHandoffFixture(t)
			child.Status = InvocationCompleted
			tc.change(&child)
			child.InputHash = hashInvocationBytes(child.Input)
			if ValidTurnTerminalDescendants(root, []Invocation{root, child}, facts) {
				t.Fatal("P1: terminal Handoff status bypassed its original frozen ChildRun and JSON authority")
			}
		})
	}
}
