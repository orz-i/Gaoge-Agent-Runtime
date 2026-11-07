package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// All fields that participate in sealed Config/Turn identity must be
// canonical BEFORE the first Store write, not only current/past message text.
// This enumerates distinct invalid bytes that encoding/json folds to U+FFFD.
func TestCanonicalUTF8ConfigIdentityMatrixRejectsBeforeHash(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*harness.ConfigSnapshot, string)
	}{
		{"instructions", func(c *harness.ConfigSnapshot, s string) { c.Instructions = "rules-" + s }},
		{"model", func(c *harness.ConfigSnapshot, s string) { c.Model = "fixture-" + s }},
		{"environment", func(c *harness.ConfigSnapshot, s string) {
			c.Environment = harness.VersionRef{ID: "environment-" + s, Revision: 1}
		}},
		{"model-options", func(c *harness.ConfigSnapshot, s string) {
			c.ModelOptions = json.RawMessage([]byte("{\"route\":\"" + s + "\"}"))
		}},
		{"input-reference", func(c *harness.ConfigSnapshot, s string) {
			c.InputMessage = &harness.HostRef{Kind: "message", ID: "input-" + s}
		}},
		{"output-reference", func(c *harness.ConfigSnapshot, s string) {
			c.OutputMessage = &harness.HostRef{Kind: "message", ID: "output-" + s}
		}},
		{"nested-tool-keys", func(c *harness.ConfigSnapshot, s string) {
			c.ToolKeys = []string{"name-" + s}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for n := 0x80; n <= 0xff; n++ {
				config := harness.ConfigSnapshot{Model: "fixture"}
				tc.mutate(&config, string([]byte{byte(n)}))
				if _, err := harness.SealConfigSnapshot("turn", config, time.Unix(1000, 0)); !errors.Is(err, harness.ErrInvalidRequest) {
					t.Fatalf("invalid byte %02x accepted in %s: %v", n, tc.name, err)
				}
			}
			// Real replacement rune is not itself an invalid UTF-8 sequence.
			valid := harness.ConfigSnapshot{Model: "fixture"}
			tc.mutate(&valid, "�")
			if _, err := harness.SealConfigSnapshot("turn", valid, time.Unix(1000, 0)); err != nil {
				t.Fatalf("valid replacement rune rejected in %s: %v", tc.name, err)
			}
		})
	}
}

func TestCanonicalUTF8ConfigJSONEscapesNeverAliasAtSeal(t *testing.T) {
	for _, bad := range []string{
		"{\"route\":\"\\ud800\"}",
		"{\"route\":\"\\udc00\"}",
		"{\"route\":\"\\ud800\\ud800\"}",
	} {
		config := harness.ConfigSnapshot{Model: "fixture", ModelOptions: json.RawMessage(bad)}
		if _, err := harness.SealConfigSnapshot("turn", config, time.Unix(1000, 0)); !errors.Is(err, harness.ErrInvalidRequest) {
			t.Fatalf("unpaired JSON Unicode escape sealed: %s err=%v", bad, err)
		}
	}
	valid := harness.ConfigSnapshot{
		Model:        "fixture",
		ModelOptions: json.RawMessage("{\"route\":\"\\ud83e\\udde9\"}"),
	}
	if _, err := harness.SealConfigSnapshot("turn", valid, time.Unix(1000, 0)); err != nil {
		t.Fatalf("valid surrogate pair rejected: %v", err)
	}
}

