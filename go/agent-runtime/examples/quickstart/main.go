package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func main() {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		log.Fatal(err)
	}
	snapshot, err := runtime.Create(context.Background(), kernel.CreateRequest{
		Kind:   kernel.RunKind("agent"),
		Actor:  kernel.ActorRef{TenantID: "acme", ActorID: "user-1"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread-1"},
		Goal:   "Prepare a release summary",
		State:  json.RawMessage(`{}`),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s revision=%d status=%s\n", snapshot.Run.ID, snapshot.Run.Revision, snapshot.Run.Status)
}
