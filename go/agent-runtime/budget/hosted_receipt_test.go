package budget

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestHostedReceiptWithoutIdentityRejectsTerminalCounts(t *testing.T) {
	for _, status := range []string{"completed", "failed", "error", "cancelled", "canceled"} {
		t.Run(status, func(t *testing.T) {
			calls := []model.HostedToolCall{
				{ToolKey: "native.search", Status: status},
				{ToolKey: "native.search", Status: status},
			}
			count, receipts, err := hostedReceiptTransfers(Ledger{}, "root", calls)
			if !errors.Is(err, ErrInvalidUsage) || count != 0 || len(receipts) != 0 {
				t.Fatalf("anonymous terminal facts certified a duplicate charge: count=%d receipts=%+v err=%v",
					count, receipts, err)
			}
		})
	}
	for _, status := range []string{"", "in_progress", "requested", "provider_unknown"} {
		calls := []model.HostedToolCall{{ToolKey: "native.search", Status: status}}
		count, receipts, err := hostedReceiptTransfers(Ledger{}, "root", calls)
		if !errors.Is(err, ErrInvalidUsage) || count != 0 || len(receipts) != 0 {
			t.Fatalf("anonymous nonterminal %q fabricated a receipt: count=%d receipts=%+v err=%v", status, count, receipts, err)
		}
	}
}
