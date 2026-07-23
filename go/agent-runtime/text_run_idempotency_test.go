package agentruntime

import (
	"testing"

	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueFileA156CC0BC        = "file_a"
	valueGoal86AC966D         = "goal"
	valueMcp3A1498F66         = "mcp.3"
	valueStory6000957F        = "story"
	valueUsageUpdatedEF9AA839 = "usage.updated"
)

func TestTextRunRequestFingerprintIsStableForSetInputs(t *testing.T) {
	toolsA, toolsB := []string{valueMcp3A1498F66, "mcp.1", valueMcp3A1498F66}, []string{"mcp.1", valueMcp3A1498F66}
	skillsA, skillsB := []model.ResourceRef{{Kind: ResourceKindSkill, ID: "9"}, {Kind: ResourceKindSkill, ID: "4"}}, []model.ResourceRef{{Kind: ResourceKindSkill, ID: "4"}, {Kind: ResourceKindSkill, ID: "9"}}
	base := StartTextRunInput{
		Actor:             model.ActorRef{TenantID: valueTenantTest, ActorID: "actor_1"},
		Thread:            model.ThreadRef{Kind: threadKindConversation, ID: "thread_2"},
		Goal:              " investigate ",
		Environment:       model.ResourceRef{Kind: resourceKindEnvironment, ID: "environment_3", Revision: "1"},
		PlatformModelName: "model",
		Options:           map[string]interface{}{"temperature": 0.2},
		FileIDs:           []string{"file_b", valueFileA156CC0BC},
		ToolKeys:          &toolsA,
		SkillRefs:         &skillsA,
	}
	reordered := base
	reordered.FileIDs = []string{valueFileA156CC0BC, "file_b"}
	reordered.ToolKeys = &toolsB
	reordered.SkillRefs = &skillsB
	left := textRunRequestFingerprint(base, "investigate")
	right := textRunRequestFingerprint(reordered, "investigate")
	if left != right {
		t.Fatalf("semantic set reorder changed fingerprint: %s != %s", left, right)
	}
	reordered.Goal = "different"
	if left == textRunRequestFingerprint(reordered, "different") {
		t.Fatal("different request reused fingerprint")
	}
	withWorkspace := base
	selection := WorkspaceSelection{Kind: "unit", ID: "9"}
	withWorkspace.Workspace = &WorkspaceRequest{SchemaVersion: 7, Type: valueStory6000957F, Selection: &selection, ExpectedRevision: 4, Directive: &WorkspaceDirective{ArtifactContract: "reply"}}
	if left == textRunRequestFingerprint(withWorkspace, "investigate") {
		t.Fatal("workspace must participate in the run fingerprint")
	}
	changedRevision := withWorkspace
	copyWorkspace := *withWorkspace.Workspace
	copyWorkspace.ExpectedRevision = 5
	changedRevision.Workspace = &copyWorkspace
	if textRunRequestFingerprint(withWorkspace, "investigate") == textRunRequestFingerprint(changedRevision, "investigate") {
		t.Fatal("workspace revision must participate in the run fingerprint")
	}
}

func TestRunRealtimeEnvelopePreservesDurableSequence(t *testing.T) {
	event := &model.Event{EventID: "evt_42", RunID: "run_test", Thread: model.ThreadRef{Kind: threadKindConversation, ID: "thread_9"}, Seq: 42, EventType: valueUsageUpdatedEF9AA839, Summary: "Usage updated", Status: model.RunStatusCompleted, ToolCallID: "tool_42", ToolName: "example_tool", StartedAt: time.Now(), PayloadJSON: `{}`}
	envelope := runEventEnvelope(event)
	// The generation stream adds its own top-level transport seq.
	envelope["seq"] = int64(3)
	durable, ok := envelope["event"].(map[string]interface{})
	thread, threadOK := durable["thread"].(map[string]string)
	if !ok || durable["seq"] != int64(42) || !threadOK || thread["kind"] != threadKindConversation || thread["id"] != "thread_9" {
		t.Fatalf("durable event was overwritten by transport envelope: %#v", envelope)
	}
	payload, ok := durable["payload"].(map[string]interface{})
	if !ok || payload["summary"] != event.Summary || payload["status"] != event.Status || payload["toolCallID"] != event.ToolCallID || payload["toolName"] != event.ToolName {
		t.Fatalf("public event fields missing from realtime payload: %#v", durable)
	}
}

func TestBuildRunEventHistoryPageReturnsAscendingExclusiveCursor(t *testing.T) {
	page := buildRunEventHistoryPage([]model.Event{{Seq: 7}, {Seq: 6}, {Seq: 5}, {Seq: 4}}, 3)
	if !page.HasMore || page.NextBeforeSeq != 5 || len(page.Results) != 3 {
		t.Fatalf("unexpected history page: %#v", page)
	}
	if page.Results[0].Seq != 5 || page.Results[1].Seq != 6 || page.Results[2].Seq != 7 {
		t.Fatalf("history must be ascending without a boundary duplicate: %#v", page.Results)
	}
}

func TestTextRunFingerprintRejectsMissingImmutableFingerprint(t *testing.T) {
	run := &model.Run{Actor: model.ActorRef{TenantID: valueTenantTest, ActorID: "actor_5"}, Thread: model.ThreadRef{Kind: threadKindConversation, ID: "thread_6"}, Environment: model.ResourceRef{Kind: resourceKindEnvironment, ID: "environment_7", Revision: "1"}, Goal: valueGoal86AC966D}
	if textRunFingerprintMatches(run, "new-fingerprint") {
		t.Fatal("v3 run without its immutable request fingerprint must conflict")
	}
	run.RequestFingerprint = "new-fingerprint"
	if !textRunFingerprintMatches(run, "new-fingerprint") {
		t.Fatal("identical v3 request fingerprint should be idempotent")
	}
}
