package budget

import (
	"context"
	"encoding/json"
)

// SettleWithReceipts atomically settles a dispatched call and transfers its
// unfinished effects to separate durable receipts. A later authoritative result
// can settle those held receipts without ever releasing uncertain work between
// two writes. Related receipts belong to the same Run and cannot be downgraded.
func (coordinator Coordinator) SettleWithReceipts(ctx context.Context, scopeID, id string, usage Usage, receipt json.RawMessage, related []Reservation) (Ledger, error) {
	if !ValidUsage(usage) || !json.Valid(receipt) {
		return Ledger{}, ErrInvalidUsage
	}
	return coordinator.update(ctx, scopeID, func(ledger *Ledger) error {
		current, found := ledger.Reservations[id]
		if !found || !current.Dispatched || current.Status != ReservationHeld && current.Status != ReservationSettled {
			return ErrLedgerConflict
		}
		if current.Status == ReservationSettled {
			if current.Usage != usage || string(current.Receipt) != string(receipt) {
				return ErrLedgerConflict
			}
			return nil // A replay must not reintroduce already reconciled effects.
		}
		seen := map[string]bool{id: true}
		for _, next := range related {
			if next.ID == "" || seen[next.ID] || next.RunID != current.RunID || next.RequestHash == "" || !ValidUsage(next.Requested) || next.Usage != (Usage{}) || !json.Valid(next.Receipt) || !next.Dispatched || next.Active || next.Status != ReservationHeld && next.Status != ReservationSettled {
				return ErrInvalidUsage
			}
			seen[next.ID] = true
			if previous, exists := ledger.Reservations[next.ID]; exists {
				if previous.RunID != next.RunID || previous.RequestHash != next.RequestHash || previous.Requested != next.Requested || previous.Status != ReservationHeld && previous.Status != ReservationSettled {
					return ErrLedgerConflict
				}
				if previous.Status == ReservationSettled {
					continue
				}
			}
			next.Receipt = append(json.RawMessage(nil), next.Receipt...)
			ledger.Reservations[next.ID] = next
		}
		current.Status, current.Active = ReservationSettled, false
		current.Usage, current.Receipt = usage, append(json.RawMessage(nil), receipt...)
		ledger.Reservations[id] = current
		return nil
	})
}
