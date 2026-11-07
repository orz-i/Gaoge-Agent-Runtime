package harness

import (
	"context"
	"strings"
	"unicode/utf8"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// TranscriptHistoryFunc adapts a Host-owned reader of completed conversation
// history to TranscriptSource. The reader returns ordered, stable-ID Entries
// BEFORE the submitted Turn. Only pure text User/Assistant history is valid:
// system instructions, Tool calls/results, ProviderReplay and media need an
// explicitly composed TranscriptSource instead. The adapter validates all
// prior messages BEFORE Harness admits an immutable Turn, then appends the
// current User message using <turn.ID>/user as its deterministic source ID.
// The Host remains responsible for authentication, persistence, and appending
// completed replies.
//
// A retry of Prompt must present the same original historical submission.
// After the Host history advances, use SessionClient.Load for an older Turn.
type TranscriptHistoryFunc func(context.Context, HostRef, HostRef) ([]runtimecontext.Entry, error)

// Seed does not write to a transcript or load one from the Harness Store.
func (read TranscriptHistoryFunc) Seed(ctx context.Context, thread, turn HostRef, prompt string) (*ContextSeed, error) {
	if read == nil || !utf8.ValidString(turn.ID) || strings.TrimSpace(turn.ID) == "" || strings.TrimSpace(turn.ID) != turn.ID ||
		model.ValidateMessage(model.Message{Role: model.RoleUser, Content: model.TextParts(prompt)}) != nil {
		return nil, ErrInvalidRequest
	}
	entries, err := read(ctx, thread, turn)
	if err != nil {
		return nil, err
	}
	entries = runtimecontext.CloneEntries(entries)
	currentID := turn.ID + "/user"
	path := make([]string, 0, len(entries)+1)
	seenSources := make(map[string]struct{}, len(entries)+1)
	seenMessages := make(map[string]struct{}, len(entries)+1)
	for _, entry := range entries {
		sourceID, messageID := strings.TrimSpace(entry.SourceID), strings.TrimSpace(entry.ID)
		if !utf8.ValidString(sourceID) || !utf8.ValidString(messageID) ||
			!utf8.ValidString(entry.TurnID) || entry.TurnID != strings.TrimSpace(entry.TurnID) ||
			sourceID == "" || sourceID != entry.SourceID || sourceID == currentID ||
			messageID == "" || messageID != entry.ID || messageID == currentID ||
			(strings.TrimSpace(entry.TurnID) != "" && strings.TrimSpace(entry.TurnID) == turn.ID) ||
			!validTextHistoryMessage(entry.Message) {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := seenSources[sourceID]; duplicate {
			return nil, ErrInvalidRequest
		}
		if _, duplicate := seenMessages[messageID]; duplicate {
			return nil, ErrInvalidRequest
		}
		seenSources[sourceID] = struct{}{}
		seenMessages[messageID] = struct{}{}
		path = append(path, sourceID)
	}
	entries = append(entries, runtimecontext.Entry{
		ID: currentID, SourceID: currentID, TurnID: turn.ID, Required: true,
		Message: model.Message{Role: model.RoleUser, Content: model.TextParts(prompt)},
	})
	path = append(path, currentID)
	return &ContextSeed{SourcePath: path, Entries: entries}, nil
}

// validTextHistoryMessage enforces the same model message protocol used by
// Context, plus the deliberately narrower standard text Session contract.
// This runs before CreateTurn so invalid Host history cannot poison an
// otherwise reusable stable Turn ID through immutable admission hashing.
func validTextHistoryMessage(message model.Message) bool {
	if message.Role != model.RoleUser && message.Role != model.RoleAssistant {
		return false
	}
	if len(message.ToolCalls) != 0 || message.ToolCallID != "" || message.ProviderReplay != nil ||
		model.ValidateMessage(message) != nil {
		return false
	}
	content, textOnly := model.PlainText(message.Content)
	return textOnly && strings.TrimSpace(content) != ""
}
