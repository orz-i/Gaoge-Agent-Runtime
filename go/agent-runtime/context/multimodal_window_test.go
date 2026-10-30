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

func TestContextMultimodalLineageSealsOrderedSourcesWithoutLoss(t *testing.T) {
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	seed := openRequest("u1")
	seed.Entries[0].Message.Content = []model.ContentPart{
		{Kind: model.ContentText, Text: "Review both attachments in sequence"},
		{Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{
			Kind: model.MediaSourceURL, URL: "https://assets.example.test/one.png",
		}},
		{Kind: model.ContentFile, MediaType: "application/pdf", Source: &model.MediaSource{
			Kind: model.MediaSourceFileID, FileID: "file-a"},
		},
		{Kind: model.ContentText, Text: "Focus on the last page"},
	}
	checkpoint, err := manager.Open(t.Context(), seed)
	if err != nil {
		t.Fatal(err)
	}
	materialized := runtimectx.Materialize(checkpoint.Window)
	if len(materialized) < 2 || !reflect.DeepEqual(materialized[len(materialized)-1].Content, seed.Entries[0].Message.Content) {
		t.Fatalf("media was flattened, reordered or lost in checkpoint: %+v", materialized)
	}
	cloned := runtimectx.CloneCheckpoint(checkpoint)
	cloned.Window.Entries[0].Message.Content[1].Source.URL = "https://changed.example.test/two.png"
	if !runtimectx.ValidCheckpoint(checkpoint) || runtimectx.ValidCheckpoint(cloned) ||
		materialized[len(materialized)-1].Content[1].Source.URL != "https://assets.example.test/one.png" {
		t.Fatal("media source pointer leaked across Context checkpoint clone or corrupted lineage")
	}
}

func TestMediaContextHardTokenBudgetRequiresExactProviderCounter(t *testing.T) {
	request := model.Request{RunID: "media-run", Messages: []model.Message{{
		Role: model.RoleUser,
		Content: []model.ContentPart{
			{Kind: model.ContentText, Text: "Describe"},
			{Kind: model.ContentImage, MediaType: "image/jpeg", Source: &model.MediaSource{
				Kind: model.MediaSourceURL, URL: "https://assets.example.test/huge.jpg",
			}},
		},
	}}}
	window := runtimectx.ModelWindow{
		ContextTokens: 1000, ReservedOutputTokens: 100,
		TokenCountContext: runtimectx.TokenCountContext{Protocol: "openai_responses", Model: "gpt-6"},
	}
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	if _, err := manager.AssessModelRequest(t.Context(), request, window, runtimectx.Policy{}); !errors.Is(err, runtimectx.ErrModelWindowUnknown) {
		t.Fatalf("URL byte count incorrectly certified decoded media tokens: %v", err)
	}
	manager = runtimectx.NewManager(runtimectx.Dependencies{Counter: exactCounter{tokens: 120}})
	assessment, err := manager.AssessModelRequest(t.Context(), request, window, runtimectx.Policy{})
	if err != nil || assessment.TokenCountSource != runtimectx.CountExact || assessment.HardTokenEstimate != 120 {
		t.Fatalf("trusted exact Provider counter was rejected: %+v err=%v", assessment, err)
	}
	// A plain text-only message still supports the prior conservative
	// serial-byte fallback; only media inputs need exact accounting.
	request.Messages[0].Content = model.TextParts(strings.Repeat("x", 14))
	if _, err := runtimectx.NewManager(runtimectx.Dependencies{}).
		AssessModelRequest(t.Context(), request, window, runtimectx.Policy{}); err != nil {
		t.Fatalf("text-only byte upper-bound was unnecessarily removed: %v", err)
	}
}

