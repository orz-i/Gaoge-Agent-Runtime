package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const CapabilityDefinitionRegistry kernel.Capability = "workflow.definition_registry"

var (
	ErrInvalidDefinitionRegistry = errors.New("invalid workflow definition registry input")
	ErrDefinitionConflict        = errors.New("workflow definition conflict")
	ErrDefinitionNotFound        = errors.New("workflow definition not found")
	ErrDefinitionDisabled        = errors.New("workflow definition is disabled")
)

// DefinitionScopeKind controls where one published definition is visible.
type DefinitionScopeKind string

const (
	DefinitionScopeSystem DefinitionScopeKind = "system"
	DefinitionScopeTenant DefinitionScopeKind = "tenant"
	DefinitionScopeActor  DefinitionScopeKind = "actor"
)

// DefinitionScope is a normalized visibility boundary for Definition revisions.
type DefinitionScope struct {
	Kind     DefinitionScopeKind `json:"kind"`
	TenantID string              `json:"tenantID,omitempty"`
	ActorID  string              `json:"actorID,omitempty"`
}

// DefinitionAvailability controls only new resolution, never historical revisions.
type DefinitionAvailability string

const (
	DefinitionActive   DefinitionAvailability = "active"
	DefinitionDisabled DefinitionAvailability = "disabled"
)

// DefinitionPublishMode decides whether a new revision becomes the active pointer.
type DefinitionPublishMode string

const (
	PublishAndActivate DefinitionPublishMode = "activate"
	PublishStaged      DefinitionPublishMode = "stage"
)

// DefinitionRevision is one immutable published Definition plus audit metadata.
type DefinitionRevision struct {
	Scope              DefinitionScope `json:"scope"`
	Definition         Definition      `json:"definition"`
	PublishedBy        string          `json:"publishedBy"`
	IdempotencyKey     string          `json:"idempotencyKey"`
	RequestFingerprint string          `json:"requestFingerprint"`
	PublishedAt        time.Time       `json:"publishedAt"`
}

// DefinitionHead is the mutable CAS pointer for one scoped Definition identity.
type DefinitionHead struct {
	Scope          DefinitionScope        `json:"scope"`
	DefinitionID   string                 `json:"definitionID"`
	LatestRevision int                    `json:"latestRevision"`
	ActiveRevision int                    `json:"activeRevision,omitempty"`
	Availability   DefinitionAvailability `json:"availability"`
	Version        uint64                 `json:"version"`
	UpdatedAt      time.Time              `json:"updatedAt"`
}

// DefinitionPublishMutation is the atomic Store command prepared by DefinitionRegistry.
type DefinitionPublishMutation struct {
	Revision         DefinitionRevision
	ExpectedRevision int
	Mode             DefinitionPublishMode
}

// DefinitionActivationMutation changes only the active pointer/availability under CAS.
type DefinitionActivationMutation struct {
	Scope           DefinitionScope
	DefinitionID    string
	TargetRevision  int
	Availability    DefinitionAvailability
	ExpectedVersion uint64
	UpdatedAt       time.Time
}

// DefinitionStore persists immutable revisions and their CAS-controlled active heads.
type DefinitionStore interface {
	Publish(context.Context, DefinitionPublishMutation) (DefinitionRevision, DefinitionHead, bool, error)
	GetRevision(context.Context, DefinitionScope, string, int) (DefinitionRevision, error)
	GetHead(context.Context, DefinitionScope, string) (DefinitionHead, error)
	ListHeads(context.Context, DefinitionScope) ([]DefinitionHead, error)
	SetActivation(context.Context, DefinitionActivationMutation) (DefinitionHead, bool, error)
}

// DefinitionRegistry validates and publishes immutable Workflow Definition revisions.
type DefinitionRegistry struct {
	store DefinitionStore
	clock kernel.Clock
}

// PublishDefinitionRequest creates the exact next revision under expectedRevision CAS.
type PublishDefinitionRequest struct {
	Scope            DefinitionScope
	Draft            DefinitionDraft
	ExpectedRevision int
	Mode             DefinitionPublishMode
	IdempotencyKey   string
	PublishedBy      string
}

// DefinitionReference binds a run to an exact or currently active revision and optional hash.
type DefinitionReference struct {
	ID       string `json:"id"`
	Revision int    `json:"revision,omitempty"`
	Hash     string `json:"hash,omitempty"`
}

