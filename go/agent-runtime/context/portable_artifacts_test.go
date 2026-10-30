package context_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	runtimectx "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

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
