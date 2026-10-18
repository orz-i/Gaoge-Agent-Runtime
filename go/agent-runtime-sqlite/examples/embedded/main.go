// Stage 1: a single-executor desktop Agent persisted to SQLite.
// It deliberately does not run a background scheduler or a remote queue.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	sqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type fixtureModel struct{}

func (fixtureModel) Generate(_ context.Context, _ model.Request) (model.Response, error) {
	return model.Response{Content: "Local Agent completed (fixture model)", Usage: &model.Usage{
		InputTokens: 4, OutputTokens: 6,
	}}, nil
}

func main() {
	dbFile := flag.String("db", "", "required path inside a private (0700) Profile directory")
	init := flag.Bool("init", false, "explicitly initialize a new (nonexistent) SQLite database")
	flag.Parse()
	if *dbFile == "" {
		log.Fatal("-db must specify a private Profile database path")
	}
	if err := run(context.Background(), *dbFile, *init); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, dbFile string, initialize bool) error {
	var store *sqlite.Store
	var err error
	if initialize {
		store, err = sqlite.CreateNew(ctx, dbFile)
	} else {
		store, err = sqlite.Open(ctx, dbFile)
	}
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	runtime, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		return err
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: fixtureModel{}})
	if err != nil {
		return err
	}
	id := fmt.Sprintf("local-%d", time.Now().UnixNano())
	snapshot, err := runner.StartRun(ctx, agent.StartRequest{
		ID:     id,
		Actor:  kernel.ActorRef{TenantID: "local", ActorID: "default-profile"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "demo"},
		Goal:   "Demonstrate a SQLite-backed local Agent",
	})
	if err != nil {
		return err
	}
	fmt.Printf("Run complete: %s revision=%d status=%s\n", snapshot.Run.ID, snapshot.Run.Revision, snapshot.Run.Status)
	if err := store.Close(); err != nil {
		return err
	}
	reopened, err := sqlite.Open(ctx, dbFile)
	if err != nil {
		return err
	}
	defer func() { _ = reopened.Close() }()
	recovered, err := reopened.Load(ctx, id)
	if err != nil {
		return err
	}
	if recovered.Run.Status != kernel.RunStatusCompleted || recovered.Run.Revision != snapshot.Run.Revision {
		return fmt.Errorf("reopened local Run does not match committed snapshot")
	}
	fmt.Printf("New connection: %s revision=%d status=%s journalHead=%d\n",
		recovered.Run.ID, recovered.Run.Revision, recovered.Run.Status, recovered.EventHead)
	return nil
}
