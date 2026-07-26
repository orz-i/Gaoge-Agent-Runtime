package http

import (
	"encoding/json"
	"strings"
	"testing"

	runtime "github.com/orz-i/Gaoge/sdk/go/agent-runtime"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	testDelegationRootRunID = "run-root"
	testParentRunIDField    = "parentRunID"
)

const (
	projectionContractEnvironmentID = "env-1"
	projectionContractEnvironment   = "environment"
	projectionContractSkillKind     = "skill"
)

func TestRunResponseUsesNeutralThreadProjection(t *testing.T) {
	response := toRunResponse(model.Run{RunID: "run_1", Actor: model.ActorRef{TenantID: "tenant", ActorID: "42"}, Thread: model.ThreadRef{Kind: "host.thread", ID: "thread_public"}})
	thread, ok := response[valueThread].(map[string]string)
	if !ok || thread[valueKind72883EFB] != "host.thread" || thread["id"] != "thread_public" {
		t.Fatalf("thread projection = %#v", response[valueThread])
	}
	actor, ok := response["actor"].(map[string]string)
	if !ok || actor["id"] != "42" {
		t.Fatalf("actor projection = %#v", response["actor"])
	}
	if _, leaked := response["conversationID"]; leaked {
		t.Fatal("Run-first DTO leaked host numeric ID")
	}
	if response["schemaVersion"] != 1 {
		t.Fatalf("schemaVersion = %#v", response["schemaVersion"])
	}
}

func TestRunResponseIncludesRootAgentIdentityWithoutFakeLineage(t *testing.T) {
	root := toRunResponse(model.Run{
		RunID: testDelegationRootRunID, Actor: model.ActorRef{TenantID: "tenant", ActorID: "42"}, Thread: model.ThreadRef{Kind: "host.thread", ID: "thread-public"},
		AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: "agent-lead", Revision: "5"}, AgentName: "Lead",
	})
	rootRef, ok := root["agentManifestRef"].(resourceRefDTO)
	if !ok || rootRef.ID != "agent-lead" || rootRef.Revision != "5" || root["agentName"] != "Lead" {
		t.Fatalf("root agent identity = %#v", root)
	}
	for _, key := range []string{testParentRunIDField, "handoffID"} {
		if _, exists := root[key]; exists {
			t.Fatalf("root run leaked lineage %q: %#v", key, root)
		}
	}
}

func TestRunResponseIncludesDelegationLineageForChildRun(t *testing.T) {
	child := toRunResponse(model.Run{
		RunID: "run-child", Actor: model.ActorRef{TenantID: "tenant", ActorID: "42"}, Thread: model.ThreadRef{Kind: "host.thread", ID: "thread-public"},
		AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: "agent-research", Revision: "2"}, AgentName: "Research",
		RootRunID: testDelegationRootRunID, ParentRunID: testDelegationRootRunID, HandoffID: "handoff-1", Depth: 1,
	})
	if child["agentName"] != "Research" || child["rootRunID"] != testDelegationRootRunID || child[testParentRunIDField] != testDelegationRootRunID || child["handoffID"] != "handoff-1" || child["depth"] != 1 {
		t.Fatalf("child lineage = %#v", child)
	}
	ref, ok := child["agentManifestRef"].(resourceRefDTO)
	if !ok || ref.ID != "agent-research" || ref.Revision != "2" {
		t.Fatalf("child manifest ref = %#v", child["agentManifestRef"])
	}
}

func TestNeutralReferenceResponsesUseLowerCamelCase(t *testing.T) {
	payload := map[string]interface{}{
		"thread":             threadRefResponse(model.ThreadRef{Kind: "host.thread", ID: "thread-1"}),
		"inputProjectionRef": projectionRefResponse(model.ProjectionRef{Kind: "host.projection", ID: "projection-1"}),
		"environmentRef":     resourceRefResponse(model.ResourceRef{Kind: projectionContractEnvironment, ID: projectionContractEnvironmentID, Revision: "3"}),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{`"Kind"`, `"ID"`, `"Revision"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("reference response leaked %s: %s", forbidden, text)
		}
	}
	for _, required := range []string{`"kind"`, `"id"`, `"revision"`} {
		if !strings.Contains(text, required) {
			t.Fatalf("reference response missing %s: %s", required, text)
		}
	}
}

func TestTextRunConfigResponseCanonicalizesNestedResourceRefs(t *testing.T) {
	payload := textRunConfigResponse(&runtime.TextRunConfigSummary{
		EnvironmentRef:       model.ResourceRef{Kind: projectionContractEnvironment, ID: projectionContractEnvironmentID, Revision: "4"},
		SkillRefs:            []model.ResourceRef{{Kind: projectionContractSkillKind, ID: "alpha"}},
		UnavailableSkillRefs: []model.ResourceRef{{Kind: projectionContractSkillKind, ID: "missing"}},
	})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{`"Kind"`, `"ID"`, `"Revision"`} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("config response leaked %s: %s", forbidden, text)
		}
	}
}

func TestCanonicalizeKnownRuntimeRefsRepairsFrozenPayloads(t *testing.T) {
	payload := map[string]interface{}{
		"environment": map[string]interface{}{"Kind": projectionContractEnvironment, "ID": projectionContractEnvironmentID, "Revision": "2"},
		"skillRefs": []interface{}{
			map[string]interface{}{"Kind": projectionContractSkillKind, "ID": "alpha", "Revision": ""},
		},
	}
	raw, err := json.Marshal(canonicalizeKnownRuntimeRefs(payload))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, `"Kind"`) || strings.Contains(text, `"ID"`) || strings.Contains(text, `"Revision"`) {
		t.Fatalf("frozen response leaked domain field casing: %s", text)
	}
}
