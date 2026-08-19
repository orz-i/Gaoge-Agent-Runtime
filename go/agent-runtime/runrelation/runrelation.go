// Package runrelation persists feature-neutral parent/child Run ownership.
package runrelation

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

const CapabilityRelations kernel.Capability = "runrelation.registry"

var (
	ErrInvalidInput = errors.New("invalid run relation input")
	ErrConflict     = errors.New("run relation conflict")
	ErrNotFound     = errors.New("run relation not found")
)

// Kind identifies why one parent owns a Child Run.
type Kind string

const (
	KindPlanStep       Kind = "plan_step"
	KindTeamMember     Kind = "team_member"
	KindWorkflowEffect Kind = "workflow_effect"
	KindDelegation     Kind = "delegation"
	KindCapability     Kind = "capability"
)

// Draft is one relation before its creation timestamp is assigned.
type Draft struct {
	ParentRunID string
	ChildRunID  string
	Kind        Kind
	OwnerNodeID string
}

// Relation is one immutable parent/child ownership fact.
type Relation struct {
	ParentRunID string    `json:"parentRunID"`
	ChildRunID  string    `json:"childRunID"`
	Kind        Kind      `json:"relationKind"`
	OwnerNodeID string    `json:"ownerNodeID"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Store persists immutable relations and enforces one Child per owner node.
type Store interface {
	Put(context.Context, Relation) (Relation, bool, error)
	GetByChild(context.Context, string) (Relation, error)
	ListChildren(context.Context, string) ([]Relation, error)
	ListAll(context.Context) ([]Relation, error)
}

// Recorder is the narrow capability consumed by parent Runtime features.
type Recorder interface {
	Ensure(context.Context, Draft) (Relation, error)
}

// Registry validates and timestamps immutable Run relations.
type Registry struct {
	store Store
	clock kernel.Clock
}

// New creates a relation Registry over one persistence adapter.
func New(store Store, clock kernel.Clock) (*Registry, error) {
	if store == nil {
		return nil, ErrInvalidInput
	}
	if clock == nil {
		clock = systemClock{}
	}
	return &Registry{store: store, clock: clock}, nil
}

// Descriptor declares the feature-neutral relation capability.
func (registry *Registry) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: "runrelation", Provides: []kernel.Capability{CapabilityRelations}}
}

// Ensure creates or reuses one identical stable relation.
func (registry *Registry) Ensure(ctx context.Context, draft Draft) (Relation, error) {
	if registry == nil || registry.store == nil {
		return Relation{}, ErrInvalidInput
	}
	relation, err := Prepare(Relation{
		ParentRunID: draft.ParentRunID, ChildRunID: draft.ChildRunID,
		Kind: draft.Kind, OwnerNodeID: draft.OwnerNodeID, CreatedAt: registry.clock.Now().UTC(),
	})
	if err != nil {
		return Relation{}, err
	}
	persisted, _, err := registry.store.Put(ctx, relation)
	return persisted, err
}

// GetByChild resolves the owning parent for one Child Run.
func (registry *Registry) GetByChild(ctx context.Context, childRunID string) (Relation, error) {
	if registry == nil || registry.store == nil {
		return Relation{}, ErrInvalidInput
	}
	return registry.store.GetByChild(ctx, strings.TrimSpace(childRunID))
}

// ListChildren returns stable child ownership in deterministic order.
func (registry *Registry) ListChildren(ctx context.Context, parentRunID string) ([]Relation, error) {
	if registry == nil || registry.store == nil {
		return nil, ErrInvalidInput
	}
	items, err := registry.store.ListChildren(ctx, strings.TrimSpace(parentRunID))
	if err != nil {
		return nil, err
	}
	Sort(items)
	return items, nil
}

// ListAll returns every immutable ownership fact for continuation reconciliation.
func (registry *Registry) ListAll(ctx context.Context) ([]Relation, error) {
	if registry == nil || registry.store == nil {
		return nil, ErrInvalidInput
	}
	items, err := registry.store.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	Sort(items)
	return items, nil
}

// Prepare normalizes and validates a relation before adapter persistence.
func Prepare(relation Relation) (Relation, error) {
	relation.ParentRunID = strings.TrimSpace(relation.ParentRunID)
	relation.ChildRunID = strings.TrimSpace(relation.ChildRunID)
	relation.Kind = Kind(strings.TrimSpace(string(relation.Kind)))
	relation.OwnerNodeID = strings.TrimSpace(relation.OwnerNodeID)
	relation.CreatedAt = relation.CreatedAt.UTC()
	if relation.ParentRunID == "" || relation.ChildRunID == "" || relation.ParentRunID == relation.ChildRunID ||
		relation.OwnerNodeID == "" || relation.CreatedAt.IsZero() || !validKind(relation.Kind) {
		return Relation{}, ErrInvalidInput
	}
	return relation, nil
}

// EqualIdentity compares immutable ownership independent of CreatedAt.
func EqualIdentity(left, right Relation) bool {
	return left.ParentRunID == right.ParentRunID && left.ChildRunID == right.ChildRunID &&
		left.Kind == right.Kind && left.OwnerNodeID == right.OwnerNodeID
}

// Sort applies the public deterministic relation order.
func Sort(items []Relation) {
	sort.Slice(items, func(left, right int) bool {
		if items[left].CreatedAt.Equal(items[right].CreatedAt) {
			if items[left].Kind == items[right].Kind {
				if items[left].OwnerNodeID == items[right].OwnerNodeID {
					return items[left].ChildRunID < items[right].ChildRunID
				}
				return items[left].OwnerNodeID < items[right].OwnerNodeID
			}
			return items[left].Kind < items[right].Kind
		}
		return items[left].CreatedAt.Before(items[right].CreatedAt)
	})
}

func validKind(kind Kind) bool {
	return kind == KindPlanStep || kind == KindTeamMember || kind == KindWorkflowEffect ||
		kind == KindDelegation || kind == KindCapability
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
