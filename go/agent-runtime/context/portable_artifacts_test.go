package context_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	runtimectx "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestPortableCompactionPreservesStructuredNumbers(t *testing.T) {
	t.Parallel()
	const original = `{"z":[0.10000000000000001,-0,1.00e+42], "n":9007199254740993}`
	const canonical = `{"n":9007199254740993,"z":[0.10000000000000001,-0,1.00e+42]}`
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	seed := openRequest("u1", "a1", "u2")
	seed.Entries[1].Message.ToolCalls = []tools.Call{{ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(original)}}
	seed.Entries = append(seed.Entries[:2:2], runtimectx.Entry{
		ID: "tool", SourceID: "tool", TurnID: "u1",
		Message: model.Message{Role: model.RoleTool, ToolCallID: "call", Content: model.TextParts(original)},
	}, seed.Entries[2])
	seed.SourcePath = []string{"u1", "a1", "tool", "u2"}
	checkpoint, err := manager.Open(t.Context(), seed)
	requireNoError(t, err)
	request := runtimectx.PortableCompactionRequest{Previous: checkpoint, RunID: "run", Messages: runtimectx.Materialize(checkpoint.Window),
		Policy: runtimectx.Policy{PreserveRecentTurns: 1}}
	compacted, err := manager.CompactPortable(request)
	requireNoError(t, err)
	requireEqual(t, len(compacted.Artifacts), 1, "archive count")
	artifact := compacted.Artifacts[0]
	requireTrue(t, runtimectx.ValidArtifact(artifact), "archive did not validate")
	requireEqual(t, artifact.Encoding, runtimectx.ArtifactEncodingLosslessJSON, "archive encoding")
	var archived []model.Message
	requireNoError(t, json.Unmarshal(artifact.ContentJSON, &archived))
	requireEqual(t, string(archived[1].ToolCalls[0].Arguments), canonical, "exact structured Tool arguments")
	requireEqual(t, model.TextValue(archived[2].Content), original, "unmodified Tool result text")
	requireEqual(t, compacted.Window.Entries[len(compacted.Window.Entries)-1].SourceID, "u2", "protected tail")

	for index := range request.Messages {
		if call := request.Messages[index].ToolCalls; len(call) != 0 {
			call[0].Arguments = json.RawMessage(strings.Replace(original, "9007199254740993", "9007199254740992", 1))
		}
	}
	if _, err := manager.CompactPortable(request); !errors.Is(err, runtimectx.ErrLineageConflict) {
		t.Fatalf("same-float replacement accepted as the original archive prefix: %v", err)
	}
}

func TestArtifactPreservesLegacyIdentityAndSeparatesLosslessNumbers(t *testing.T) {
	t.Parallel()
	// Fixed pre-change artifact wire vector (the already-stored integer is .992).
	const oldWire = `{"id":"ctxa_2f119ebde5b4c0251003ce55d788e38d","kind":"compaction","scopeID":"legacy-scope","generation":2,"sourceID":"legacy-source","contentJSON":{"n":9007199254740992,"z":[0.25,"🌱"]},"contentHash":"adcd04b4f9e62e8d30663de3b3fb2a987e5977c0f26a3bb6920e478ea8909560","tokenEstimate":14}`
	var old runtimectx.Artifact
	requireNoError(t, json.Unmarshal([]byte(oldWire), &old))
	requireTrue(t, runtimectx.ValidArtifact(old), "legacy artifact did not validate")
	reminted, err := runtimectx.NewArtifact(old.Kind, old.ScopeID, old.Generation, old.SourceID, "", json.RawMessage(`{"z":[0.25,"🌱"], "n":9007199254740992}`))
	requireNoError(t, err)
	wire, err := json.Marshal(reminted)
	requireNoError(t, err)
	requireEqual(t, string(wire), oldWire, "legacy wire, digest, and ID")

	changed := json.RawMessage(strings.Replace(string(old.ContentJSON), "9007199254740992", "9007199254740993", 1))
	forged := old
	forged.ContentJSON = changed
	requireTrue(t, !runtimectx.ValidArtifact(forged), "old digest authenticated a different rounded value")
	newArtifact, err := runtimectx.NewArtifact(old.Kind, old.ScopeID, old.Generation, old.SourceID, "", changed)
	requireNoError(t, err)
	requireTrue(t, newArtifact.ID != old.ID && newArtifact.ContentHash != old.ContentHash, "legacy and lossless identities alias")
	requireEqual(t, newArtifact.Encoding, runtimectx.ArtifactEncodingLosslessJSON, "lossless format")
	serialized, err := json.Marshal(newArtifact)
	requireNoError(t, err)
	requireTrue(t, strings.Contains(string(serialized), `"encoding":"lossless-json-v1"`), "wire omitted canonical encoding marker")
	var restored runtimectx.Artifact
	requireNoError(t, json.Unmarshal(serialized, &restored))
	requireTrue(t, reflect.DeepEqual(restored, newArtifact) && runtimectx.ValidArtifact(restored), "lossless artifact did not survive serialization")
	for _, marker := range []string{"", "future-encoding"} {
		changed := restored
		changed.Encoding = marker
		requireTrue(t, !runtimectx.ValidArtifact(changed), "missing/unknown format accepted")
	}
	unnecessary := reminted
	unnecessary.Encoding = runtimectx.ArtifactEncodingLosslessJSON
	requireTrue(t, !runtimectx.ValidArtifact(unnecessary), "legacy-safe content accepted a second encoding identity")
	wide, err := runtimectx.NewArtifact(old.Kind, old.ScopeID, old.Generation, old.SourceID, "", json.RawMessage(`{"n":1e400}`))
	requireNoError(t, err)
	requireTrue(t, runtimectx.ValidArtifact(wide), "standalone archive unnecessarily parsed the value as float64")
	requireEqual(t, string(wide.ContentJSON), `{"n":1e400}`, "original exponent")
}

