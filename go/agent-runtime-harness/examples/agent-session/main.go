// Run with: go run ./go/agent-runtime-harness/examples/agent-session
// Memory Stores and a fixture model make this example offline and process-local.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const releaseTool = "release_checks:v1"

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		return err
	}
	session, err := harness.OpenAgentSession(harness.AgentSessionOptions{
		Runtime: runtime, Store: harness.NewMemoryStore(), Model: fixtureModel{},
		Tools: []tools.Registration{{
			Definition: tools.Definition{Key: releaseTool, Name: "release_checks",
				InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
			Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				return tools.ExecutionResult{Content: json.RawMessage(`{"passed":12,"failed":0}`),
					Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "read"}}, nil
			}),
		}},
		Session: harness.SessionOptions{
			HostThread: harness.HostRef{Kind: "conversation", ID: "demo/release"},
			Actor:      kernel.ActorRef{TenantID: "demo", ActorID: "operator"},
			Thread:     kernel.ThreadRef{Kind: "conversation", ID: "demo/release"},
			Config: harness.ConfigSnapshot{
				Model: "fixture", MaxModelOutputTokens: 256,
				ToolKeys: []string{releaseTool, harness.ContextArtifactToolKey},
				ToolPolicies: []harness.ToolPolicySnapshot{
					{Key: releaseTool, DefinitionVersion: "v1", ApprovalMode: "never", SideEffectLevel: "read"},
					harness.ContextArtifactToolPolicySnapshot(),
				},
				ContextPolicy: runtimecontext.Policy{MaxInputTokens: 8192},
			},
			// The Host supplies prior User/Assistant history here. The adapter
			// appends this turn's User prompt with stable source identities.
			Transcript: harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
				return nil, nil
			}),
		},
		Context: harness.AgentSessionContextOptions{ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
			return runtimecontext.ModelWindow{ContextTokens: 16384, ReservedOutputTokens: 256}, nil
		}},
	})
	if err != nil {
		return err
	}
	result, err := session.Prompt(ctx, "release-turn-1", "Check the release and summarize the result.")
	if err != nil {
		return err
	}
	if result.Turn.Status != harness.TurnCompleted || result.Output == nil {
		return fmt.Errorf("release turn did not complete: %s", result.Turn.Status)
	}
	fmt.Println(string(result.Output.Content))
	return nil
}

type fixtureModel struct{}

func (fixtureModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if request.MaxOutputTokens != 256 {
		return model.Response{}, fmt.Errorf("expected frozen output ceiling 256, got %d", request.MaxOutputTokens)
	}
	for _, message := range request.Messages {
		if message.Role == model.RoleTool {
			return model.Response{Content: "Release checks: 12 passed, 0 failed."}, nil
		}
	}
	return model.Response{ToolCalls: []tools.Call{{ID: "release-check-1", ToolKey: releaseTool, Arguments: json.RawMessage(`{}`)}}}, nil
}
