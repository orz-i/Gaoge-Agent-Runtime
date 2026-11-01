// An optional Harness Session over the same single Agent loop.
// Run with: go run ./go/agent-runtime-harness/examples/session
// This uses Memory Stores: it does not survive process exit.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer) error {
	// A plain Agent needs only Kernel + Agent; add Harness only when a
	// product needs durable Turn identities and host-owned Context.
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		return err
	}
	client := &fixtureModel{}
	loop, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: client,
		// The Host owns the transcript. Project its immutable Context before
		// the one ModelInvocation commit, never from dispatch middleware.
		PrepareModelRequest: harness.MaterializeContextWindowRequest,
	})
	if err != nil {
		return err
	}
	host, err := harness.NewRunner(harness.Dependencies{
		Runtime: runtime, Agent: loop, Store: harness.NewMemoryStore(),
		Context: runtimecontext.NewManager(runtimecontext.Dependencies{}),
		// A Clock is not required for the normal system-clock path.
	})
	if err != nil {
		return err
	}

	// The transcript is deliberately held by the Host. Harness persists only
	// model-visible Context checkpoints; it is not a second conversation DB.
	// This minimal source reads current history, not historical submission
	// snapshots: after later Turns, use Session.Load for an earlier result.
	var history []runtimecontext.Entry
	threadID := "demo/tenant-a/conversation"
	session, err := host.OpenSession(harness.SessionOptions{
		HostThread: harness.HostRef{Kind: "conversation", ID: threadID},
		Actor:      kernel.ActorRef{TenantID: "tenant-a", ActorID: "reader"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: threadID},
		Config:     harness.ConfigSnapshot{Model: "fixture"},
		Transcript: harness.TranscriptSourceFunc(func(
			_ context.Context, _, turn harness.HostRef, prompt string,
		) (*harness.ContextSeed, error) {
			current := userEntry(turn.ID, prompt)
			entries := append(runtimecontext.CloneEntries(history), current)
			path := make([]string, len(entries))
			for i, entry := range entries {
				path[i] = entry.SourceID
			}
			return &harness.ContextSeed{SourcePath: path, Entries: entries}, nil
		}),
	})
	if err != nil {
		return err
	}

	turns := []struct{ id, prompt string }{
		{"turn-1", "Hello."},
		{"turn-2", "How many user turns are in the conversation?"},
	}
	var firstTurn harness.Snapshot
	for index, turn := range turns {
		snapshot, promptErr := session.Prompt(ctx, turn.id, turn.prompt)
		if promptErr != nil {
			return promptErr
		}
		if snapshot.Turn.Status != harness.TurnCompleted || snapshot.Output == nil ||
			snapshot.Output.ContentType != "text" {
			return fmt.Errorf("turn %s did not complete", turn.id)
		}
		if index == 0 {
			firstTurn = snapshot
		}
		var answer string
		if err := json.Unmarshal(snapshot.Output.Content, &answer); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "%s: %s\n", turn.id, answer); err != nil {
			return err
		}
		// Retry only the SAME admitted Host transcript and stable Turn ID.
		// A completed Turn must not trigger another model execution.
		replayed, replayErr := session.Prompt(ctx, turn.id, turn.prompt)
		if replayErr != nil {
			return fmt.Errorf("turn %s replay failed: %w", turn.id, replayErr)
		}
		if replayed.Turn.ID != snapshot.Turn.ID || client.calls != index+1 {
			return fmt.Errorf("turn %s replay dispatched a new model call: calls=%d", turn.id, client.calls)
		}
		// The Host, not the Agent SDK, appends the completed assistant answer
		// to the conversation before the next prompt.
		history = append(history, userEntry(turn.id, turn.prompt), runtimecontext.Entry{
			ID: turn.id + "/assistant", SourceID: turn.id + "/assistant", TurnID: turn.id,
			Message: model.Message{Role: model.RoleAssistant, Content: model.TextParts(answer)},
		})
	}

	// Both Assistant answers have now been appended to the Host transcript.
	// For an older, already-admitted Turn, read its committed result with Load:
	// Prompt would reconstruct a DIFFERENT ContextSeed from today's history.
	old, err := session.Load(ctx, turns[0].id)
	if err != nil {
		return fmt.Errorf("load historical turn: %w", err)
	}
	if old.Turn.ID != firstTurn.Turn.ID || old.Turn.Status != harness.TurnCompleted ||
		old.Output == nil || string(old.Output.Content) != string(firstTurn.Output.Content) || client.calls != len(turns) {
		return errors.New("historical Load changed the original answer or dispatched a model call")
	}

	// Regression proof: the changed Host transcript cannot be passed as a
	// retry of the OLD Prompt. Frozen admission rejects it, without dispatch.
	if _, lateErr := session.Prompt(ctx, turns[0].id, turns[0].prompt); !errors.Is(lateErr, harness.ErrConflict) {
		if lateErr == nil {
			return errors.New("changed historical Prompt unexpectedly succeeded")
		}
		return fmt.Errorf("changed historical Prompt: %w", lateErr)
	}
	if client.calls != len(turns) {
		return errors.New("rejected historical Prompt called model again")
	}

	// A terminal Resume only observes the existing Turn. It must not send
	// another physical model request or create another execution owner.
	reloaded, err := session.Resume(ctx, "turn-2")
	if err != nil {
		return err
	}
	if reloaded.Turn.Status != harness.TurnCompleted || client.calls != 2 {
		return fmt.Errorf("unexpected replay: status=%s model_calls=%d", reloaded.Turn.Status, client.calls)
	}
	return nil
}

func userEntry(turnID, content string) runtimecontext.Entry {
	id := turnID + "/user"
	return runtimecontext.Entry{
		ID: id, SourceID: id, TurnID: turnID, Required: true,
		Message: model.Message{Role: model.RoleUser, Content: model.TextParts(content)},
	}
}

// Replace this model.Client with an explicitly configured Provider. It counts
// actual model-visible user messages so Context projection is observable.
type fixtureModel struct{ calls int }

func (client *fixtureModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.calls++
	users := 0
	for _, message := range request.Messages {
		if message.Role == model.RoleUser {
			users++
		}
	}
	return model.Response{Content: fmt.Sprintf("I can see %d user turn(s).", users)}, nil
}
