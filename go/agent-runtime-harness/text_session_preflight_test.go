package harness_test

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A malformed Host transcript must be refused before the immutable Harness
// Turn is ever created: otherwise the same stable Turn ID cannot be repaired
// after the Host corrects its source data.
func TestTextSessionHistoryPreflightRejectsInvalidContentBeforeAdmission(t *testing.T) {
	good := model.Message{Role: model.RoleUser, Content: model.TextParts("prior valid user text")}
	cases := []struct {
		name    string
		message model.Message
	}{
		{"invalid empty system", model.Message{Role: model.RoleSystem}},
		{"valid system is not conversation history", model.Message{Role: model.RoleSystem, Content: model.TextParts("injected system")}},
		{"valid assistant with no visible text", model.Message{Role: model.RoleAssistant}},
		{"assistant signed replay", model.Message{Role: model.RoleAssistant, Content: model.TextParts("hello"), ProviderReplay: &model.ProviderReplay{Provider: "openai", Format: "opaque"}}},
		{"assistant tool call", model.Message{Role: model.RoleAssistant, Content: model.TextParts("hello"), ToolCalls: []tools.Call{{ID: "call", ToolKey: "secret", Arguments: []byte("{}")}}}},
		{"orphan tool result", model.Message{Role: model.RoleTool, Content: model.TextParts("result"), ToolCallID: "call"}},
		{"user image", model.Message{Role: model.RoleUser, Content: []model.ContentPart{{Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://example.com/image.png"}}}}},
		{"user file", model.Message{Role: model.RoleUser, Content: []model.ContentPart{{Kind: model.ContentFile, MediaType: "application/pdf", Source: &model.MediaSource{Kind: model.MediaSourceFileID, FileID: "file_test"}}}}},
		{"user audio", model.Message{Role: model.RoleUser, Content: []model.ContentPart{{Kind: model.ContentAudio, MediaType: "audio/wav", Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://example.com/audio.wav"}}}}},
		{"invalid empty user", model.Message{Role: model.RoleUser}},
		{"invalid role", model.Message{Role: "developer", Content: model.TextParts("secret")}},
		{"invalid content kind", model.Message{Role: model.RoleUser, Content: []model.ContentPart{{Kind: "unknown", Text: "hello"}}}},
		{"invalid empty text part", model.Message{Role: model.RoleUser, Content: []model.ContentPart{{Kind: model.ContentText, Text: ""}}}},
		{"text with unexpected media", model.Message{Role: model.RoleUser, Content: []model.ContentPart{{Kind: model.ContentText, Text: "hello", Source: &model.MediaSource{Kind: model.MediaSourceFileID, FileID: "private"}}}}},
		{"user tool call id", model.Message{Role: model.RoleUser, Content: model.TextParts("hello"), ToolCallID: "fake"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kstore, hstore := memory.NewStore(), harness.NewMemoryStore()
			client := &textSessionModel{}
			history := []runtimecontext.Entry{{
				ID: "prior/user", SourceID: "prior/user", TurnID: "prior", Message: tc.message,
			}}
			source := harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
				return history, nil
			})
			options := textSessionOptions(mustTextRuntime(t, kstore), hstore, client, source)
			session, err := harness.OpenTextAgentSession(options)
			if err != nil {
				t.Fatal(err)
			}
			_, err = session.Prompt(t.Context(), "new-turn", "new prompt")
			if !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("invalid Host history admission: err=%v", err)
			}
			sessionID, err := harness.SessionID(options.Session.HostThread)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = hstore.GetSession(t.Context(), sessionID); !errors.Is(err, harness.ErrNotFound) {
				t.Fatalf("invalid Host history persisted Session: err=%v", err)
			}
			turnID, err := harness.TurnID(sessionID, harness.HostRef{Kind: "conversation_turn", ID: "new-turn"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = hstore.GetTurn(t.Context(), turnID); !errors.Is(err, harness.ErrNotFound) {
				t.Fatalf("invalid Host history persisted Turn: err=%v", err)
			}
			if len(client.requests) != 0 {
				t.Fatalf("invalid Host history caused physical model calls: %d", len(client.requests))
			}
			history[0].Message = good
			recovered, err := session.Prompt(t.Context(), "new-turn", "new prompt")
			if err != nil || recovered.Turn.Status != harness.TurnCompleted || len(client.requests) != 1 {
				t.Fatalf("corrected same Turn failed: status=%s err=%v model_calls=%d", recovered.Turn.Status, err, len(client.requests))
			}
			if len(client.requests[0].Messages) != 2 ||
				model.TextValue(client.requests[0].Messages[0].Content) != "prior valid user text" ||
				model.TextValue(client.requests[0].Messages[1].Content) != "new prompt" {
				t.Fatalf("unexpected corrected model context: %+v", client.requests[0].Messages)
			}
		})
	}
}

func TestTextSessionHistoryPreflightRetainsMultiPartTextAndRejectsOversizedContent(t *testing.T) {
	source := harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
		return []runtimecontext.Entry{{
			ID: "prior/a", SourceID: "prior/a", TurnID: "prior",
			Message: model.Message{Role: model.RoleAssistant, Content: []model.ContentPart{
				{Kind: model.ContentText, Text: "first"},
				{Kind: model.ContentText, Text: " second"},
			}},
		}}, nil
	})
	modelClient := &textSessionModel{}
	options := textSessionOptions(mustTextRuntime(t, memory.NewStore()), harness.NewMemoryStore(), modelClient, source)
	session, err := harness.OpenTextAgentSession(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Prompt(t.Context(), "next", "hi")
	if err != nil || result.Turn.Status != harness.TurnCompleted || len(modelClient.requests) != 1 {
		t.Fatalf("multipart text: status=%s err=%v calls=%d", result.Turn.Status, err, len(modelClient.requests))
	}
	if first := modelClient.requests[0].Messages[0]; first.Role != model.RoleAssistant ||
		len(first.Content) != 2 || first.Content[0].Text != "first" || first.Content[1].Text != " second" {
		t.Fatalf("multipart text lost its original ordered content: %+v", first)
	}

	parts := make([]model.ContentPart, 65)
	for i := range parts {
		parts[i] = model.ContentPart{Kind: model.ContentText, Text: "x"}
	}
	bad := harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
		return []runtimecontext.Entry{{ID: "prior/u", SourceID: "prior/u", Message: model.Message{Role: model.RoleUser, Content: parts}}}, nil
	})
	badOptions := textSessionOptions(mustTextRuntime(t, memory.NewStore()), harness.NewMemoryStore(), &textSessionModel{}, bad)
	badSession, err := harness.OpenTextAgentSession(badOptions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badSession.Prompt(t.Context(), "turn", "hello"); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("oversized multipart history accepted: %v", err)
	}
}
