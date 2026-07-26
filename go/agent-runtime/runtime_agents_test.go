package agentruntime

import (
	"context"
	"encoding/json"
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
	testAgentStoryScope    = "story"
	testAgentStoryReadTool = "story.read"
	testAgentParentRunID   = "run-parent"
	testAgentReviewID      = "agent-review"
	testAgentSearchToolKey = "search"
	testJoinHandoffA       = "handoff-a"
	testJoinOne            = "one"
	testJoinTwo            = "two"
	testJoinThree          = "three"
	testJoinReadyID        = "join-ready"
	testMissingRunID       = "run-missing"
)

type agentRuntimeTestStore struct {
	Store
	runs              map[string]model.Run
	manifests         map[string]model.AgentManifest
	handoffs          []model.RunHandoff
	handoffCompletion model.RunHandoffCompletionResult
	appendedRunEvents []model.Event
	getRunCalls       []string
	batchRunCalls     [][]string
}

func TestNormalizeAgentManifestOwnership(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	tests := []struct {
		name     string
		input    AgentManifestRevisionInput
		scope    string
		tenantID string
		ownerID  string
		valid    bool
	}{
		{name: "actor defaults", input: AgentManifestRevisionInput{Actor: actor}, scope: model.AgentManifestScopeActor, tenantID: actor.TenantID, ownerID: actor.ActorID, valid: true},
		{name: "tenant target", input: AgentManifestRevisionInput{Actor: actor, Scope: model.AgentManifestScopeTenant, TenantID: "tenant-shared"}, scope: model.AgentManifestScopeTenant, tenantID: "tenant-shared", valid: true},
		{name: "system clears owner", input: AgentManifestRevisionInput{Actor: actor, Scope: model.AgentManifestScopeSystem, TenantID: "ignored", OwnerActorID: "ignored"}, scope: model.AgentManifestScopeSystem, valid: true},
		{name: "invalid scope", input: AgentManifestRevisionInput{Actor: actor, Scope: "workspace"}, valid: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope, tenantID, ownerID, valid := normalizeAgentManifestOwnership(test.input)
			if scope != test.scope || tenantID != test.tenantID || ownerID != test.ownerID || valid != test.valid {
				t.Fatalf("ownership = %q,%q,%q,%t", scope, tenantID, ownerID, valid)
			}
		})
	}
}

func TestResolveTextRunAgentManifestFreezesRootRevision(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	manifest := model.AgentManifest{
		ManifestID: "agent-root", Revision: 4, TenantID: actor.TenantID, Name: "Lead writer", Status: model.AgentManifestStatusActive,
		ModelName: "model-lead", ExecutionMode: TextRunExecutionModeDirect, ToolKeys: []string{testAgentStoryReadTool, testPublishToolKey},
		SkillRefs: []model.ResourceRef{{Kind: ResourceKindSkill, ID: "writing"}}, MaxChildRuns: 3, MaxDepth: 2,
	}
	engine := &Engine{repo: &agentRuntimeTestStore{manifests: map[string]model.AgentManifest{manifest.ManifestID: manifest}}}
	resolved, frozen, err := engine.resolveTextRunAgentManifest(t.Context(), StartTextRunInput{
		Actor: actor, AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: manifest.ManifestID},
	})
	if err != nil || frozen == nil {
		t.Fatalf("resolve root manifest = %#v, %v", frozen, err)
	}
	if resolved.AgentManifest.Revision != "4" || resolved.PlatformModelName != manifest.ModelName || resolved.ExecutionMode != TextRunExecutionModeDirect {
		t.Fatalf("resolved input = %#v", resolved)
	}
	if resolved.ToolKeys == nil || !slices.Equal(*resolved.ToolKeys, manifest.ToolKeys) || resolved.SkillRefs == nil || !slices.Equal(*resolved.SkillRefs, manifest.SkillRefs) {
		t.Fatalf("resolved capabilities = tools=%#v skills=%#v", resolved.ToolKeys, resolved.SkillRefs)
	}
}

