package agentruntime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	testAgentActorID       = "actor"
	testAgentTenantID      = "tenant"
	testAgentParentRunID   = "run-parent"
	testAgentReviewID      = "agent-review"
	testAgentSearchToolKey = "search"
	testJoinHandoffA       = "handoff-a"
	testJoinOne            = "one"
	testJoinTwo            = "two"
	testJoinThree          = "three"
)

type agentRuntimeTestStore struct {
	Store
	runs      map[string]model.Run
	manifests map[string]model.AgentManifest
	handoffs  []model.RunHandoff
}

func TestCreateRunHandoffJoinCanonicalizesPolicy(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	parent := model.Run{
		RunID: testAgentParentRunID, Actor: actor, Status: model.RunStatusRunning, CurrentStepID: "step-parent",
		RunConfigSnapshotJSON: mustRunJSON(effectiveTextRunConfig{SemanticVersion: RuntimeSnapshotVersion, Strategy: TextRunStrategyDirect}),
	}
	normalized, err := normalizeCreateRunHandoffJoinInput(CreateRunHandoffJoinInput{
		Actor: actor, ParentRunID: parent.RunID, ClientJoinID: "client-join", HandoffIDs: []string{"handoff-b", testJoinHandoffA},
	})
	if err != nil {
		t.Fatal(err)
	}
	join, checkpoint, events, err := buildRunHandoffJoinWait(parent, normalized)
	if err != nil {
		t.Fatal(err)
	}
	policy := struct {
		mode, failure string
		quorum        int
	}{join.Mode, join.FailurePolicy, join.Quorum}
	wantPolicy := struct {
		mode, failure string
		quorum        int
	}{model.RunHandoffJoinModeAll, model.RunHandoffJoinFailureCollect, 1}
	if policy != wantPolicy {
		t.Fatalf("join policy=%#v", policy)
	}
	if !slices.Equal(join.HandoffIDs, []string{testJoinHandoffA, "handoff-b"}) {
		t.Fatalf("canonical join IDs=%#v", join.HandoffIDs)
	}
	if join.RequestFingerprint == "" {
		t.Fatal("join fingerprint is empty")
	}
	if !strings.HasPrefix(join.JoinID, "join_") {
		t.Fatalf("join ID=%q", join.JoinID)
	}
	if checkpoint.CheckpointID != join.ResumeCheckpointID || checkpoint.Kind != handoffJoinCheckpointKind || len(events) != 4 {
		t.Fatalf("join wait bundle join=%#v checkpoint=%#v events=%#v", join, checkpoint, events)
	}
}

