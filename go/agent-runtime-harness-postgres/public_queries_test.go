package harnesspostgres

import (
	"encoding/json"
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

func TestPublicTurnConfigTenantAndActorScope(t *testing.T) {
	store, db := publicQueryStore(t)
	for _, id := range []string{"owned", "foreign-tenant", "foreign-actor"} {
		tenant, actor := "tenant", "actor"
		if id == "foreign-tenant" {
			tenant = "other"
		}
		if id == "foreign-actor" {
			actor = "other"
		}
		raw, err := json.Marshal(harness.ConfigSnapshot{ID: "config-" + id, TurnID: "turn-" + id, Model: "model-" + id})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range []any{
			&sessionRecord{ID: id, TenantID: tenant, ActorID: actor, HostThreadKind: "chat", HostThreadID: id},
			&turnRecord{ID: "turn-" + id, SessionID: id, Status: "completed", ConfigSnapshotID: "config-" + id},
			&configRecord{ID: "config-" + id, TurnID: "turn-" + id, ContentHash: id, PayloadJSON: string(raw)},
		} {
			if err = db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	values, err := store.ListTurnConfigs(t.Context(), TurnConfigQuery{TenantID: "tenant", ActorID: "actor", TurnIDs: []string{"turn-owned", "turn-foreign-tenant", "turn-foreign-actor"}})
	if err != nil || len(values) != 1 || values[0].Config.Model != "model-owned" {
		t.Fatalf("values=%v err=%v", values, err)
	}
}
