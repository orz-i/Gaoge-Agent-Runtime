package kernel

import (
	"context"
)

func (r *Runtime) SupportsOwnedRetirement() bool {
	if r == nil {
		return false
	}
	_, ok := r.store.(OwnedRetirementStore)
	return ok
}
func (r *Runtime) LoadOwnedRetirement(ctx context.Context, id string) (OwnedRetirementReceipt, error) {
	if r == nil || ctx == nil || id == "" {
		return OwnedRetirementReceipt{}, ErrInvalidInput
	}
	s, ok := r.store.(OwnedRetirementStore)
	if !ok {
		return OwnedRetirementReceipt{}, ErrExecutionRequired
	}
	v, e := s.LoadOwnedRetirement(ctx, id)
	if e != nil {
		return v, e
	}
	if !v.Valid() || v.Evidence.Grant.ID != id {
		return OwnedRetirementReceipt{}, ErrExecutionConflict
	}
	return v, nil
}
func (r *Runtime) LookupOwnedRetirementForRun(ctx context.Context, runID string) (OwnedRetirementReceipt, error) {
	if r == nil || ctx == nil || runID == "" {
		return OwnedRetirementReceipt{}, ErrInvalidInput
	}
	s, ok := r.store.(OwnedRetirementStore)
	if !ok {
		return OwnedRetirementReceipt{}, ErrExecutionRequired
	}
	v, e := s.LookupOwnedRetirementForRun(ctx, runID)
	if e != nil {
		return v, e
	}
	if !v.Valid() || v.Evidence.Grant.ChildRunID != runID {
		return OwnedRetirementReceipt{}, ErrExecutionConflict
	}
	return v, nil
}
