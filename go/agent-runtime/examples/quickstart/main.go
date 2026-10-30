// This offline quickstart executes an Agent, a Tool, and a final answer.
// Its deterministic fixture model needs no API key. Memory state lasts only for
// this process; see the Harness task-agent example for Context and recovery.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const releaseChecksKey = "release_checks:v1"

type releaseChecks struct {
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: releaseChecksKey, Name: "release_checks", Description: "Read the local release-check fixture",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			content, marshalErr := json.Marshal(releaseChecks{Passed: 12, Failed: 0})
			return tools.ExecutionResult{
				Content: content,
				Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "read"},
			}, marshalErr
		}),
	}})
	if err != nil {
		return err
	}
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		return err
	}
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: fixtureModel{}, Catalog: registry, Executor: registry,
		RunExecution: &agent.RunExecution{OwnerID: "quickstart", LeaseDuration: 5 * time.Minute},
	})
	if err != nil {
		return err
	}
	snapshot, err := runner.StartRun(ctx, agent.StartRequest{
		ID:     "quickstart-release-report",
		Actor:  kernel.ActorRef{TenantID: "local", ActorID: "operator"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "quickstart"},
		Goal:   "Read the release checks and report whether any checks failed.", Model: "offline-fixture",
		ToolKeys: []string{releaseChecksKey}, RequiredToolKeys: []string{releaseChecksKey},
	})
	if err != nil {
		return err
	}
	view, err := agent.ViewState(snapshot)
	if err != nil {
		return err
	}
	if snapshot.Run.Status != kernel.RunStatusCompleted || len(view.Messages) == 0 {
		return fmt.Errorf("agent did not complete: %s", snapshot.Run.Status)
	}
	fmt.Println(view.Messages[len(view.Messages)-1].Content)
	fmt.Printf("Run %s: %s\n", snapshot.Run.ID, snapshot.Run.Status)
	return nil
}

// A fixture isolates SDK execution from model quality. Replace it with an
// explicitly configured provider client to run an actual language model.
type fixtureModel struct{}

func (fixtureModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		message := request.Messages[index]
		if message.Role != model.RoleTool {
			continue
		}
		var checks releaseChecks
		if err := json.Unmarshal([]byte(model.TextValue(message.Content)), &checks); err != nil {
			return model.Response{}, err
		}
		return model.Response{Content: fmt.Sprintf("Release checks: %d passed, %d failed.", checks.Passed, checks.Failed)}, nil
	}
	return model.Response{ToolCalls: []tools.Call{{
		ID: "release-checks-1", ToolKey: releaseChecksKey, Arguments: json.RawMessage(`{}`),
	}}}, nil
}
