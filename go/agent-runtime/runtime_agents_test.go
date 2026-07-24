package agentruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	testAgentActorID       = "actor"
	testAgentTenantID      = "tenant"
	testAgentParentRunID   = "run-parent"
	testAgentReviewID      = "agent-review"
	testAgentSearchToolKey = "search"
)

type agentRuntimeTestStore struct {
	Store
	runs      map[string]model.Run
	manifests map[string]model.AgentManifest
	handoffs  []model.RunHandoff
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
