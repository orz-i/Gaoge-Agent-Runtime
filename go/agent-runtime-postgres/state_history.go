package postgres

import (
	"encoding/json"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel/statehistory"
	"gorm.io/gorm"
)

func pgAppendHistory(tx *gorm.DB, id string, deltas []statehistory.Append) error {
	var rows []models.KernelStateHistoryRecord
	for _, d := range deltas {
		for i, item := range d.Entries {
			rows = append(rows, models.KernelStateHistoryRecord{RunID: id, Stream: d.Stream, Generation: d.Generation, Ordinal: d.First + i, PayloadJSON: string(item)})
		}
	}
	if len(rows) == 0 {
		return nil
	}
	return translateKernelApplyError(tx.CreateInBatches(&rows, 100).Error)
}
func pgReadHistory(db *gorm.DB, id string, stream int, gen uint64, count int) ([]json.RawMessage, error) {
	var rows []models.KernelStateHistoryRecord
	e := db.Where("run_id=? AND stream=? AND generation=? AND ordinal<?", id, stream, gen, count).Order("ordinal").Find(&rows).Error
	if e != nil {
		return nil, translateKernelError(e)
	}
	items := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		if row.Ordinal != len(items) {
			return nil, kernel.ErrExecutionConflict
		}
		items = append(items, json.RawMessage(row.PayloadJSON))
	}
	return items, nil
}
