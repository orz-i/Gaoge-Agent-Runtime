package harness_test

import (
	"context"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestTranscriptHistorySourceAppendsStableUserWithoutMutatingHostData(t *testing.T) {
	prior := []runtimecontext.Entry{{ID: "first/user", SourceID: "first/user", TurnID: "first", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("earlier")}}}
	reader := harness.TranscriptHistoryFunc(func(_ context.Context, thread, turn harness.HostRef) ([]runtimecontext.Entry, error) {
		if thread.ID != "thread" || turn.ID != "second" {
			t.Fatalf("unexpected read scope: thread=%+v turn=%+v", thread, turn)
		}
		return prior, nil
	})
	seed, err := reader.Seed(t.Context(), harness.HostRef{ID: "thread"}, harness.HostRef{ID: "second"}, "now")
	if err != nil || len(seed.Entries) != 2 || len(seed.SourcePath) != 2 {
		t.Fatalf("seed: %+v err=%v", seed, err)
	}
	if seed.SourcePath[0] != "first/user" || seed.SourcePath[1] != "second/user" ||
		seed.Entries[1].TurnID != "second" || !seed.Entries[1].Required ||
		seed.Entries[1].Message.Role != model.RoleUser || model.TextValue(seed.Entries[1].Message.Content) != "now" {
		t.Fatalf("invalid user append: %+v", seed)
	}
	seed.Entries[0].Message.Content[0].Text = "tampered"
	seed.Entries[0].SourceID = "tampered"
	if prior[0].SourceID != "first/user" || model.TextValue(prior[0].Message.Content) != "earlier" {
		t.Fatal("seed aliased the host transcript")
	}
}

func TestTranscriptHistorySourceRejectsInvalidUTF8IdentitiesAndPrompt(t *testing.T) {
	makeEntry := func() runtimecontext.Entry {
		return runtimecontext.Entry{
			ID: "prior/id", SourceID: "prior/source", TurnID: "prior",
			Message: model.Message{Role: model.RoleUser, Content: model.TextParts("valid history")},
		}
	}
	thread := harness.HostRef{Kind: "conversation", ID: "thread"}
	turn := harness.HostRef{Kind: "conversation_turn", ID: "new"}
	cases := []struct {
		name   string
		mutate func(*runtimecontext.Entry, *harness.HostRef)
	}{
		{"entry ID", func(e *runtimecontext.Entry, _ *harness.HostRef) { e.ID = "bad-\xff" }},
		{"source ID", func(e *runtimecontext.Entry, _ *harness.HostRef) { e.SourceID = "bad-\xfe" }},
		{"prior turn ID", func(e *runtimecontext.Entry, _ *harness.HostRef) { e.TurnID = "bad-\xe2\x82" }},
		{"noncanonical prior turn ID", func(e *runtimecontext.Entry, _ *harness.HostRef) { e.TurnID = " prior " }},
		{"current turn ID", func(_ *runtimecontext.Entry, t *harness.HostRef) { t.ID = "bad-\xff" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry, current := makeEntry(), turn
			tc.mutate(&entry, &current)
			reader := harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
				return []runtimecontext.Entry{entry}, nil
			})
			if _, err := reader.Seed(t.Context(), thread, current, "valid prompt"); !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("malformed identity was accepted: %v", err)
			}
		})
	}
	reads := 0
	reader := harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
		reads++
		return []runtimecontext.Entry{makeEntry()}, nil
	})
	if _, err := reader.Seed(t.Context(), thread, turn, "bad \xff"); !errors.Is(err, harness.ErrInvalidRequest) || reads != 0 {
		t.Fatalf("invalid prompt bypassed direct source preflight: err=%v reads=%d", err, reads)
	}
}

func TestTranscriptHistorySourceRejectsCollisionsAndPropagatesErrors(t *testing.T) {
	thread, turn := harness.HostRef{ID: "thread"}, harness.HostRef{ID: "second"}
	cases := []struct {
		name    string
		entries []runtimecontext.Entry
	}{
		{"missing SourceID", []runtimecontext.Entry{{ID: "x"}}},
		{"missing Entry ID", []runtimecontext.Entry{{SourceID: "x"}}},
		{"mutable SourceID", []runtimecontext.Entry{{ID: "x", SourceID: " x"}}},
		{"duplicate SourceID", []runtimecontext.Entry{{ID: "a", SourceID: "x"}, {ID: "b", SourceID: "x"}}},
		{"duplicate message ID", []runtimecontext.Entry{{ID: "same", SourceID: "x"}, {ID: "same", SourceID: "y"}}},
		{"current user message ID collision", []runtimecontext.Entry{{ID: "second/user", SourceID: "other-source"}}},
		{"current user collision", []runtimecontext.Entry{{ID: "current", SourceID: "second/user"}}},
		{"current Turn already in history", []runtimecontext.Entry{{ID: "old", SourceID: "older", TurnID: "second"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
				return tc.entries, nil
			})
			if _, err := source.Seed(t.Context(), thread, turn, "prompt"); !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("invalid history accepted: %v", err)
			}
		})
	}
	var absent harness.TranscriptHistoryFunc
	if _, err := absent.Seed(t.Context(), thread, turn, "prompt"); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("nil reader: %v", err)
	}
	source := harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
		return nil, harness.ErrNotFound
	})
	if _, err := source.Seed(t.Context(), thread, turn, "prompt"); !errors.Is(err, harness.ErrNotFound) {
		t.Fatalf("reader error swallowed: %v", err)
	}
	if _, err := source.Seed(t.Context(), thread, turn, " "); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("empty prompt read: %v", err)
	}
	if _, err := source.Seed(t.Context(), thread, harness.HostRef{}, "prompt"); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("empty turn ID read: %v", err)
	}
	if _, err := source.Seed(t.Context(), thread, harness.HostRef{ID: " second "}, "prompt"); !errors.Is(err, harness.ErrInvalidRequest) {
		t.Fatalf("noncanonical turn ID read: %v", err)
	}
}
