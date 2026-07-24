package postgres

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime-postgres/models"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const valueActiveC374515E = "active"

const (
	valueText572DB41F          = "text"
	migrationTestSourceTextRun = "text_run"
	migrationTestOldValue      = "old"
)

func migrateAgentRuntimeTest(db *gorm.DB, vectorRequired ...bool) error {
	required := false
	if len(vectorRequired) > 0 {
		required = vectorRequired[0]
	}
	_ = required
	return Migrate(db)
}

func openConversationPostgresContractDB(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	schema := "agent_runtime_contract_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err = base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create contract schema: %v", err)
	}
	schemaDSN, err := postgresDSNWithSearchPath(dsn, schema)
	if err != nil {
		t.Fatalf("set postgres search path: %v", err)
	}
	db, err := gorm.Open(postgres.Open(schemaDSN), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres contract schema: %v", err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	testSQL, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = testSQL.Close()
		_ = base.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		_ = baseSQL.Close()
	})
	return db
}

func postgresDSNWithSearchPath(dsn string, schema string) (string, error) {
	if !strings.Contains(dsn, "://") {
		return fmt.Sprintf("%s search_path=%s,public", dsn, schema), nil
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func openConversationRepositoryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	if err := db.AutoMigrate(&model.RunRecord{}, &model.EventRecord{}, &model.RunStep{}, &model.RuntimePlanRecord{}, &model.RunInteraction{}, &model.RunCheckpoint{}, &model.RuntimeOutputIdentityRecord{}, &model.RuntimeOutputRefRecord{}, &model.RuntimeWorkbenchProjectionRecord{}, &model.RuntimePhaseProjectionRecord{}, &model.EvidenceSelection{}, &model.RunQueueItemRecord{}, &model.ContinuationJobRecord{}, &model.ContextRecord{}); err != nil {
		t.Fatalf("migrate models: %v", err)
	}
	return db
}

func newTestRepository(t *testing.T) *Repository {
	t.Helper()
	db := openConversationRepositoryTestDB(t)
	return New(db, StaticSessions(db))
}