// External JSON payloads must be decoded to the canonical model.Request
// before token admission. Arbitrary map/struct JSON must never go directly to
// a generic counting API and bypass detection of remote media references.
func TestMediaTokenAdmissionSerializedPayloadRequiresTypedModelRequest(t *testing.T) {
	media := model.Message{Role: model.RoleUser, Content: []model.ContentPart{{
		Kind: model.ContentImage, MediaType: "image/jpeg", Source: &model.MediaSource{
			Kind: model.MediaSourceURL, URL: "https://assets.example.test/large.jpg",
		},
	}}}
	window := runtimectx.ModelWindow{ContextTokens: 4096,
		TokenCountContext: runtimectx.TokenCountContext{Protocol: "openai_responses", Model: "gpt-test"},
	}
	inputs := []struct {
		name  string
		value any
	}{
		{name: "map", value: map[string]any{"runID": "media", "messages": []model.Message{media}}},
		{name: "struct", value: struct {
			RunID    string          `json:"runID"`
			Messages []model.Message `json:"messages"`
		}{RunID: "media", Messages: []model.Message{media}}},
	}
	for _, input := range inputs {
		t.Run(input.name, func(t *testing.T) {
			encoded, err := json.Marshal(input.value)
			if err != nil {
				t.Fatal(err)
			}
			var request model.Request
			if err := json.Unmarshal(encoded, &request); err != nil {
				t.Fatal(err)
			}
			if len(request.Messages) != 1 || model.ValidateMessage(request.Messages[0]) != nil {
				t.Fatalf("typed media request not preserved: %+v", request)
			}
			manager := runtimectx.NewManager(runtimectx.Dependencies{})
			if assessment, err := manager.AssessModelRequest(t.Context(), request, window, runtimectx.Policy{}); !errors.Is(err, runtimectx.ErrModelWindowUnknown) {
				t.Fatalf("unmetered media admitted from %s: assessment=%+v err=%v", input.name, assessment, err)
			}
			manager = runtimectx.NewManager(runtimectx.Dependencies{Counter: exactCounter{tokens: 220}})
			assessment, err := manager.AssessModelRequest(t.Context(), request, window, runtimectx.Policy{})
			if err != nil || assessment.TokenCountSource != runtimectx.CountExact || assessment.HardTokenEstimate != 220 {
				t.Fatalf("exactly metered media rejected: assessment=%+v err=%v", assessment, err)
			}
		})
	}
}

// An archived old user attachment is durable evidence, not system guidance.
// Rolling over a long conversation must retain exact ordered media inside the
// immutable artifact while preventing source URLs and provider IDs from being
// promoted into a synthetic System/Context extract.
func TestPortableMultimodalArchivePreservesMediaWithoutPromotingUserSources(t *testing.T) {
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	seed := openRequest("u1", "a1", "u2")
	seed.Entries[0].Message.Content = []model.ContentPart{
		{Kind: model.ContentText, Text: "inspect the attached historical document"},
		{Kind: model.ContentFile, MediaType: "application/pdf", Source: &model.MediaSource{
			Kind: model.MediaSourceFileID, FileID: "file-private-old-attachment",
		}},
		{Kind: model.ContentImage, MediaType: "image/jpeg", Source: &model.MediaSource{
			Kind: model.MediaSourceURL, URL: "https://assets.example.test/private-old.jpg",
		}},
	}
	original := model.CloneContentParts(seed.Entries[0].Message.Content)
	checkpoint, err := manager.Open(t.Context(), seed)
	if err != nil {
		t.Fatal(err)
	}
	compacted, err := manager.CompactPortable(runtimectx.PortableCompactionRequest{
		Previous: checkpoint,
		RunID:    "media-rollover",
		Messages: runtimectx.Materialize(checkpoint.Window),
		Policy:   runtimectx.Policy{PreserveRecentTurns: 1, MaxCompactionTokens: 256},
	})
	if err != nil || len(compacted.Artifacts) == 0 || len(compacted.Window.Entries) < 2 {
		t.Fatalf("media archive was not durable: compacted=%+v err=%v", compacted, err)
	}
	var archived []model.Message
	for _, artifact := range compacted.Artifacts {
		var part []model.Message
		if err := json.Unmarshal(artifact.ContentJSON, &part); err != nil {
			t.Fatal(err)
		}
		archived = append(archived, part...)
	}
	if len(archived) == 0 || !reflect.DeepEqual(archived[0].Content, original) {
		t.Fatalf("historical attachment blocks changed in durable archive: %+v", archived)
	}
	for _, message := range runtimectx.Materialize(compacted.Window) {
		if message.Role != model.RoleSystem && message.Role != model.RoleUser {
			continue
		}
		text, textOnly := model.PlainText(message.Content)
		if !textOnly || strings.Contains(text, "file-private-old-attachment") ||
			strings.Contains(text, "https://assets.example.test/private-old.jpg") {
			t.Fatalf("private historical media refs promoted into active Context: %+v", message)
		}
	}
}