// ActivateDefinitionRequest updates one scoped Definition head under version CAS.
type ActivateDefinitionRequest struct {
	Scope           DefinitionScope
	DefinitionID    string
	TargetRevision  int
	Availability    DefinitionAvailability
	ExpectedVersion uint64
}

// NewDefinitionRegistry creates a Runtime-owned Definition registry.
func NewDefinitionRegistry(store DefinitionStore, clock kernel.Clock) (*DefinitionRegistry, error) {
	if store == nil {
		return nil, ErrInvalidDefinitionRegistry
	}
	if clock == nil {
		clock = definitionSystemClock{}
	}
	return &DefinitionRegistry{store: store, clock: clock}, nil
}

// Descriptor declares the independently composed Definition Registry capability.
func (registry *DefinitionRegistry) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{
		Name:     "workflow-definition-registry",
		Provides: []kernel.Capability{CapabilityDefinitionRegistry},
	}
}

// Publish compiles and atomically stores the next immutable Definition revision.
func (registry *DefinitionRegistry) Publish(
	ctx context.Context,
	request PublishDefinitionRequest,
) (DefinitionRevision, DefinitionHead, bool, error) {
	if registry == nil || registry.store == nil || request.ExpectedRevision < 0 {
		return DefinitionRevision{}, DefinitionHead{}, false, ErrInvalidDefinitionRegistry
	}
	scope, err := PrepareDefinitionScope(request.Scope)
	if err != nil {
		return DefinitionRevision{}, DefinitionHead{}, false, err
	}
	request.Draft.Revision = request.ExpectedRevision + 1
	definition, err := CompileDefinition(request.Draft)
	if err != nil {
		return DefinitionRevision{}, DefinitionHead{}, false, err
	}
	mode := DefinitionPublishMode(strings.TrimSpace(string(request.Mode)))
	if mode == "" {
		mode = PublishAndActivate
	}
	revision := DefinitionRevision{
		Scope: scope, Definition: definition,
		PublishedBy:    strings.TrimSpace(request.PublishedBy),
		IdempotencyKey: strings.TrimSpace(request.IdempotencyKey),
		PublishedAt:    registry.clock.Now().UTC(),
	}
	revision.RequestFingerprint, err = definitionPublishFingerprint(
		revision, request.ExpectedRevision, mode,
	)
	if err != nil {
		return DefinitionRevision{}, DefinitionHead{}, false, err
	}
	mutation, err := PrepareDefinitionPublishMutation(DefinitionPublishMutation{
		Revision: revision, ExpectedRevision: request.ExpectedRevision, Mode: mode,
	})
	if err != nil {
		return DefinitionRevision{}, DefinitionHead{}, false, err
	}
	return registry.store.Publish(ctx, mutation)
}

// Get returns an exact historical revision even when its head is disabled.
func (registry *DefinitionRegistry) Get(
	ctx context.Context,
	scope DefinitionScope,
	definitionID string,
	revision int,
) (DefinitionRevision, error) {
	if registry == nil || registry.store == nil || revision <= 0 {
		return DefinitionRevision{}, ErrInvalidDefinitionRegistry
	}
	normalized, err := PrepareDefinitionScope(scope)
	if err != nil {
		return DefinitionRevision{}, err
	}
	return registry.store.GetRevision(ctx, normalized, strings.TrimSpace(definitionID), revision)
}

// ResolveForStart resolves the most-specific visible active Definition for a new run.
func (registry *DefinitionRegistry) ResolveForStart(
	ctx context.Context,
	visibility DefinitionScope,
	reference DefinitionReference,
) (DefinitionRevision, error) {
	if registry == nil || registry.store == nil {
		return DefinitionRevision{}, ErrInvalidDefinitionRegistry
	}
	reference.ID = strings.TrimSpace(reference.ID)
	reference.Hash = strings.TrimSpace(reference.Hash)
	if reference.ID == "" || reference.Revision < 0 {
		return DefinitionRevision{}, ErrInvalidDefinitionRegistry
	}
	scopes, err := VisibleDefinitionScopes(visibility)
	if err != nil {
		return DefinitionRevision{}, err
	}
	for _, scope := range scopes {
		head, loadErr := registry.store.GetHead(ctx, scope, reference.ID)
		if errors.Is(loadErr, ErrDefinitionNotFound) {
			continue
		}
		if loadErr != nil {
			return DefinitionRevision{}, loadErr
		}
		if head.Availability != DefinitionActive || head.ActiveRevision <= 0 {
			return DefinitionRevision{}, ErrDefinitionDisabled
		}
		revision := reference.Revision
		if revision == 0 {
			revision = head.ActiveRevision
		}
		published, loadErr := registry.store.GetRevision(ctx, scope, reference.ID, revision)
		if loadErr != nil {
			return DefinitionRevision{}, loadErr
		}
		if reference.Hash != "" && published.Definition.Hash != reference.Hash {
			return DefinitionRevision{}, ErrDefinitionHash
		}
		return published, nil
	}
	return DefinitionRevision{}, ErrDefinitionNotFound
}

