package harness

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

type BudgetAuthorityDependencies struct {
	Store     Store
	Ledgers   budget.LedgerStore
	Relations ContextRunRelationSource
	Meter     model.TokenAdmissionPlanner
	Clock     Clock
	TurnFeed  *TurnFeed
}

// BudgetAuthority is the Host-owned ledger and original-receipt authority.
// It NEVER implements Run/Model/Tool middleware: the sole physical execution
// driver is the native agent.BoundedBudgetAdapter, composed via Adapter().
type BudgetAuthority struct {
	dependencies BudgetAuthorityDependencies
	adapterOnce  sync.Once
	adapter      *agent.BoundedBudgetAdapter
	adapterErr   error
}

func NewBudgetAuthority(dependencies BudgetAuthorityDependencies) (*BudgetAuthority, error) {
	if dependencies.Ledgers == nil {
		dependencies.Ledgers, _ = dependencies.Store.(budget.LedgerStore)
	}
	if dependencies.Store == nil || dependencies.Ledgers == nil || dependencies.Clock == nil {
		return nil, ErrInvalidRequest
	}
	return &BudgetAuthority{dependencies: dependencies}, nil
}

func (authority *BudgetAuthority) coordinator() budget.Coordinator {
	return budget.Coordinator{Store: authority.dependencies.Ledgers}
}

// AdmitRemote admits the child count but forbids a remote execution from
// bypassing token limits when the protocol has no enforceable usage contract.
func (authority *BudgetAuthority) AdmitRemote(ctx context.Context, runID string) error {
	ledger, err := authority.bindRun(ctx, runID)
	if err != nil || ledger.ID == "" {
		return err
	}
	if ledger.HasRunTokenLimits(runID) {
		return budget.ErrUnmetered
	}
	return authority.project(ctx, ledger)
}

func (authority *BudgetAuthority) resolveScope(ctx context.Context, runID string) (agent.BoundedBudgetScope, error) {
	d := authority.dependencies
	turn, _, err := resolveContextTurnForRun(ctx, d.Store, d.Relations, runID)
	if errors.Is(err, ErrNotFound) {
		return agent.BoundedBudgetScope{}, nil
	}
	if err != nil {
		return agent.BoundedBudgetScope{}, err
	}
	config, err := loadSealedConfig(ctx, d.Store, turn.ConfigSnapshotID)
	if err != nil || config.SharedBudget == nil {
		return agent.BoundedBudgetScope{}, err
	}
	scope := agent.BoundedBudgetScope{LedgerID: turn.ID, Limits: *config.SharedBudget}
	existing, err := d.Ledgers.LoadBudget(ctx, turn.ID)
	if err != nil && !errors.Is(err, budget.ErrLedgerNotFound) {
		return scope, err
	}
	seen := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if seen[id] {
			return ErrConflict
		}
		seen[id] = true
		if binding, ok := existing.Runs[id]; ok {
			// Existing bindings already carry their admitted ancestry and limits.
			scope.Runs = append(scope.Runs, agent.BoundedBudgetRunBinding{RunID: id, Binding: binding})
			return nil
		}
		parent, found, err := contextParentRunID(ctx, d.Relations, id)
		if err != nil {
			return err
		}
		if found {
			if err = visit(parent); err != nil {
				return err
			}
		}
		scope.Runs = append(scope.Runs, agent.BoundedBudgetRunBinding{RunID: id, Binding: budget.RunBudget{ParentRunID: parent}})
		return nil
	}
	if err = visit(runID); err != nil {
		return scope, err
	}
	return scope, nil
}

func (authority *BudgetAuthority) bindRun(ctx context.Context, runID string) (budget.Ledger, error) {
	scope, err := authority.resolveScope(ctx, runID)
	if err != nil || scope.LedgerID == "" {
		return budget.Ledger{}, err
	}
	coordinator := authority.coordinator()
	ledger, err := coordinator.Ensure(ctx, scope.LedgerID, scope.Limits)
	if err != nil {
		return ledger, err
	}
	for _, run := range scope.Runs {
		if original, exists := ledger.Runs[run.RunID]; exists {
			if original != run.Binding {
				return ledger, budget.ErrLedgerConflict
			}
			continue
		}
		ledger, err = coordinator.RegisterRun(ctx, ledger.ID, run.RunID, run.Binding)
		if err != nil {
			return ledger, err
		}
	}
	if _, exists := ledger.Runs[runID]; !exists {
		return ledger, ErrConflict
	}
	return ledger, nil
}

