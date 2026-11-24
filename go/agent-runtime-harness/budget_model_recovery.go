package harness

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// VerifyModelOutcome is an explicitly composed, read-only source of the
// original ledger receipt. It neither registers ancestry nor reserves, bills,
// projects, dispatches, or invokes a middleware chain. The request is the
// exact frozen physical input to the Provider, after all sampling restrictions.
func (authority *BudgetAuthority) VerifyModelOutcome(ctx context.Context, request model.Request) (model.Response, error) {
	if authority == nil || strings.TrimSpace(request.RunID) == "" || strings.TrimSpace(request.InvocationID) == "" {
		return model.Response{}, ErrInvalidRequest
	}
	turn, _, err := resolveContextTurnForRun(ctx, authority.dependencies.Store, authority.dependencies.Relations, request.RunID)
	if err != nil {
		return model.Response{}, err
	}
	config, err := loadSealedConfig(ctx, authority.dependencies.Store, turn.ConfigSnapshotID)
	if err != nil || config.SharedBudget == nil {
		return model.Response{}, errors.Join(ErrConflict, err)
	}
	ledger, err := authority.dependencies.Ledgers.LoadBudget(ctx, turn.ID)
	if err != nil {
		return model.Response{}, err
	}
	if ledger.ID != turn.ID {
		return model.Response{}, ErrConflict
	}
	if _, ok := ledger.Runs[request.RunID]; !ok {
		return model.Response{}, ErrConflict
	}
	hash, err := model.PhysicalRequestHash(request)
	if err != nil {
		return model.Response{}, err
	}
	id := "model:" + request.InvocationID
	receipt, ok := ledger.Reservations[id]
	if !ok || receipt.ID != id || receipt.RunID != request.RunID || receipt.PhysicalRequestHash != hash || receipt.Status != budget.ReservationSettled || !receipt.Dispatched || len(receipt.Receipt) == 0 {
		return model.Response{}, ErrConflict
	}
	return decodeBudgetModelReceipt(receipt.Receipt)
}