func TestDelegatedTextRunStartInputInheritsFrozenWorkspace(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	workspace := &WorkspaceSnapshot{
		SchemaVersion: RuntimeSnapshotVersion,
		Request:       ResolvedWorkspaceContext{SchemaVersion: RuntimeSnapshotVersion, Type: testAgentStoryScope, ResourceID: "story_1", Revision: 41, ArtifactContract: "review"},
		Revision:      41, SnapshotID: "snapshot_story_41", StateHash: "hash_story_41", ContentJSON: `{"storyID":"story_1"}`,
		Tools: []WorkspaceToolDefinition{{ToolKey: testAgentStoryReadTool, Name: "story_read", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}
	parent := model.Run{
		RunID: testAgentParentRunID, Actor: actor, Thread: model.ThreadRef{Kind: threadKindConversation, ID: "conversation_1"},
		Environment: model.ResourceRef{Kind: resourceKindEnvironment, ID: "2", Revision: "3"}, PlatformModelName: "model-parent", Provider: "provider",
		RunConfigSnapshotJSON: mustRunJSON(effectiveTextRunConfig{SemanticVersion: RuntimeSnapshotVersion, Workspace: workspace}),
	}
	manifest := model.AgentManifest{ManifestID: "agent-specialist", Revision: 2, TenantID: actor.TenantID, Name: "Specialist", Status: model.AgentManifestStatusActive, ToolKeys: []string{testAgentStoryReadTool}}
	prepared := preparedDelegation{parent: parent, manifest: manifest, handoff: model.RunHandoff{HandoffID: "handoff_1", ChildRunID: "run_child", Goal: "Audit Story"}}
	start, err := delegatedTextRunStartInput(DelegateTextRunInput{Actor: actor}, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if start.FrozenWorkspace == nil || start.FrozenWorkspace.SnapshotID != workspace.SnapshotID || start.ThreadScope != testAgentStoryScope {
		t.Fatalf("delegated start = %#v", start)
	}
	if start.Workspace != nil || start.Delegation == nil || start.Delegation.Manifest.Ref() != manifest.Ref() {
		t.Fatalf("delegation contract = %#v", start)
	}
}

func (s *agentRuntimeTestStore) ListRunHandoffJoins(_ context.Context, _ model.ActorRef, _ model.RunHandoffJoinFilter) (model.RunHandoffJoinPage, error) {
	return model.RunHandoffJoinPage{Results: []model.RunHandoffJoin{}}, nil
}

func TestGetRunTaskTreeBatchLoadsChildRuns(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	root := model.Run{RunID: "run-root", RootRunID: "run-root", Actor: actor, Status: model.RunStatusRunning}
	childOne := model.Run{RunID: "run-child-one", RootRunID: root.RunID, Actor: actor, Status: model.RunStatusCompleted}
	childTwo := model.Run{RunID: "run-child-two", RootRunID: root.RunID, Actor: actor, Status: model.RunStatusRunning}
	store := &agentRuntimeTestStore{
		runs: map[string]model.Run{root.RunID: root, childOne.RunID: childOne, childTwo.RunID: childTwo},
		handoffs: []model.RunHandoff{
			{HandoffID: "handoff-one", Actor: actor, RootRunID: root.RunID, ParentRunID: root.RunID, ChildRunID: childOne.RunID},
			{HandoffID: "handoff-missing", Actor: actor, RootRunID: root.RunID, ParentRunID: root.RunID, ChildRunID: testMissingRunID},
			{HandoffID: "handoff-two", Actor: actor, RootRunID: root.RunID, ParentRunID: root.RunID, ChildRunID: childTwo.RunID},
			{HandoffID: "handoff-one-reused", Actor: actor, RootRunID: root.RunID, ParentRunID: root.RunID, ChildRunID: childOne.RunID},
		},
	}
	engine := &Engine{repo: store}
	tree, err := engine.GetRunTaskTree(t.Context(), actor, root.RunID)
	if err != nil || tree.RootRun.RunID != root.RunID || len(tree.Tasks) != 3 {
		t.Fatalf("task tree = %+v, %v", tree, err)
	}
	if !slices.Equal(store.getRunCalls, []string{root.RunID}) {
		t.Fatalf("point run calls = %#v", store.getRunCalls)
	}
	if len(store.batchRunCalls) != 1 || !slices.Equal(store.batchRunCalls[0], []string{childOne.RunID, testMissingRunID, childTwo.RunID}) {
		t.Fatalf("batch run calls = %#v", store.batchRunCalls)
	}
}

func TestAgentExecutionBudgetOnlyNarrowsEnvironmentLimits(t *testing.T) {
	if got := narrowAgentBudget(12, 4); got != 4 {
		t.Fatalf("narrowed LLM budget = %d", got)
	}
	if got := narrowAgentBudget(8, 0); got != 8 {
		t.Fatalf("inherited tool budget = %d", got)
	}
	if got := narrowAgentBudget(8, 16); got != 8 {
		t.Fatalf("expanded tool budget = %d", got)
	}
	for _, test := range []struct {
		value, minimum, maximum int
		valid                   bool
	}{{0, 2, 32, true}, {2, 2, 32, true}, {32, 2, 32, true}, {1, 2, 32, false}, {33, 2, 32, false}} {
		if got := validAgentBudget(test.value, test.minimum, test.maximum); got != test.valid {
			t.Fatalf("validAgentBudget(%d) = %t", test.value, got)
		}
	}
}

func TestFinalizeRunHandoffDoesNotResumeTerminalParent(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	parent := model.Run{RunID: testAgentParentRunID, Actor: actor, Status: model.RunStatusCancelled, CurrentStepID: "step-parent"}
	child := model.Run{RunID: "run-child", Actor: actor, HandoffID: "handoff-1"}
	handoff := model.RunHandoff{
		HandoffID: "handoff-1", ParentRunID: parent.RunID, ChildRunID: child.RunID, Actor: actor,
		AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: testAgentReviewID, Revision: "1"}, AgentName: "Reviewer",
	}
	store := &agentRuntimeTestStore{
		runs: map[string]model.Run{parent.RunID: parent},
		handoffCompletion: model.RunHandoffCompletionResult{
			Handoff:       handoff,
			ResolvedJoins: []model.RunHandoffJoin{{JoinID: testJoinReadyID, ParentRunID: parent.RunID, Status: model.RunHandoffJoinStatusReady}},
		},
	}
	engine := &Engine{repo: store}
	parentRunID, events, err := engine.finalizeRunHandoff(t.Context(), child, model.TerminalIntent{Outcome: model.TerminalCancelled}, nil)
	if err != nil || parentRunID != parent.RunID || len(events) != 1 || len(store.appendedRunEvents) != 1 {
		t.Fatalf("terminal parent handoff result = parent=%q events=%+v appended=%+v err=%v", parentRunID, events, store.appendedRunEvents, err)
	}
	if events[0].EventType != "handoff.cancelled" {
		t.Fatalf("terminal parent event = %+v", events[0])
	}
}

func (s *agentRuntimeTestStore) CompleteRunHandoffWithJoins(_ context.Context, _ model.ActorRef, _ string, _ model.RunHandoffCompletion) (model.RunHandoffCompletionResult, error) {
	return s.handoffCompletion, nil
}

func (s *agentRuntimeTestStore) AppendRunEvent(_ context.Context, event *model.Event) (*model.Event, bool, error) {
	if event == nil {
		return nil, false, ErrInvalidInput
	}
	item := *event
	s.appendedRunEvents = append(s.appendedRunEvents, item)
	return &item, true, nil
}

func TestCancelRunHandoffJoinIsMonotonic(t *testing.T) {
	now := time.Now().UTC()
	pending := model.RunHandoffJoin{JoinID: "join-1", Status: model.RunHandoffJoinStatusPending}
	cancelled := model.CancelRunHandoffJoin(pending, now, "parent_run_cancelled", "parent cancelled")
	if cancelled.Status != model.RunHandoffJoinStatusCancelled || cancelled.ResolvedAt == nil || !cancelled.ResolvedAt.Equal(now) || cancelled.ErrorCode != "parent_run_cancelled" {
		t.Fatalf("cancelled join = %+v", cancelled)
	}
	ready := model.RunHandoffJoin{JoinID: testJoinReadyID, Status: model.RunHandoffJoinStatusReady}
	if result := model.CancelRunHandoffJoin(ready, now, "ignored", "ignored"); result.Status != model.RunHandoffJoinStatusReady || result.ErrorCode != "" {
		t.Fatalf("terminal join changed = %+v", result)
	}
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
	s.getRunCalls = append(s.getRunCalls, runID)
	run, ok := s.runs[runID]
	if !ok || run.Actor != actor {
		return nil, ErrNotFound
	}
	return &run, nil
}

func (s *agentRuntimeTestStore) GetRunsByIDs(_ context.Context, actor model.ActorRef, runIDs []string) ([]model.Run, error) {
	s.batchRunCalls = append(s.batchRunCalls, append([]string(nil), runIDs...))
	result := make([]model.Run, 0, len(runIDs))
	for _, runID := range runIDs {
		if run, ok := s.runs[runID]; ok && run.Actor == actor {
			result = append(result, run)
		}
	}
	return result, nil
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
		SkillRefs: []model.ResourceRef{{Kind: ResourceKindSkill, ID: "research"}}, MaxChildRuns: 2, MaxDepth: 3, MaxLLMCalls: 4, MaxToolCalls: 6,
	}
	instructions, snapshot := textRunAgentManifest(&manifest, "Environment rules")
	if snapshot == nil || snapshot.Ref.Revision != "3" || snapshot.Name != manifest.Name || len(snapshot.ToolKeys) != 1 || snapshot.MaxLLMCalls != 4 || snapshot.MaxToolCalls != 6 {
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
	parent := model.Run{
		RunID: testAgentParentRunID, Actor: actor, Status: model.RunStatusRunning,
		RunConfigSnapshotJSON: mustRunJSON(effectiveTextRunConfig{
			SemanticVersion: RuntimeSnapshotVersion,
			AgentManifest:   &effectiveAgentManifest{MaxChildRuns: 1, MaxDepth: 1},
		}),
	}
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