// ListVisible returns one deterministic, most-specific head per Definition identity.
func (registry *DefinitionRegistry) ListVisible(
	ctx context.Context,
	visibility DefinitionScope,
) ([]DefinitionHead, error) {
	if registry == nil || registry.store == nil {
		return nil, ErrInvalidDefinitionRegistry
	}
	scopes, err := VisibleDefinitionScopes(visibility)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	result := make([]DefinitionHead, 0)
	for _, scope := range scopes {
		heads, listErr := registry.store.ListHeads(ctx, scope)
		if listErr != nil {
			return nil, listErr
		}
		for _, head := range heads {
			if _, exists := seen[head.DefinitionID]; exists {
				continue
			}
			seen[head.DefinitionID] = struct{}{}
			result = append(result, head)
		}
	}
	SortDefinitionHeads(result)
	return result, nil
}

// SetActivation atomically activates one revision or disables new resolution.
func (registry *DefinitionRegistry) SetActivation(
	ctx context.Context,
	request ActivateDefinitionRequest,
) (DefinitionHead, bool, error) {
	if registry == nil || registry.store == nil {
		return DefinitionHead{}, false, ErrInvalidDefinitionRegistry
	}
	mutation, err := PrepareDefinitionActivationMutation(DefinitionActivationMutation{
		Scope: request.Scope, DefinitionID: request.DefinitionID,
		TargetRevision: request.TargetRevision, Availability: request.Availability,
		ExpectedVersion: request.ExpectedVersion, UpdatedAt: registry.clock.Now().UTC(),
	})
	if err != nil {
		return DefinitionHead{}, false, err
	}
	return registry.store.SetActivation(ctx, mutation)
}

// PrepareDefinitionScope normalizes and validates one visibility scope.
func PrepareDefinitionScope(scope DefinitionScope) (DefinitionScope, error) {
	scope.Kind = DefinitionScopeKind(strings.TrimSpace(string(scope.Kind)))
	scope.TenantID = strings.TrimSpace(scope.TenantID)
	scope.ActorID = strings.TrimSpace(scope.ActorID)
	switch scope.Kind {
	case DefinitionScopeSystem:
		if scope.TenantID != "" || scope.ActorID != "" {
			return DefinitionScope{}, ErrInvalidDefinitionRegistry
		}
	case DefinitionScopeTenant:
		if scope.TenantID == "" || scope.ActorID != "" {
			return DefinitionScope{}, ErrInvalidDefinitionRegistry
		}
	case DefinitionScopeActor:
		if scope.TenantID == "" || scope.ActorID == "" {
			return DefinitionScope{}, ErrInvalidDefinitionRegistry
		}
	default:
		return DefinitionScope{}, ErrInvalidDefinitionRegistry
	}
	return scope, nil
}

// VisibleDefinitionScopes returns most-specific to least-specific resolution order.
func VisibleDefinitionScopes(scope DefinitionScope) ([]DefinitionScope, error) {
	normalized, err := PrepareDefinitionScope(scope)
	if err != nil {
		return nil, err
	}
	system := DefinitionScope{Kind: DefinitionScopeSystem}
	switch normalized.Kind {
	case DefinitionScopeSystem:
		return []DefinitionScope{system}, nil
	case DefinitionScopeTenant:
		return []DefinitionScope{normalized, system}, nil
	case DefinitionScopeActor:
		return []DefinitionScope{
			normalized,
			{Kind: DefinitionScopeTenant, TenantID: normalized.TenantID},
			system,
		}, nil
	default:
		return nil, ErrInvalidDefinitionRegistry
	}
}

// DefinitionScopeKey returns a collision-safe adapter key.
func DefinitionScopeKey(scope DefinitionScope) string {
	return string(scope.Kind) + "\x00" + scope.TenantID + "\x00" + scope.ActorID
}

