package kernel

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Only a validated native allowance CAS can create this journal record.
const allowanceDispatchEvent = "kernel.allowance.dispatch_committed"

type allowanceDispatchKey struct{}
type allowanceDispatch struct {
	id, run    string
	revision   uint64
	sourceHead int64
}

// NewAllowanceDispatch mints an original identifier before the host persists its
// dispatch. The journal offset is for lookup. The random nonce binds that grant.
func NewAllowanceDispatch(ctx context.Context, source Snapshot) (context.Context, string, error) {
	if ctx == nil || source.Run.ID == "" || source.Run.Revision == 0 || source.Run.Status != RunStatusPausedBudget || source.EventHead < 0 || source.EventHead == math.MaxInt64 {
		return ctx, "", ErrInvalidInput
	}
	id := "hdispatch_" + rand.Text() + "_" + strconv.FormatInt(source.EventHead, 10)
	scope := allowanceDispatch{id: id, run: source.Run.ID, revision: source.Run.Revision, sourceHead: source.EventHead}
	return context.WithValue(ctx, allowanceDispatchKey{}, scope), id, nil
}
func stampAllowanceDispatch(ctx context.Context, current Snapshot, next *StoreMutation) error {
	scope, ok := ctx.Value(allowanceDispatchKey{}).(allowanceDispatch)
	if !ok {
		return nil
	}
	if scope.run != current.Run.ID || scope.revision != current.Run.Revision || scope.sourceHead != current.EventHead {
		return ErrConflict
	}
	marker := EventDraft{Type: allowanceDispatchEvent, Message: scope.id, Data: json.RawMessage(strconv.FormatUint(scope.revision, 10))}
	next.Events = append([]EventDraft{marker}, next.Events...)
	return nil
}

// CommittedAllowanceDispatch inspects the single original Run journal slot. A
// missing receipt or one for another grant leaves the physical dispatch pending.
func (r *Runtime) CommittedAllowanceDispatch(ctx context.Context, run, id string) (bool, error) {
	if r == nil || ctx == nil || run == "" || id == "" {
		return false, ErrInvalidInput
	}
	split := strings.LastIndexByte(id, '_')
	if split < 0 {
		return false, nil
	}
	head, err := strconv.ParseInt(id[split+1:], 10, 64)
	if err != nil || head < 0 || head == math.MaxInt64 {
		return false, nil
	}
	current, err := r.Load(ctx, run)
	if err != nil || current.EventHead <= head {
		return false, err
	}
	events, err := r.ListEvents(ctx, run, head, 1)
	if err != nil || len(events) != 1 {
		return false, err
	}
	event := events[0]
	if event.Seq != head+1 || event.Type != allowanceDispatchEvent || event.Message != id {
		return false, nil
	}
	var source uint64
	if json.Unmarshal(event.Data, &source) != nil || source == 0 || source >= current.Run.Revision {
		return false, ErrApplyOutcomeUnknown
	}
	return true, nil
}
