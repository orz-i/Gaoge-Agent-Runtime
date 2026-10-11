package http

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

func TestPublicDiagnosticProjectionRetainsLegitimateBusinessFailurePayload(t *testing.T) {
	t.Parallel()
	input := json.RawMessage(`{"result":{"error":{"code":"OUT_OF_STOCK","message":"No matching item"}},"items":[{"failure":{"reason":"invalid part"}}],"privateDebug":{"errorDetail":"Bearer sk-RAW-DIAGNOSTIC"}}`)
	public := PublicDiagnosticPayload(input)
	var object map[string]any
	if err := json.Unmarshal(public, &object); err != nil {
		t.Fatal(err)
	}
	result, ok := object["result"].(map[string]any)
	if !ok {
		t.Fatalf("normal result lost: %s", public)
	}
	failure, ok := result["error"].(map[string]any)
	if !ok || failure["code"] != "OUT_OF_STOCK" {
		t.Fatalf("business error lost: %s", public)
	}
	items, ok := object["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("business list lost: %s", public)
	}
	item, ok := items[0].(map[string]any)
	if !ok || item["failure"] == nil {
		t.Fatalf("business failure lost: %s", public)
	}
	if _, exists := object["privateDebug"]; exists {
		t.Fatalf("private diagnostic container must not be exposed: %s", public)
	}
}

func TestPublicWorkbenchRehashesChangedVisibleProjection(t *testing.T) {
	t.Parallel()
	detail := workbench.Detail{
		Run:      kernel.Run{ErrorCode: "model.upstream_auth_failed", ErrorDetail: "Bearer sk-PROVIDER"},
		Overview: workbench.Overview{ErrorCode: "model.upstream_auth_failed", ErrorDetail: "Bearer sk-PROVIDER"},
		Sections: []workbench.Section{{Name: "result", Available: true, Hash: "unredacted-section", Content: json.RawMessage(`{"value":1,"errorDetail":"Bearer sk-PROVIDER"}`)}},
		Topology: &workbench.TopologyV1{SchemaVersion: 1, RootNodeID: "node-1", Revision: 1, Hash: "unredacted-topology", Nodes: []workbench.TopologyNode{{ID: "node-1", Kind: "model", Label: "Generation", Status: "failed", Data: json.RawMessage(`{"value":1,"errorDetail":"Bearer sk-PROVIDER"}`)}}},
		Hash:     "unredacted-detail",
	}
	public := workbenchResponse(detail)
	if public.Sections[0].Hash == detail.Sections[0].Hash || public.Topology.Hash == detail.Topology.Hash || public.Hash == detail.Hash {
		t.Fatalf("stale hashes after redaction: %s %s %s", public.Sections[0].Hash, public.Topology.Hash, public.Hash)
	}
	if want := testPublicHash(t, public.Sections[0].Content); public.Sections[0].Hash != want {
		t.Fatalf("section hash %s, want %s", public.Sections[0].Hash, want)
	}
	topology := *public.Topology
	topology.Hash = ""
	if want := testPublicHash(t, topology); public.Topology.Hash != want {
		t.Fatalf("topology hash %s, want %s", public.Topology.Hash, want)
	}
	material := public
	material.Hash = ""
	if want := testPublicHash(t, material); public.Hash != want {
		t.Fatalf("workbench hash %s, want %s", public.Hash, want)
	}
	if detail.Hash != "unredacted-detail" || detail.Topology.Hash != "unredacted-topology" {
		t.Fatal("public projection mutated original Workbench")
	}
}

func testPublicHash(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func TestPublicWorkflowStateKeepsFailureStatusAndCodeButNotPrivateDetail(t *testing.T) {
	t.Parallel()
	snapshot := kernel.Snapshot{
		Run:   kernel.Run{ID: "wf-1", Kind: "workflow", Status: kernel.RunStatusFailed, ErrorCode: "workflow.effect_failed", ErrorDetail: "Bearer sk-PRIVATE-WORKFLOW"},
		State: json.RawMessage(`{"failure":{"status":"failed","code":"workflow.effect_failed","detail":"Bearer sk-PRIVATE-WORKFLOW"},"activations":[{"status":"failed","errorCode":"provider.Bearer-sk-PRIVATE-WORKFLOW","error":"Bearer sk-PRIVATE-WORKFLOW"}],"input":{"error":{"code":"ORDER_CONFLICT","message":"Business result"}}}`),
	}
	public := snapshotResponse(snapshot)
	if bytes.Contains(public.State, []byte("sk-PRIVATE-WORKFLOW")) {
		t.Fatalf("workflow owner sees private error in State: %s", public.State)
	}
	var state map[string]any
	if err := json.Unmarshal(public.State, &state); err != nil {
		t.Fatal(err)
	}
	failure, ok := state["failure"].(map[string]any)
	if !ok || failure["status"] != "failed" || failure["code"] != "execution.failed" || failure["detail"] != nil {
		t.Fatalf("failure intent facts were erased or unbounded: %s", public.State)
	}
	input, ok := state["input"].(map[string]any)
	if !ok || input["error"] == nil {
		t.Fatalf("legitimate input was lost: %s", public.State)
	}
}