// previewModelReservation computes all Tool visibility restrictions against the
// ledger that would hold this reservation. It does not meter or write quota.
func (authority *BudgetAuthority) previewModelReservation(ledger budget.Ledger, request model.Request) (model.Request, budget.Reservation, error) {
	hash, err := model.PhysicalRequestHash(request)
	if err != nil {
		return request, budget.Reservation{}, err
	}
	reservation := budget.Reservation{ID: "model:" + request.InvocationID, RunID: request.RunID, RequestHash: hash, Requested: budget.Usage{LLMCalls: 1}}
	if existing, found := ledger.Reservations[reservation.ID]; found {
		if existing.RequestHash != reservation.RequestHash || existing.RunID != reservation.RunID {
			return request, reservation, budget.ErrLedgerConflict
		}
		reservation = existing
		if ceiling := existing.Requested.OutputTokens; ceiling > 0 &&
			(request.MaxOutputTokens == 0 || ceiling < request.MaxOutputTokens) {
			request.MaxOutputTokens = ceiling
		}
		request = applyHostedReservation(request, existing.Requested.HostedToolCalls, ledger, existing.RunID)
	} else {
		request, reservation = reserveHostedModelTools(request, reservation, ledger)
		if ledger.HasRunTokenLimits(request.RunID) {
			// A known output-only limit can be applied even before input is
			// measured. The total-token ceiling is finalized after counting.
			ceiling := request.MaxOutputTokens
			if ceiling == 0 {
				ceiling = math.MaxInt64
			}
			ceiling = remainingOutputCeiling(ledger, request.RunID, 0, ceiling)
			// A zero remaining quota is a ledger admission fact, not a
			// malformed request or failed Host callback. Keep a valid shape
			// for the Runtime's pure reservation decision.
			if ceiling > 0 && ceiling != math.MaxInt64 {
				request.MaxOutputTokens = ceiling
			}
		}
	}
	// Measure exactly the Tool set that would be visible after this atomic
	// reservation, never an intermediate request followed by a late filter.
	if reservation.Status != budget.ReservationSettled && reservation.Status != budget.ReservationReleased {
		projected := budget.CloneLedger(ledger)
		held := reservation
		held.Status, held.Active = budget.ReservationHeld, true
		projected.Reservations[held.ID] = held
		request = applyLocalToolBudget(request, projected, request.RunID)
	}
	return request, reservation, nil
}

func (authority *BudgetAuthority) modelReservation(ctx context.Context, ledger budget.Ledger, original model.Request) (model.Request, budget.Reservation, error) {
	request, reservation, err := authority.previewModelReservation(ledger, original)
	if err != nil || !ledger.HasRunTokenLimits(original.RunID) {
		return request, reservation, err
	}
	if reservation.Status == budget.ReservationSettled || reservation.Dispatched {
		return request, reservation, nil
	}
	if authority.dependencies.Meter == nil {
		return request, reservation, budget.ErrUnmetered
	}
	// Existing reservations are immutable. A fresh reservation may tighten
	// the output ceiling, but its final request is measured again if it did.
	if reservation.Status != "" {
		admission, admissionErr := authority.dependencies.Meter.PlanTokenAdmission(ctx, model.CloneRequest(request))
		if admissionErr != nil {
			return request, reservation, admissionErr
		}
		if admission.InputUpperBound <= 0 || admission.InputUpperBound > reservation.Requested.InputTokens ||
			admission.MaxOutputTokens < reservation.Requested.OutputTokens {
			return request, reservation, budget.ErrUnmetered
		}
		return request, reservation, nil
	}
	for attempt := 0; attempt < 16; attempt++ {
		if err := ctx.Err(); err != nil {
			return request, reservation, err
		}
		admission, err := authority.dependencies.Meter.PlanTokenAdmission(ctx, model.CloneRequest(request))
		if err != nil {
			return request, reservation, err
		}
		if admission.InputUpperBound <= 0 || admission.MaxOutputTokens <= 0 {
			return request, reservation, budget.ErrUnmetered
		}
		output := admission.MaxOutputTokens
		if request.MaxOutputTokens > 0 {
			output = min(output, request.MaxOutputTokens)
		}
		// Tighten only to an executable positive ceiling. If even that
		// cannot fit, return a fully measured candidate; the same ledger
		// admission rule used at Reserve determines its disposition.
		if affordable := remainingOutputCeiling(ledger, original.RunID, admission.InputUpperBound, output); affordable > 0 {
			output = affordable
		}
		if request.MaxOutputTokens != output {
			request.MaxOutputTokens = output
			continue
		}
		if admission.InputUpperBound > math.MaxInt64-output {
			return request, reservation, budget.ErrInvalidUsage
		}
		reservation.Requested.InputTokens = admission.InputUpperBound
		reservation.Requested.OutputTokens = output
		reservation.Requested.TotalTokens = admission.InputUpperBound + output
		if !budget.ValidUsage(reservation.Requested) {
			return request, reservation, budget.ErrInvalidUsage
		}
		return request, reservation, nil
	}
	return request, reservation, budget.ErrUnmetered
}

func (authority *BudgetAuthority) project(ctx context.Context, ledger budget.Ledger) error {
	if ledger.ID == "" {
		return nil
	}
	return projectBudgetItem(ctx, authority.dependencies.Store, authority.dependencies.TurnFeed, authority.dependencies.Clock, ledger)
}

func projectBudgetItem(ctx context.Context, store Store, feed *TurnFeed, clock Clock, ledger budget.Ledger) error {
	payload, err := json.Marshal(ledger.View(""))
	if err != nil {
		return err
	}
	now := clock.Now().UTC()
	_, err = appendItemFact(ctx, store, feed, Item{ID: stableID("hbudget", ledger.ID, uintString(ledger.Revision)), TurnID: ledger.ID,
		Kind: ItemBudget, Status: ItemCompleted, Payload: payload, CreatedAt: now, UpdatedAt: now})
	return err
}

func remainingOutputCeiling(ledger budget.Ledger, runID string, input, output int64) int64 {
	for current := runID; ; current = ledger.Runs[current].ParentRunID {
		view := ledger.View(current)
		if view.Limits.MaxOutputTokens > 0 {
			output = min(output, view.Limits.MaxOutputTokens-view.Usage.OutputTokens)
		}
		if view.Limits.MaxTotalTokens > 0 {
			output = min(output, view.Limits.MaxTotalTokens-view.Usage.TotalTokens-input)
		}
		if current == "" {
			return output
		}
	}
}