func TestCreateRunHandoffJoinRejectsDuplicateMembers(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	if _, err := normalizeCreateRunHandoffJoinInput(CreateRunHandoffJoinInput{
		Actor: actor, ParentRunID: testAgentParentRunID, ClientJoinID: "duplicate", HandoffIDs: []string{testJoinHandoffA, testJoinHandoffA},
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate handoff IDs error=%v", err)
	}
}

func TestResolveRunHandoffJoinQuorumIsMonotonic(t *testing.T) {
	now := time.Now().UTC()
	join := model.RunHandoffJoin{
		HandoffIDs: []string{testJoinOne, testJoinTwo, testJoinThree}, Mode: model.RunHandoffJoinModeQuorum, Quorum: 2,
		FailurePolicy: model.RunHandoffJoinFailureCollect, Status: model.RunHandoffJoinStatusPending,
	}
	pending := model.ResolveRunHandoffJoin(join, []model.RunHandoff{
		{HandoffID: testJoinOne, Status: model.RunHandoffStatusCompleted},
		{HandoffID: testJoinTwo, Status: model.RunHandoffStatusFailed},
		{HandoffID: testJoinThree, Status: model.RunHandoffStatusQueued},
	}, now)
	if pending.Status != model.RunHandoffJoinStatusPending || pending.CompletedCount != 1 || pending.PendingCount != 1 {
		t.Fatalf("pending quorum=%#v", pending)
	}
	failed := model.ResolveRunHandoffJoin(pending, []model.RunHandoff{
		{HandoffID: testJoinOne, Status: model.RunHandoffStatusCompleted},
		{HandoffID: testJoinTwo, Status: model.RunHandoffStatusFailed},
		{HandoffID: testJoinThree, Status: model.RunHandoffStatusCancelled},
	}, now.Add(time.Second))
	if failed.Status != model.RunHandoffJoinStatusFailed || failed.ErrorCode != "handoff_join_quorum_unreachable" {
		t.Fatalf("failed quorum=%#v", failed)
	}
	ready := model.ResolveRunHandoffJoin(join, []model.RunHandoff{
		{HandoffID: testJoinOne, Status: model.RunHandoffStatusCompleted},
		{HandoffID: testJoinTwo, Status: model.RunHandoffStatusCompleted},
		{HandoffID: testJoinThree, Status: model.RunHandoffStatusQueued},
	}, now)
	if ready.Status != model.RunHandoffJoinStatusReady || ready.ResolvedAt == nil {
		t.Fatalf("ready quorum=%#v", ready)
	}
	unchanged := model.ResolveRunHandoffJoin(ready, []model.RunHandoff{
		{HandoffID: testJoinOne, Status: model.RunHandoffStatusFailed},
		{HandoffID: testJoinTwo, Status: model.RunHandoffStatusFailed},
		{HandoffID: testJoinThree, Status: model.RunHandoffStatusFailed},
	}, now.Add(2*time.Second))
	if unchanged.Status != model.RunHandoffJoinStatusReady || unchanged.CompletedCount != ready.CompletedCount {
		t.Fatalf("terminal join regressed: before=%#v after=%#v", ready, unchanged)
	}
}

func (s *agentRuntimeTestStore) GetRun(_ context.Context, actor model.ActorRef, runID string) (*model.Run, error) {
	run, ok := s.runs[runID]
	if !ok || run.Actor != actor {
		return nil, ErrNotFound
	}
	return &run, nil
}

func (s *agentRuntimeTestStore) GetAgentManifest(_ context.Context, actor model.ActorRef, ref model.ResourceRef) (*model.AgentManifest, error) {
	item, ok := s.manifests[ref.ID]
	if !ok || item.TenantID != actor.TenantID {
		return nil, ErrNotFound
	}
	return &item, nil
}

func (s *agentRuntimeTestStore) ListRunHandoffs(_ context.Context, actor model.ActorRef, filter model.RunHandoffFilter) (model.RunHandoffPage, error) {
	items := make([]model.RunHandoff, 0)
	for _, item := range s.handoffs {
		if item.Actor == actor && (filter.ParentRunID == "" || item.ParentRunID == filter.ParentRunID) {
			items = append(items, item)
		}
	}
	return model.RunHandoffPage{Total: int64(len(items)), Results: items}, nil
}

func TestTextRunAgentManifestFreezesDelegatedInstructions(t *testing.T) {
	manifest := model.AgentManifest{
		ManifestID: "agent-research", Revision: 3, Name: "Research specialist", Description: "Collect bounded evidence.",
		Instructions: "Return a concise source-backed summary.", ToolKeys: []string{testAgentSearchToolKey},
		SkillRefs: []model.ResourceRef{{Kind: ResourceKindSkill, ID: "research"}}, MaxChildRuns: 2, MaxDepth: 3,
	}
	instructions, snapshot := textRunAgentManifest(StartTextRunInput{Delegation: &RunDelegationStart{Manifest: manifest}}, "Environment rules")
	if snapshot == nil || snapshot.Ref.Revision != "3" || snapshot.Name != manifest.Name || len(snapshot.ToolKeys) != 1 {
		t.Fatalf("manifest snapshot = %#v", snapshot)
	}
	for _, expected := range []string{"Environment rules", "Research specialist", "Collect bounded evidence.", "Return a concise source-backed summary."} {
		if !strings.Contains(instructions, expected) {
			t.Fatalf("instructions %q do not contain %q", instructions, expected)
		}
	}
}

func TestValidateDelegationLimitsFailsBeforeChildExecution(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	parent := model.Run{RunID: testAgentParentRunID, Actor: actor, Status: model.RunStatusRunning}
	manifest := model.AgentManifest{MaxChildRuns: 1, MaxDepth: 1}
	store := &agentRuntimeTestStore{runs: map[string]model.Run{parent.RunID: parent}, handoffs: []model.RunHandoff{{Actor: actor, ParentRunID: parent.RunID}}}
	engine := &Engine{repo: store}
	if err := engine.validateDelegationLimits(t.Context(), actor, parent, manifest, 2); !errors.Is(err, ErrRunHandoffDepth) {
		t.Fatalf("depth error = %v", err)
	}
	if err := engine.validateDelegationLimits(t.Context(), actor, parent, manifest, 1); !errors.Is(err, ErrRunHandoffLimit) {
		t.Fatalf("child limit error = %v", err)
	}
}

func TestResolveDelegationSourceRejectsBlockedParentAndDisabledManifest(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	manifestRef := model.ResourceRef{Kind: model.AgentManifestKind, ID: testAgentReviewID, Revision: "1"}
	input := DelegateTextRunInput{Actor: actor, ParentRunID: testAgentParentRunID, AgentManifest: manifestRef}
	store := &agentRuntimeTestStore{
		runs:      map[string]model.Run{testAgentParentRunID: {RunID: testAgentParentRunID, Actor: actor, Status: model.RunStatusCompleted}},
		manifests: map[string]model.AgentManifest{testAgentReviewID: {ManifestID: testAgentReviewID, Revision: 1, TenantID: actor.TenantID, Status: model.AgentManifestStatusActive}},
	}
	engine := &Engine{repo: store}
	if _, _, err := engine.resolveDelegationSource(t.Context(), input); !errors.Is(err, ErrRunHandoffParentBlocked) {
		t.Fatalf("blocked parent error = %v", err)
	}
	store.runs[testAgentParentRunID] = model.Run{RunID: testAgentParentRunID, Actor: actor, Status: model.RunStatusRunning, OutputProjection: model.ProjectionRef{Kind: "host.message", ID: "message-1"}}
	disabled := store.manifests[testAgentReviewID]
	disabled.Status = model.AgentManifestStatusDisabled
	store.manifests[testAgentReviewID] = disabled
	if _, _, err := engine.resolveDelegationSource(t.Context(), input); !errors.Is(err, ErrAgentManifestDisabled) {
		t.Fatalf("disabled manifest error = %v", err)
	}
}

func TestDelegatedPublicIDsAreStableAndActorScoped(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	handoffID, runID := delegatedPublicIDs(actor, "client-handoff")
	replayedHandoffID, replayedRunID := delegatedPublicIDs(actor, "client-handoff")
	otherHandoffID, otherRunID := delegatedPublicIDs(model.ActorRef{TenantID: testAgentTenantID, ActorID: "other"}, "client-handoff")
	if handoffID != replayedHandoffID || runID != replayedRunID || handoffID == otherHandoffID || runID == otherRunID {
		t.Fatalf("stable IDs = %q/%q replay=%q/%q other=%q/%q", handoffID, runID, replayedHandoffID, replayedRunID, otherHandoffID, otherRunID)
	}
}
