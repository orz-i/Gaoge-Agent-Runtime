package harness

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestRoleToolKeysStripVisibleHandoffOutsideGroupChatMaterialization(t *testing.T) {
	role := RoleSnapshot{ToolKeys: []string{"read.tool", GroupChatHandoffToolKey, "read.tool"}}
	keys := roleToolKeys(role)
	if len(keys) != 1 || keys[0] != "read.tool" {
		t.Fatalf("role tool keys = %#v", keys)
	}
}

func TestGroupChatHandoffToolPersistsOneIdempotentVisibleSignal(t *testing.T) {
	fixture := newGroupChatHandoffToolFixture(t, "idempotent")
	authority := &recordingGroupChatHandoffAuthority{}
	handler := NewGroupChatHandoffToolHandler(fixture.signals)
	if err := handler.BindAuthority(authority); err != nil {
		t.Fatal(err)
	}
	call := func(id, target string) error {
		arguments, marshalErr := json.Marshal(map[string]string{"targetParticipantID": target})
		if marshalErr != nil {
			return marshalErr
		}
		result, executeErr := handler.Execute(t.Context(), tools.ExecutionRequest{
			RunID: fixture.childRunID,
			Call:  tools.Call{ID: id, ToolKey: GroupChatHandoffToolKey, Arguments: arguments},
		})
		if executeErr != nil {
			return executeErr
		}
		if result.Receipt.ExecutionID != id || result.Receipt.Disposition != "committed" {
			t.Fatalf("receipt = %#v", result.Receipt)
		}
		return nil
	}
	if err := call("call-1", "participant-reviewer"); err != nil {
		t.Fatal(err)
	}
	if err := call("call-replay", "participant-reviewer"); err != nil {
		t.Fatalf("same target replay failed: %v", err)
	}
	signal, found, err := fixture.signals.ResolveVisibleHandoff(t.Context(), fixture.childRunID)
	if err != nil || !found || signal.TargetParticipantID != "participant-reviewer" {
		t.Fatalf("signal=%#v found=%v err=%v", signal, found, err)
	}
	items, err := fixture.store.ListItems(t.Context(), fixture.invocation.TurnID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range items {
		if item.Kind == ItemGroupChatHandoff {
			count++
			if item.InvocationID != fixture.invocation.ID || item.RunID != fixture.childRunID {
				t.Fatalf("handoff item identity = %#v", item)
			}
		}
	}
	if count != 1 {
		t.Fatalf("handoff items = %#v", items)
	}
	if len(authority.calls) != 2 {
		t.Fatalf("authority calls = %#v", authority.calls)
	}
}

func TestGroupChatHandoffToolRejectsSecondDifferentTarget(t *testing.T) {
	fixture := newGroupChatHandoffToolFixture(t, "conflict")
	handler := NewGroupChatHandoffToolHandler(fixture.signals)
	if err := handler.BindAuthority(&recordingGroupChatHandoffAuthority{}); err != nil {
		t.Fatal(err)
	}
	execute := func(callID, target string) error {
		raw, _ := json.Marshal(map[string]string{"targetParticipantID": target})
		_, executeErr := handler.Execute(t.Context(), tools.ExecutionRequest{
			RunID: fixture.childRunID,
			Call:  tools.Call{ID: callID, ToolKey: GroupChatHandoffToolKey, Arguments: raw},
		})
		return executeErr
	}
	if err := execute("call-a", "participant-reviewer"); err != nil {
		t.Fatal(err)
	}
	err := execute("call-b", "participant-writer")
	code, _, recoverable := tools.RecoverableCallErrorInfo(err)
	if !recoverable || code != "groupchat.handoff_already_requested" {
		t.Fatalf("second target error=%v code=%q recoverable=%v", err, code, recoverable)
	}
}

type groupChatHandoffToolFixture struct {
	store      *MemoryStore
	signals    *GroupChatHandoffSignals
	invocation Invocation
	childRunID string
}

func newGroupChatHandoffToolFixture(t *testing.T, suffix string) groupChatHandoffToolFixture {
	t.Helper()
	store := NewMemoryStore()
	now := time.Date(2026, time.September, 25, 1, 0, 0, 0, time.UTC)
	rootRunID := "groupchat-root-" + suffix
	childRunID := "run-visible-speaker-" + suffix
	invocation := Invocation{
		ID: "invocation-groupchat-" + suffix, TurnID: "turn-visible-handoff-" + suffix,
		CapabilityKey: CapabilityGroupChat, DefinitionVersion: RuntimeCapabilityVersion,
		ExecutionClass: ExecutionGroupChat, ExecutionRefID: rootRunID,
		Status: InvocationRunning, Attempt: 1, Revision: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateInvocation(t.Context(), invocation); err != nil {
		t.Fatal(err)
	}
	relations, err := runrelation.New(memory.NewRunRelationStore(), fixedGroupChatHandoffClock{now: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = relations.Ensure(t.Context(), runrelation.Draft{
		ParentRunID: rootRunID, ChildRunID: childRunID,
		Kind: runrelation.KindGroupChatSpeaker, OwnerNodeID: "speaker-" + suffix,
	}); err != nil {
		t.Fatal(err)
	}
	signals, err := NewGroupChatHandoffSignals(store, relations, fixedGroupChatHandoffClock{now: now}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return groupChatHandoffToolFixture{
		store: store, signals: signals, invocation: invocation, childRunID: childRunID,
	}
}

type recordingGroupChatHandoffAuthority struct {
	calls []string
	err   error
}

func (authority *recordingGroupChatHandoffAuthority) ValidateVisibleHandoff(
	_ context.Context,
	childRunID string,
	targetParticipantID string,
) error {
	authority.calls = append(authority.calls, childRunID+"->"+targetParticipantID)
	return authority.err
}

type fixedGroupChatHandoffClock struct{ now time.Time }

func (clock fixedGroupChatHandoffClock) Now() time.Time { return clock.now }
