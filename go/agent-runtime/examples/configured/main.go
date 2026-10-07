package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/bootstrap"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func main() {
	path := "runtime.yaml"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	config, err := bootstrap.Load(path, bootstrap.LoadOptions{})
	if err != nil {
		log.Fatal(err)
	}
	registry := bootstrap.NewDefaultRegistry()
	if err = registry.RegisterModel("demo", func(context.Context, bootstrap.FactoryInput) (model.Client, error) {
		return demoModel{}, nil
	}); err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	host, err := bootstrap.Build(ctx, config, registry, bootstrap.EnvSecretResolver{})
	if err != nil {
		log.Fatal(err)
	}
	if err = host.Start(ctx); err != nil {
		log.Fatal(err)
	}
	defer func() {
		if closeErr := host.Close(context.Background()); closeErr != nil {
			log.Printf("close runtime: %v", closeErr)
		}
	}()
	snapshot, err := host.Agent.StartRun(ctx, agent.StartRequest{
		Actor:  kernel.ActorRef{TenantID: "example", ActorID: "operator"},
		Thread: kernel.ThreadRef{Kind: "demo", ID: "configured"},
		Goal:   "Demonstrate configuration-driven bootstrap",
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("run=%s status=%s\n", snapshot.Run.ID, snapshot.Run.Status)
}

type demoModel struct{}

func (demoModel) Generate(context.Context, model.Request) (model.Response, error) {
	return model.Response{Content: "configured runtime is ready"}, nil
}