func TestArtifactRejectsAmbiguousStructuredInput(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{`[{"n":1,"n":2}]`, `{"a":1,"A":2}`, `{"s":"\ud800"}`, `{} {}`} {
		if _, err := runtimectx.NewArtifact(runtimectx.ArtifactCompaction, "scope", 1, "source", "", json.RawMessage(payload)); !errors.Is(err, runtimectx.ErrInvalidInput) {
			t.Errorf("accepted or misclassified invalid archive %q: %v", payload, err)
		}
	}
}

func TestPortableCompactionSealsMultipleUnicodeArchivesAndRollsOver(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	seed := openRequest("u1", "a1", "u2", "a2", "u3")
	seed.Entries[0].Message.Content = model.TextParts("FIRST_ANCHOR\n" + strings.Repeat("🌱", 550_000))
	seed.Entries[2].Message.Content = model.TextParts("SECOND_ANCHOR\n" + strings.Repeat("Ħ", 1_100_000))
	checkpoint, err := manager.Open(t.Context(), seed)
	requireNoError(t, err)
	request := runtimectx.PortableCompactionRequest{
		Previous: checkpoint, RunID: "run-multipart", Messages: runtimectx.Materialize(checkpoint.Window),
		Policy: runtimectx.Policy{PreserveRecentTurns: 1, MaxCompactionTokens: 256},
	}
	compacted, err := manager.CompactPortable(request)
	requireNoError(t, err)
	requireEqual(t, len(compacted.Artifacts), 2, "multipart archive count")
	requireEqual(t, compacted.RemovedMessages, 4, "multipart removed messages")
	var restored []model.Message
	for _, artifact := range compacted.Artifacts {
		requireTrue(t, runtimectx.ValidArtifact(artifact), "invalid multipart artifact")
		requireTrue(t, len(artifact.ContentJSON) <= runtimectx.MaxArtifactPayloadBytes, "artifact exceeded durable ceiling")
		var part []model.Message
		requireNoError(t, json.Unmarshal(artifact.ContentJSON, &part))
		restored = append(restored, part...)
		requireTrue(t, containsAll(model.TextValue(compacted.Window.Entries[0].Message.Content), artifact.ID, artifact.ContentHash), "marker omitted archive identity")
	}
	restoredJSON, err := json.Marshal(restored)
	requireNoError(t, err)
	originalJSON, err := json.Marshal(request.Messages[1:5])
	requireNoError(t, err)
	if string(restoredJSON) != string(originalJSON) {
		t.Fatal("ordered artifacts did not restore the exact removed transcript")
	}
	active := runtimectx.Materialize(compacted.Window)
	requireTrue(t, reflect.DeepEqual(active[len(active)-1].Content, seed.Entries[4].Message.Content), "protected current input changed")
	requireEqual(t, compacted.Window.Entries[len(compacted.Window.Entries)-1].SourceID, "u3", "protected source identity lost")
	for _, message := range active {
		if message.Role == model.RoleSystem && strings.Contains(model.TextValue(message.Content), "FIRST_ANCHOR") {
			t.Fatal("untrusted archived text was promoted to system instructions")
		}
	}
	request.OmitExtract = true
	metadataOnly, err := manager.CompactPortable(request)
	requireNoError(t, err)
	requireEqual(t, len(metadataOnly.Window.Entries), 2, "metadata-only active entries")
	if !reflect.DeepEqual(compacted.Artifacts, metadataOnly.Artifacts) {
		t.Fatal("retry changed exact archive identities")
	}
	next, err := manager.Rollover(t.Context(), runtimectx.RolloverRequest{
		Previous: checkpoint, Window: compacted.Window, Artifacts: compacted.Artifacts, Reason: "soft_limit",
	})
	requireNoError(t, err)
	requireTrue(t, runtimectx.ValidCheckpoint(next), "multipart rollover checkpoint is invalid")
	requireEqual(t, next.Generation, 2, "multipart generation")
	requireEqual(t, len(next.ArtifactIDs), 2, "durable checkpoint omitted an archive")
}

