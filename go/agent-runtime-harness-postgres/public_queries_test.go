package harnesspostgres

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func publicQueryStore(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	dialector := sqlite.Open(filepath.Join(t.TempDir(), "queries.db"))
	if dsn := os.Getenv("TEST_POSTGRES_DSN"); dsn != "" {
		admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		schema := "public_queries_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		if err = admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
			sqlDB, _ := admin.DB()
			_ = sqlDB.Close()
		})
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := parsed.Query()
		q.Set("search_path", schema)
		parsed.RawQuery = q.Encode()
		dialector = postgres.Open(parsed.String())
	}
	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err = Migrate(db); err != nil {
		t.Fatal(err)
	}
	store, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return store, db
}

func TestPublicTurnConfigScopeAndFullActorErasure(t *testing.T) {
	store, db := publicQueryStore(t)
	for _, id := range []string{"owned", "foreign"} {
		tenant := "tenant"
		if id == "foreign" {
			tenant = "other"
		}
		raw, _ := json.Marshal(harness.ConfigSnapshot{ID: "config-" + id, TurnID: "turn-" + id, Model: "model-" + id})
		for _, row := range []any{
			&sessionRecord{ID: id, TenantID: tenant, ActorID: "actor", HostThreadKind: "chat", HostThreadID: id},
			&turnRecord{ID: "turn-" + id, SessionID: id, Status: "completed", ConfigSnapshotID: "config-" + id},
			&configRecord{ID: "config-" + id, TurnID: "turn-" + id, ContentHash: id, PayloadJSON: string(raw)},
			&contextCheckpointRecord{ID: "checkpoint-" + id, ScopeID: id},
			&contextHeadRecord{ScopeID: id, CheckpointID: "checkpoint-" + id},
			&contextArtifactRecord{ID: "artifact-" + id, ScopeID: id},
			&itemRecord{ID: "item-" + id, TurnID: "turn-" + id, Seq: 1},
			&budgetRecord{ID: "turn-" + id, PayloadJSON: `{"id":"turn-` + id + `"}`},
		} {
			if err := db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	values, err := store.ListTurnConfigs(t.Context(), TurnConfigQuery{TenantID: "tenant", ActorID: "actor", TurnIDs: []string{"turn-owned", "turn-foreign"}})
	if err != nil || len(values) != 1 || values[0].Config.Model != "model-owned" {
		t.Fatalf("values=%v err=%v", values, err)
	}
	if err = db.Create(&invocationRecord{ID: "pending", TurnID: "turn-owned", Status: "running"}).Error; err != nil {
		t.Fatal(err)
	}
	if err = store.EraseActorData(t.Context(), "tenant", "actor"); !errors.Is(err, ErrActorActive) {
		t.Fatalf("pending not protected: %v", err)
	}
	for _, row := range []any{
		&contextCheckpointRecord{ID: "child-checkpoint", ScopeID: "pending"},
		&contextHeadRecord{ScopeID: "pending", CheckpointID: "child-checkpoint"},
		&contextArtifactRecord{ID: "child-artifact", ScopeID: "pending"},
	} {
		if err = db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err = db.Model(&invocationRecord{}).Where("id = ?", "pending").Update("status", "completed").Error; err != nil {
		t.Fatal(err)
	}
	if err = store.EraseActorData(t.Context(), "tenant", "actor"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{&sessionRecord{}, &turnRecord{}, &configRecord{}, &contextCheckpointRecord{}, &contextHeadRecord{}, &contextArtifactRecord{}, &itemRecord{}, &budgetRecord{}} {
		var n int64
		if err = db.Model(row).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("%T remaining %d want foreign only", row, n)
		}
	}
	var n int64
	db.Model(&invocationRecord{}).Count(&n)
	if n != 0 {
		t.Fatal("orphan invocation")
	}
}
