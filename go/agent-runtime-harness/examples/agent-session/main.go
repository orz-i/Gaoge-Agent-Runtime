// Run with: go run ./go/agent-runtime-harness/examples/agent-session
// Memory Stores and a fixture model make this example offline and process-local.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runfeed"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const releaseTool = "release_checks:v1"

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, output io.Writer) error {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		return err
	}
	rawFeed, err := runfeed.New(memory.NewRunFeedStore(), runfeed.Options{})
	if err != nil {
		return err
	}
	feed, err := harness.NewTurnFeed(rawFeed)
	if err != nil {
		return err
	}
	// The Host is the only owner of conversation history. This demo keeps it
	// in memory and runs one user Turn at a time.
	var history []runtimecontext.Entry
	session, err := harness.OpenAgentSession(harness.AgentSessionOptions{
		Runtime: runtime, Store: harness.NewMemoryStore(), Model: fixtureModel{}, TurnFeed: feed,
		LocalTools: []harness.SessionTool{{
			Registration: tools.Registration{
				Definition: tools.Definition{Key: releaseTool, Name: "release_checks",
					InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
				Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
					return tools.ExecutionResult{Content: json.RawMessage(`{"passed":12,"failed":0}`),
						Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "read"}}, nil
				}),
			},
			Policy: harness.ToolPolicySnapshot{DefinitionVersion: "v1", ApprovalMode: "never", SideEffectLevel: "read"},
		}},
		Session: harness.SessionOptions{
			HostThread: harness.HostRef{Kind: "conversation", ID: "demo/release"},
			Actor:      kernel.ActorRef{TenantID: "demo", ActorID: "operator"},
			Thread:     kernel.ThreadRef{Kind: "conversation", ID: "demo/release"},
			Config:     harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{Model: "fixture", MaxModelOutputTokens: 256}},
			Transcript: harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
				return history, nil
			}),
		},
		Context: harness.AgentSessionContextOptions{ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
			return runtimecontext.ModelWindow{ContextTokens: 16384, ReservedOutputTokens: 256}, nil
		}},
	})
	if err != nil {
		return err
	}
	for _, input := range []struct{ ID, Text string }{
		{"release-1", "Check the release and summarize the result."},
		{"release-2", "What were the earlier release results?"},
	} {
		result, err := session.Prompt(ctx, input.ID, input.Text)
		if err != nil {
			return err
		}
		if result.Turn.Status != harness.TurnCompleted || result.Output == nil {
			return fmt.Errorf("%s did not complete: %s", input.ID, result.Turn.Status)
		}
		// Subscribe uses the same Host Turn ID and executing Runner. Durable
		// state above remains authoritative if a live stream closes early.
		subscription, err := session.Subscribe(ctx, input.ID, 0)
		if err != nil {
			return err
		}
		subscription.Close()
		if len(subscription.Replay) == 0 || !subscription.Replay[len(subscription.Replay)-1].Terminal {
			return fmt.Errorf("%s has no retained completion event", input.ID)
		}
		var answer string
		if err := json.Unmarshal(result.Output.Content, &answer); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "%s: %s\n", input.ID, answer); err != nil {
			return err
		}
		history = append(history, entry(input.ID, model.RoleUser, input.Text), entry(input.ID, model.RoleAssistant, answer))
	}
	return nil
}

func entry(turnID string, role model.Role, text string) runtimecontext.Entry {
	id := turnID + "/" + string(role)
	return runtimecontext.Entry{ID: id, SourceID: id, TurnID: turnID, Message: model.Message{Role: role, Content: model.TextParts(text)}}
}

type fixtureModel struct{}

func (fixtureModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if request.MaxOutputTokens != 256 {
		return model.Response{}, fmt.Errorf("expected frozen output ceiling 256, got %d", request.MaxOutputTokens)
	}
	for _, message := range request.Messages {
		switch message.Role {
		case model.RoleAssistant:
			if text := model.TextValue(message.Content); text != "" {
				return model.Response{Content: "Previous answer: " + text}, nil
			}
		case model.RoleTool:
			var checks struct{ Passed, Failed int }
			if err := json.Unmarshal([]byte(model.TextValue(message.Content)), &checks); err != nil {
				return model.Response{}, err
			}
			return model.Response{Content: fmt.Sprintf("Release checks: %d passed, %d failed.", checks.Passed, checks.Failed)}, nil
		case model.RoleSystem, model.RoleUser:
			continue
		}
	}
	return model.Response{ToolCalls: []tools.Call{{ID: "release-check-1", ToolKey: releaseTool, Arguments: json.RawMessage(`{}`)}}}, nil
}
