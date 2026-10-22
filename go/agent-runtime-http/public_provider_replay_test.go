package http

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workbench"
)

const secretSignedThought = "PRIVATE_THINKING_HTTP_BOUNDARY_TEST"
const secretSignedSignature = "PRIVATE_SIGNATURE_HTTP_BOUNDARY_TEST"
const secretEncryptedReasoning = "PRIVATE_ENCRYPTED_REASONING_HTTP_BOUNDARY_TEST"

// All providerReplay variants are provider-owned evidence, not authored data.
// In particular, nesting inside a business result must not make them public.
const replaySensitivePayload = `{
 "messages":[{"role":"assistant","content":"visible answer","providerReplay":{"provider":"anthropic","format":"messages.content.v1","data":[{"type":"thinking","thinking":"PRIVATE_THINKING_HTTP_BOUNDARY_TEST","signature":"PRIVATE_SIGNATURE_HTTP_BOUNDARY_TEST"}]}}],
 "modelInvocations":[{"request":{"messages":[{"role":"assistant","content":"previously visible","provider_replay":{"data":"PRIVATE_THINKING_HTTP_BOUNDARY_TEST"}}]},"response":{"content":"visible completion","providerReplay":{"provider":"openai","data":[{"type":"reasoning","encrypted_content":"PRIVATE_ENCRYPTED_REASONING_HTTP_BOUNDARY_TEST"}]}}}],
 "result":{"content":{"text":"visible result","provider-replay":{"data":"PRIVATE_SIGNATURE_HTTP_BOUNDARY_TEST"}}},
 "input":{"note":"visible input"},
 "ProviderReplay":{"data":"PRIVATE_THINKING_HTTP_BOUNDARY_TEST"},
 "unrelated":{"ok":true}
}`

func assertNoProviderReplay(t *testing.T, public []byte) {
	t.Helper()
	for _, phrase := range []string{secretSignedThought, secretSignedSignature, secretEncryptedReasoning,
		"providerReplay", "provider_replay", "provider-replay", "ProviderReplay"} {
		if bytes.Contains(public, []byte(phrase)) {
			t.Errorf("public projection leaked reserved provider evidence marker %q", phrase)
		}
	}
	for _, plain := range []string{"visible answer", "visible completion", "visible result", "visible input"} {
		if !bytes.Contains(public, []byte(plain)) {
			t.Errorf("public projection lost ordinary author-visible %q", plain)
		}
	}
}

func TestProviderReplayRemovedFromPublicStateAndNestedBusinessPayloads(t *testing.T) {
	t.Parallel()
	original := json.RawMessage(replaySensitivePayload)
	originalCopy := bytes.Clone(original)
	public := PublicDiagnosticPayload(original)
	assertNoProviderReplay(t, public)
	if !bytes.Equal(original, originalCopy) {
		t.Fatal("redaction changed authoritative Feature State")
	}
	snapshot := kernel.Snapshot{Run: kernel.Run{Kind: "agent", ID: "provider-security-audit"}, State: original}
	res := SnapshotResponse(snapshot)
	assertNoProviderReplay(t, res.State)
	if !bytes.Equal(snapshot.State, originalCopy) {
		t.Fatal("snapshot serialization mutated internal Kernel data")
	}
	// Re-projection must not reintroduce data.
	assertNoProviderReplay(t, PublicDiagnosticPayload(res.State))
}

func TestProviderReplayScrubbedFromAllPublicDiagnosticSurfaces(t *testing.T) {
	t.Parallel()
	payload := json.RawMessage(replaySensitivePayload)
	detail := workbench.Detail{
		Run:      kernel.Run{Kind: "agent", ID: "provider-security-audit"},
		Sections: []workbench.Section{{Name: "state", Available: true, Content: payload}},
		Timeline: []workbench.TimelineItem{{ID: "t1", Kind: "event", Data: payload}},
		Topology: &workbench.TopologyV1{
			Nodes: []workbench.TopologyNode{{ID: "n1", Kind: "model", Data: payload}},
			Edges: []workbench.TopologyEdge{{ID: "e1", Data: payload}},
		},
	}
	projected := publicWorkbench(detail)
	encoded, err := json.Marshal(projected)
	if err != nil {
		t.Fatal(err)
	}
	assertNoProviderReplay(t, encoded)
	if !bytes.Equal(detail.Sections[0].Content, payload) || !bytes.Equal(detail.Timeline[0].Data, payload) {
		t.Fatal("public Workbench scrubber mutated retained private diagnostic content")
	}
	event := runfeed.Event{RunID: "provider-security-audit", Type: runfeed.EventRunStarted, Data: payload}
	output, err := json.Marshal(publicRunFeedEvent(event))
	if err != nil {
		t.Fatal(err)
	}
	assertNoProviderReplay(t, output)
	if !bytes.Equal(event.Data, payload) {
		t.Fatal("public runfeed mutated private source event")
	}
}
