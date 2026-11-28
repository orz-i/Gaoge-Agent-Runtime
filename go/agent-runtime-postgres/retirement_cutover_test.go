package postgres

import (
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-postgres/models"
)

func TestRetirementCutoverRejectsUnfinishedHistory(t *testing.T) {
	db := openKernelStoreTestDB(t)
	row := models.KernelOwnedRetirementRecord{GrantID: "old", RunID: "original", SettledRevision: 1, Version: 1, Phase: "RETIRED", Payload: "{}"}
	if e := db.Create(&row).Error; e != nil {
		t.Fatal(e)
	}
	if e := Migrate(db); e == nil || !strings.Contains(e.Error(), "unfinished original retirements") {
		t.Fatalf("old intermediate admitted: %v", e)
	}
	var after models.KernelOwnedRetirementRecord
	if e := db.Where("grant_id = ?", row.GrantID).First(&after).Error; e != nil || after != row {
		t.Fatalf("old evidence changed: %+v %v", after, e)
	}
}
