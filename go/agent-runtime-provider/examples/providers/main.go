// A real, text-and-tool Agent example with either OpenAI or Anthropic.
// No provider credentials are embedded in source or persisted in Kernel state.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func main() {
	kind, modelID := strings.TrimSpace(os.Getenv("GAOGE_PROVIDER")), strings.TrimSpace(os.Getenv("GAOGE_MODEL"))
	if kind == "" || modelID == "" || len(os.Args) < 2 {
		log.Fatal("usage: set GAOGE_PROVIDER=openai|anthropic, GAOGE_MODEL=<model-id>, provider API key; pass a prompt")
	}
	var client model.Client
	var err error
	switch kind {
	case "openai":
		client, err = openai.New(openai.Config{APIKey: os.Getenv("OPENAI_API_KEY")})
	case "anthropic":
		client, err = anthropic.New(anthropic.Config{APIKey: os.Getenv("ANTHROPIC_API_KEY")})
	default:
		log.Fatal("GAOGE_PROVIDER must be openai or anthropic")
	}
	if err != nil {
		log.Fatal(err)
	}
	router, err := provider.NewRouter(kind, provider.Binding{Name: kind, Client: client})
	if err != nil {
		log.Fatal(err)
	}
	registry, err := tools.NewRegistry([]tools.Registration{{
		Definition: tools.Definition{
			Key: "echo:v1", Name: "echo", Description: "Echo a text string without external side effects",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"],"additionalProperties":false}`),
		},
		Handler: tools.HandlerFunc(func(ctx context.Context, req tools.ExecutionRequest) (tools.ExecutionResult, error) {
			var args struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(req.Call.Arguments, &args); err != nil {
				return tools.ExecutionResult{}, err
			}
			body, err := json.Marshal(map[string]string{"echo": args.Text})
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			return tools.ExecutionResult{Content: body, Receipt: tools.Receipt{
				ExecutionID: req.Call.ID, Disposition: "committed",
			}}, nil
		}),
	}})
	if err != nil {
		log.Fatal(err)
	}
	k, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		log.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: k, Model: router, Catalog: registry, Executor: registry})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	snapshot, err := runner.StartRun(ctx, agent.StartRequest{
		Actor:  kernel.ActorRef{TenantID: "local", ActorID: "operator"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "provider-demo"},
		Goal:   strings.Join(os.Args[1:], " "), Instructions: "Use the echo tool, then report its returned value.",
		Model: kind + "/" + modelID, ToolKeys: []string{"echo:v1"}, RequiredToolKeys: []string{"echo:v1"},
	})
	if err != nil {
		log.Fatal(err)
	}
	view, err := agent.ViewState(snapshot)
	if err != nil {
		log.Fatal(err)
	}
	if len(view.Messages) > 0 {
		fmt.Println(view.Messages[len(view.Messages)-1].Content)
	}
	fmt.Printf("Run %s: %s\n", snapshot.Run.ID, snapshot.Run.Status)
}