// PrepareDefinitionPublishMutation validates the atomic persistence command.
func PrepareDefinitionPublishMutation(
	mutation DefinitionPublishMutation,
) (DefinitionPublishMutation, error) {
	revision, err := PrepareDefinitionRevision(mutation.Revision)
	if err != nil {
		return DefinitionPublishMutation{}, err
	}
	mutation.Revision = revision
	mutation.Mode = DefinitionPublishMode(strings.TrimSpace(string(mutation.Mode)))
	if mutation.ExpectedRevision < 0 || revision.Definition.Revision != mutation.ExpectedRevision+1 ||
		(mutation.Mode != PublishAndActivate && mutation.Mode != PublishStaged) ||
		(mutation.ExpectedRevision == 0 && mutation.Mode != PublishAndActivate) {
		return DefinitionPublishMutation{}, ErrInvalidDefinitionRegistry
	}
	return mutation, nil
}

// PrepareDefinitionRevision validates one adapter-facing immutable revision.
func PrepareDefinitionRevision(revision DefinitionRevision) (DefinitionRevision, error) {
	scope, err := PrepareDefinitionScope(revision.Scope)
	if err != nil {
		return DefinitionRevision{}, err
	}
	revision.Scope = scope
	revision.PublishedBy = strings.TrimSpace(revision.PublishedBy)
	revision.IdempotencyKey = strings.TrimSpace(revision.IdempotencyKey)
	revision.RequestFingerprint = strings.TrimSpace(revision.RequestFingerprint)
	revision.PublishedAt = revision.PublishedAt.UTC()
	revision.Definition = cloneDefinition(revision.Definition)
	if revision.PublishedBy == "" || revision.IdempotencyKey == "" ||
		revision.RequestFingerprint == "" || revision.PublishedAt.IsZero() ||
		ValidateDefinition(revision.Definition) != nil {
		return DefinitionRevision{}, ErrInvalidDefinitionRegistry
	}
	return revision, nil
}

// PrepareDefinitionActivationMutation validates a head CAS mutation.
func PrepareDefinitionActivationMutation(
	mutation DefinitionActivationMutation,
) (DefinitionActivationMutation, error) {
	scope, err := PrepareDefinitionScope(mutation.Scope)
	if err != nil {
		return DefinitionActivationMutation{}, err
	}
	mutation.Scope = scope
	mutation.DefinitionID = strings.TrimSpace(mutation.DefinitionID)
	mutation.Availability = DefinitionAvailability(strings.TrimSpace(string(mutation.Availability)))
	mutation.UpdatedAt = mutation.UpdatedAt.UTC()
	if mutation.DefinitionID == "" || mutation.ExpectedVersion == 0 || mutation.UpdatedAt.IsZero() {
		return DefinitionActivationMutation{}, ErrInvalidDefinitionRegistry
	}
	if mutation.Availability == DefinitionActive && mutation.TargetRevision <= 0 {
		return DefinitionActivationMutation{}, ErrInvalidDefinitionRegistry
	}
	if mutation.Availability == DefinitionDisabled {
		mutation.TargetRevision = 0
		return mutation, nil
	}
	if mutation.Availability != DefinitionActive {
		return DefinitionActivationMutation{}, ErrInvalidDefinitionRegistry
	}
	return mutation, nil
}

// CloneDefinitionRevision returns an isolated copy for adapter boundaries.
func CloneDefinitionRevision(revision DefinitionRevision) DefinitionRevision {
	revision.Definition = cloneDefinition(revision.Definition)
	return revision
}

// SortDefinitionHeads applies the public deterministic order.
func SortDefinitionHeads(heads []DefinitionHead) {
	sort.Slice(heads, func(left, right int) bool {
		if heads[left].DefinitionID == heads[right].DefinitionID {
			return DefinitionScopeKey(heads[left].Scope) < DefinitionScopeKey(heads[right].Scope)
		}
		return heads[left].DefinitionID < heads[right].DefinitionID
	})
}

func definitionPublishFingerprint(
	revision DefinitionRevision,
	expectedRevision int,
	mode DefinitionPublishMode,
) (string, error) {
	encoded, err := json.Marshal(struct {
		Scope            DefinitionScope       `json:"scope"`
		DefinitionHash   string                `json:"definitionHash"`
		ExpectedRevision int                   `json:"expectedRevision"`
		Mode             DefinitionPublishMode `json:"mode"`
		PublishedBy      string                `json:"publishedBy"`
	}{revision.Scope, revision.Definition.Hash, expectedRevision, mode, revision.PublishedBy})
	if err != nil {
		return "", errors.Join(ErrInvalidDefinitionRegistry, err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

type definitionSystemClock struct{}

func (definitionSystemClock) Now() time.Time { return time.Now() }