// A malformed current Session configuration or custom Context instruction
// must not persist a Session/Turn, must not invoke the Model, and must allow
// an authenticated Host to correct and submit the SAME stable Turn ID.
func TestCanonicalUTF8SessionAdmissionBeforeAnyModelEffect(t *testing.T) {
	for _, field := range []string{"config-instructions", "config-model", "context-instructions"} {
		t.Run(field, func(t *testing.T) {
			kstore, hstore := memory.NewStore(), harness.NewMemoryStore()
			client := &textSessionModel{}
			raw := "rule-" + string([]byte{0xff})
			newSession := func() (*harness.SessionClient, error) {
				reader := harness.TranscriptSourceFunc(func(_ context.Context, _, turn harness.HostRef, prompt string) (*harness.ContextSeed, error) {
					entry := runtimecontext.Entry{ID: turn.ID + "/user", SourceID: turn.ID + "/user",
						TurnID: turn.ID, Message: model.Message{Role: model.RoleUser, Content: model.TextParts(prompt)}}
					seed := &harness.ContextSeed{SourcePath: []string{entry.SourceID}, Entries: []runtimecontext.Entry{entry}}
					if field == "context-instructions" {
						seed.Instructions = raw
					}
					return seed, nil
				})
				opts := textSessionOptions(mustTextRuntime(t, kstore), hstore, client, reader)
				switch field {
				case "config-instructions":
					opts.Session.Config.Instructions = raw
				case "config-model":
					opts.Session.Config.Model = raw
				}
				return harness.OpenTextAgentSession(opts)
			}
			session, err := newSession()
			if err == nil {
				_, err = session.Prompt(t.Context(), "turn", "hello")
			}
			if !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("%s: malformed UTF-8 not rejected before admission: %v", field, err)
			}
			if len(client.requests) != 0 {
				t.Fatalf("%s: physical model effect on invalid submission: %d", field, len(client.requests))
			}
			sid, err := harness.SessionID(harness.HostRef{Kind: "conversation", ID: "tenant-a/chat-1"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := hstore.GetSession(t.Context(), sid); !errors.Is(err, harness.ErrNotFound) {
				t.Fatalf("%s: invalid submission persisted Session: %v", field, err)
			}
			tid, err := harness.TurnID(sid, harness.HostRef{Kind: "conversation_turn", ID: "turn"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := hstore.GetTurn(t.Context(), tid); !errors.Is(err, harness.ErrNotFound) {
				t.Fatalf("%s: invalid submission persisted Turn: %v", field, err)
			}
			raw = "rule-✓"
			session, err = newSession()
			if err != nil {
				t.Fatalf("%s: corrected session construction: %v", field, err)
			}
			out, err := session.Prompt(t.Context(), "turn", "hello")
			if err != nil || out.Turn.Status != harness.TurnCompleted || len(client.requests) != 1 {
				t.Fatalf("%s: same-ID corrected submission failed, status=%s err=%v modelCalls=%d",
					field, out.Turn.Status, err, len(client.requests))
			}
			raw = "rule-next"
			changed, err := newSession()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := changed.Prompt(t.Context(), "turn", "hello"); !errors.Is(err, harness.ErrConflict) ||
				len(client.requests) != 1 {
				t.Fatalf("%s: changed valid admission unexpectedly reused original: err=%v calls=%d",
					field, err, len(client.requests))
			}
		})
	}
}

// Direct Harness entrypoints must share the same admission check, rather than
// depending on the optional Session facade to do the right thing.
func TestCanonicalUTF8DirectHarnessStartFailsBeforeDurableAdmission(t *testing.T) {
	cases := []struct {
		name string
		edit func(*harness.StartRequest)
	}{
		{"goal", func(r *harness.StartRequest) { r.Goal = "bad-\xff" }},
		{"thread", func(r *harness.StartRequest) { r.Thread.ID = "bad-\xfe" }},
		{"input-reference", func(r *harness.StartRequest) {
			r.InputMessage = &harness.HostRef{Kind: "message", ID: "bad-\xe2\x82"}
		}},
		{"config-memory-policy", func(r *harness.StartRequest) {
			r.Config.MemoryPolicy = "bad-\xff"
		}},
		{"context-instructions", func(r *harness.StartRequest) {
			r.Context.Instructions = "bad-\xfe"
		}},
		{"context-source-path", func(r *harness.StartRequest) {
			r.Context.SourcePath[0] = "bad-\xff"
			r.Context.Entries[0].SourceID = "bad-\xff"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kstore, hstore := memory.NewStore(), harness.NewMemoryStore()
			modelClient := &textSessionModel{}
			runtime := mustTextRuntime(t, kstore)
			loop, err := agent.NewRunner(agent.Dependencies{
				Runtime: runtime, Model: modelClient, PrepareModelRequest: harness.MaterializeContextWindowRequest,
			})
			if err != nil {
				t.Fatal(err)
			}
			runner, err := harness.NewRunner(harness.Dependencies{
				Runtime: runtime, Agent: loop, Store: hstore,
				Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
			})
			if err != nil {
				t.Fatal(err)
			}
			options := textSessionOptions(runtime, hstore, modelClient, nil).Session
			current := runtimecontext.Entry{ID: "turn/user", SourceID: "turn/user", TurnID: "turn",
				Message: model.Message{Role: model.RoleUser, Content: model.TextParts("hello")}}
			original := harness.StartRequest{
				HostThread: options.HostThread, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "turn"},
				Actor: options.Actor, Thread: options.Thread, RequestID: "turn", Goal: "hello",
				Config: options.Config,
				Context: &harness.ContextSeed{SourcePath: []string{current.SourceID},
					Entries: []runtimecontext.Entry{current}},
			}
			input := original
			input.Context = &harness.ContextSeed{SourcePath: append([]string(nil), original.Context.SourcePath...),
				Entries: runtimecontext.CloneEntries(original.Context.Entries)}
			tc.edit(&input)
			if _, err := runner.StartAgentTurn(t.Context(), input); !errors.Is(err, harness.ErrInvalidRequest) {
				t.Fatalf("invalid direct Start admitted: %v", err)
			}
			sid, err := harness.SessionID(original.HostThread)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := hstore.GetSession(t.Context(), sid); !errors.Is(err, harness.ErrNotFound) {
				t.Fatalf("invalid direct Start persisted Session: %v", err)
			}
			tid, err := harness.TurnID(sid, original.HostTurn)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := hstore.GetTurn(t.Context(), tid); !errors.Is(err, harness.ErrNotFound) {
				t.Fatalf("invalid direct Start persisted Turn: %v", err)
			}
			if len(modelClient.requests) != 0 {
				t.Fatalf("invalid direct Start invoked model: %d", len(modelClient.requests))
			}
			out, err := runner.StartAgentTurn(t.Context(), original)
			if err != nil || out.Turn.Status != harness.TurnCompleted || len(modelClient.requests) != 1 {
				t.Fatalf("same-ID corrected direct Start failed: status=%s err=%v calls=%d",
					out.Turn.Status, err, len(modelClient.requests))
			}
		})
	}
}

func TestCanonicalUTF8ModelMetadataRejectsAliases(t *testing.T) {
	for _, invalid := range []string{string([]byte{0xff}), string([]byte{0xfe}), string([]byte{0xe2, 0x82})} {
		t.Run(fmt.Sprintf("%x", invalid), func(t *testing.T) {
			media := model.Message{Role: model.RoleUser, Content: []model.ContentPart{{
				Kind: model.ContentFile, MediaType: "application/pdf",
				Source: &model.MediaSource{Kind: model.MediaSourceFileID, FileID: "file-" + invalid},
			}}}
			if err := model.ValidateMessage(media); !errors.Is(err, model.ErrInvalidContent) {
				t.Fatalf("malformed media identity accepted: %v", err)
			}
			replay := model.Message{Role: model.RoleAssistant, Content: model.TextParts("text"),
				ProviderReplay: &model.ProviderReplay{Provider: "provider-" + invalid, Format: "opaque"}}
			if err := model.ValidateMessage(replay); !errors.Is(err, model.ErrInvalidContent) {
				t.Fatalf("malformed ProviderReplay identity accepted: %v", err)
			}
		})
	}
}
