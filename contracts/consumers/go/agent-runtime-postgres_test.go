package consumer

import (
	"context"
	postgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/runrelation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/workflow"
	"gorm.io/gorm"
)

var _ interface {
	ListRuns(context.Context, postgres.RunQuery) ([]kernel.Snapshot, error)
	ListEventMetadata(context.Context, postgres.EventQuery) ([]postgres.EventMetadata, int64, error)
	CountEventTypes(context.Context, postgres.EventQuery) ([]postgres.EventTypeCount, error)
	EraseActorData(context.Context, string, string) error
} = (*postgres.KernelStore)(nil)

// Migrations are a separate host operation; constructors retain typed ports.
var (
	_ func(*gorm.DB) error                             = postgres.Migrate
	_ func(*gorm.DB) *postgres.KernelStore             = postgres.NewKernelStore
	_ func(*gorm.DB) *postgres.RunRelationStore        = postgres.NewRunRelationStore
	_ func(*gorm.DB) *postgres.WorkflowDefinitionStore = postgres.NewWorkflowDefinitionStore
	_ kernel.Store                                     = (*postgres.KernelStore)(nil)
	_ runrelation.Store                                = (*postgres.RunRelationStore)(nil)
	_ workflow.DefinitionStore                         = (*postgres.WorkflowDefinitionStore)(nil)
)