func TestPortableCompactionRejectsDivergentSourcePrefix(t *testing.T) {
	t.Parallel()
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	checkpoint, err := manager.Open(t.Context(), openRequest("u1", "a1", "u2"))
	requireNoError(t, err)
	messages := runtimectx.Materialize(checkpoint.Window)
	messages[1].Content = model.TextParts("different source fact")
	_, err = manager.CompactPortable(runtimectx.PortableCompactionRequest{
		Previous: checkpoint, RunID: "run-divergent", Messages: messages,
		Policy: runtimectx.Policy{PreserveRecentTurns: 1},
	})
	if !errors.Is(err, runtimectx.ErrLineageConflict) {
		t.Fatalf("divergent source must not be archived under the checkpoint lineage: %v", err)
	}
}

func TestContextPolicyKeepsPreferredAndProtectedTailsDistinct(t *testing.T) {
	t.Parallel()
	defaults := runtimectx.NormalizePolicy(runtimectx.Policy{})
	requireEqual(t, defaults.PreserveRecentTurns, 8, "preferred tail default")
	requireEqual(t, defaults.MinimumRecentTurns, 1, "protected current turn default")
	explicit := runtimectx.NormalizePolicy(runtimectx.Policy{PreserveRecentTurns: 2, MinimumRecentTurns: 3})
	requireEqual(t, explicit.PreserveRecentTurns, 3, "preference must honor larger protected minimum")
	requireEqual(t, explicit.MinimumRecentTurns, 3, "explicit protected tail changed")
}

func TestMillionTokenInputBoundaryIncludesOutputReserve(t *testing.T) {
	t.Parallel()
	for _, count := range []int64{995_904, 995_905} {
		manager := runtimectx.NewManager(runtimectx.Dependencies{Counter: exactCounter{tokens: count}})
		assessment, err := manager.AssessModelRequest(t.Context(), model.Request{
			Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("counted boundary input")}},
		}, runtimectx.ModelWindow{
			ContextTokens: 1_000_000, ReservedOutputTokens: 4096,
			TokenCountContext: runtimectx.TokenCountContext{Protocol: "openai_responses", Model: "MiniMax-M3.1-Flash-Preview"},
		}, runtimectx.Policy{})
		requireNoError(t, err)
		requireEqual(t, assessment.HardInputTokens, int64(995_904), "million-token effective input limit")
		requireEqual(t, assessment.TokenCountSource, runtimectx.CountExact, "native count source")
		requireEqual(t, runtimectx.WithinHardBudget(assessment, runtimectx.Policy{}), count == 995_904, "hard input boundary admission")
	}
}

func TestPortableArchivePayloadBoundary(t *testing.T) {
	t.Parallel()
	for _, extra := range []int{0, 1} {
		t.Run(map[int]string{0: "exact-ceiling", 1: "oversized-message"}[extra], func(t *testing.T) {
			manager := runtimectx.NewManager(runtimectx.Dependencies{})
			seed := openRequest("u1", "a1", "u2")
			// Content is omitted when empty; account for its JSON field before filling it.
			seed.Entries[0].Message.Content = model.TextParts("x")
			oneJSON, marshalErr := json.Marshal(seed.Entries[0].Message)
			requireNoError(t, marshalErr)
			seed.Entries[0].Message.Content = model.TextParts(strings.Repeat("x", runtimectx.MaxArtifactPayloadBytes-len(oneJSON)+1-2+extra))
			checkpoint, openErr := manager.Open(t.Context(), seed)
			requireNoError(t, openErr)
			compacted, compactErr := manager.CompactPortable(runtimectx.PortableCompactionRequest{
				Previous: checkpoint, RunID: "run-boundary", Messages: runtimectx.Materialize(checkpoint.Window),
				Policy: runtimectx.Policy{PreserveRecentTurns: 1},
			})
			if extra == 1 {
				if !errors.Is(compactErr, runtimectx.ErrArtifactPayloadTooLarge) || len(compacted.Artifacts) != 0 {
					t.Fatalf("oversized single message must fail without partial archives: %v", compactErr)
				}
				return
			}
			requireNoError(t, compactErr)
			requireEqual(t, len(compacted.Artifacts), 2, "full first archive must force another archive")
			requireEqual(t, len(compacted.Artifacts[0].ContentJSON), runtimectx.MaxArtifactPayloadBytes, "exact ceiling bytes")
		})
	}
}
