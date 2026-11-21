package context_test

import (
	"errors"
	"testing"

	runtimectx "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestCheckpointWriteProfileOwnsEveryNewRevisionButNotNoOps(t *testing.T) {
	baseManager := runtimectx.NewManager(runtimectx.Dependencies{CheckpointWriteProfile: runtimectx.CheckpointWriteLegacy})
	base, err := baseManager.Open(t.Context(), openRequest("u1"))
	requireNoError(t, err)
	for _, profile := range []runtimectx.CheckpointWriteProfile{runtimectx.CheckpointWriteLegacy, runtimectx.CheckpointWriteLosslessJSON} {
		t.Run(string(profile), func(t *testing.T) {
			manager, err := runtimectx.NewManager(runtimectx.Dependencies{}).WithCheckpointWriteProfile(profile)
			requireNoError(t, err)
			encoding := runtimectx.CheckpointContentHashEncodingLosslessJSON
			if profile == runtimectx.CheckpointWriteLegacy {
				encoding = ""
			}
			artifact, err := runtimectx.NewArtifact(runtimectx.ArtifactToolResult, base.ScopeID, base.Generation, "call", "result", nil)
			requireNoError(t, err)
			for _, test := range []struct {
				name string
				make func() (runtimectx.Checkpoint, error)
			}{
				{"fresh", func() (runtimectx.Checkpoint, error) { return manager.Open(t.Context(), openRequest("other")) }},
				{"append full path", func() (runtimectx.Checkpoint, error) {
					r := openRequest("u1", "a1")
					r.Previous = &base
					return manager.Open(t.Context(), r)
				}},
				{"append source delta", func() (runtimectx.Checkpoint, error) {
					r := openRequest("u2")
					r.Previous, r.SourceDelta = &base, true
					return manager.Open(t.Context(), r)
				}},
				{"reset", func() (runtimectx.Checkpoint, error) {
					r := openRequest("other")
					r.Previous = &base
					return manager.Open(t.Context(), r)
				}},
				{"working state", func() (runtimectx.Checkpoint, error) {
					r := openRequest("u1")
					r.Previous, r.WorkingState = &base, workingStateFor(base, 1)
					return manager.Open(t.Context(), r)
				}},
				{"runtime tail", func() (runtimectx.Checkpoint, error) {
					return manager.Capture(t.Context(), runtimectx.CaptureRequest{Previous: base, StaticFingerprint: base.StaticFingerprint, RunID: "run",
						Messages: append(runtimectx.Materialize(base.Window), model.Message{Role: model.RoleAssistant, Content: model.TextParts("answer")})})
				}},
				{"artifact only", func() (runtimectx.Checkpoint, error) {
					return manager.Capture(t.Context(), runtimectx.CaptureRequest{Previous: base, StaticFingerprint: base.StaticFingerprint, RunID: "run",
						Messages: runtimectx.Materialize(base.Window), Artifacts: []runtimectx.Artifact{artifact}})
				}},
				{"model window", func() (runtimectx.Checkpoint, error) { return manager.BindModelWindow(t.Context(), base, "window") }},
				{"rollover", func() (runtimectx.Checkpoint, error) {
					return manager.Rollover(t.Context(), runtimectx.RolloverRequest{Previous: base, Window: base.Window, Reason: "explicit"})
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					next, err := test.make()
					requireNoError(t, err)
					if !runtimectx.ValidCheckpoint(next) || next.ContentHashEncoding != encoding {
						t.Fatalf("new revision ignored selected profile: %+v", next)
					}
				})
			}
			r := openRequest("u1")
			r.Previous = &base
			unchanged, err := manager.Open(t.Context(), r)
			requireNoError(t, err)
			if unchanged.ID != base.ID || unchanged.ContentHashEncoding != "" || manager.CheckpointWriteProfile() != profile {
				t.Fatal("writer profile rewrote an unchanged legacy record")
			}
		})
	}
}

func TestCheckpointWriteProfileRejectsUnresolvedAndUnknownReplaySelections(t *testing.T) {
	manager := runtimectx.NewManager(runtimectx.Dependencies{})
	for _, profile := range []runtimectx.CheckpointWriteProfile{"", "future-profile"} {
		if _, err := manager.WithCheckpointWriteProfile(profile); !errors.Is(err, runtimectx.ErrInvalidInput) {
			t.Fatalf("replay inherited a default or accepted unknown profile %q: %v", profile, err)
		}
	}
	unknown := runtimectx.NewManager(runtimectx.Dependencies{CheckpointWriteProfile: "future-profile"})
	if _, err := unknown.Open(t.Context(), openRequest("u1")); !errors.Is(err, runtimectx.ErrInvalidInput) {
		t.Fatalf("unknown writer silently selected another encoding: %v", err)
	}
}
