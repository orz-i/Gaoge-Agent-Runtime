package agentruntime

import (
	"errors"
	"strings"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const testAgentTeamDuplicateMember = "same"

func TestNormalizeStartAgentTeamInputFreezesJoinDefaults(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	input, err := normalizeStartAgentTeamInput(StartAgentTeamInput{
		ClientTeamID: "writer-room-1",
		Coordinator: StartTextRunInput{
			Actor: actor, Thread: model.ThreadRef{Kind: threadKindConversation, ID: "conversation-1"},
			Environment: model.ResourceRef{Kind: "environment", ID: "1"}, Goal: "Integrate the team results",
			AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: "agent-lead"},
		},
		Members: []AgentTeamMemberInput{
			{MemberID: "architect", AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: "agent-architect"}, Goal: "Audit structure"},
			{MemberID: "continuity", AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: "agent-continuity"}, Goal: "Audit continuity"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if input.Join.Mode != model.RunHandoffJoinModeAll || input.Join.Quorum != 1 || input.Join.FailurePolicy != model.RunHandoffJoinFailureCollect || input.Join.TimeoutSeconds != defaultHandoffJoinTimeoutSeconds || input.Join.TimeoutPolicy != model.RunHandoffJoinTimeoutCancelPending {
		t.Fatalf("normalized join = %#v", input.Join)
	}
	if input.Members[0].ContentType != valueText6CED98CE || input.Members[1].ContentType != valueText6CED98CE {
		t.Fatalf("member content types = %#v", input.Members)
	}
}

func TestAgentTeamStartFailurePreservesStageAndCause(t *testing.T) {
	engine := &Engine{}
	cause := ErrRunToolUnavailable
	err := engine.agentTeamStartFailure(
		StartAgentTeamInput{ClientTeamID: "team-stage"},
		AgentTeamStartStageMemberRun,
		"architect",
		cause,
	)
	var failure *AgentTeamStartError
	if !errors.As(err, &failure) || !errors.Is(err, cause) {
		t.Fatalf("failure=%#v err=%v", failure, err)
	}
	if failure.Stage != AgentTeamStartStageMemberRun || failure.MemberID != "architect" || !strings.Contains(err.Error(), "architect") {
		t.Fatalf("failure=%#v err=%v", failure, err)
	}
}

func TestNormalizeStartAgentTeamInputRejectsDuplicateMembers(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	_, err := normalizeStartAgentTeamInput(StartAgentTeamInput{
		ClientTeamID: "team",
		Coordinator: StartTextRunInput{
			Actor: actor, Thread: model.ThreadRef{Kind: threadKindConversation, ID: "conversation-1"},
			Environment: model.ResourceRef{Kind: "environment", ID: "1"}, Goal: "Coordinate",
			AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: "agent-lead"},
		},
		Members: []AgentTeamMemberInput{
			{MemberID: testAgentTeamDuplicateMember, AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: "agent-one"}, Goal: "First"},
			{MemberID: testAgentTeamDuplicateMember, AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: "agent-two"}, Goal: "Second"},
		},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate member error = %v", err)
	}
}

func TestAgentTeamClientPartIDsAreStableAndScoped(t *testing.T) {
	actor := model.ActorRef{TenantID: testAgentTenantID, ActorID: testAgentActorID}
	first := agentTeamClientPartID("team_member", actor, "team-1", "architect")
	replayed := agentTeamClientPartID("team_member", actor, "team-1", "architect")
	otherMember := agentTeamClientPartID("team_member", actor, "team-1", "continuity")
	otherActor := agentTeamClientPartID("team_member", model.ActorRef{TenantID: testAgentTenantID, ActorID: "other"}, "team-1", "architect")
	if first != replayed || first == otherMember || first == otherActor || len(first) > 64 {
		t.Fatalf("team ids = %q replay=%q otherMember=%q otherActor=%q", first, replayed, otherMember, otherActor)
	}
}

func TestRunCanEnterHandoffJoinWaitRequiresDeferredQueuedRoot(t *testing.T) {
	queued := model.Run{Status: model.RunStatusQueued, RunConfigSnapshotJSON: mustRunJSON(effectiveTextRunConfig{SemanticVersion: RuntimeSnapshotVersion, InitialContinuationDeferred: true})}
	if !runCanEnterHandoffJoinWait(queued) {
		t.Fatal("deferred queued root must enter Handoff Join wait")
	}
	queued.RunConfigSnapshotJSON = mustRunJSON(effectiveTextRunConfig{SemanticVersion: RuntimeSnapshotVersion})
	if runCanEnterHandoffJoinWait(queued) {
		t.Fatal("ordinary queued root must not enter Handoff Join wait")
	}
	if !runCanEnterHandoffJoinWait(model.Run{Status: model.RunStatusRunning}) {
		t.Fatal("running root must enter Handoff Join wait")
	}
}

func TestParentDelegationLimitsUseCoordinatorSnapshot(t *testing.T) {
	parent := model.Run{RunConfigSnapshotJSON: mustRunJSON(effectiveTextRunConfig{
		SemanticVersion: RuntimeSnapshotVersion,
		AgentManifest:   &effectiveAgentManifest{MaxChildRuns: 7, MaxDepth: 2},
	})}
	children, depth, err := parentDelegationLimits(parent)
	if err != nil || children != 7 || depth != 2 {
		t.Fatalf("parent limits = %d/%d, %v", children, depth, err)
	}
}
