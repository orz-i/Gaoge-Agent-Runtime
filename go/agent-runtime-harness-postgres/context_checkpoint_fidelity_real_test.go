package harnesspostgres_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRealPostgresContextCheckpointPreservesLosslessWindowHash(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	db, isolatedDSN := openIsolatedHarnessPostgres(t, dsn)
	if err := harnesspostgres.Migrate(db); err != nil {
		t.Fatal(err)
	}
	writer, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	open := func(number string) runtimecontext.Checkpoint {
		t.Helper()
		checkpoint, err := manager.Open(t.Context(), runtimecontext.OpenRequest{
			ScopeID: "postgres-lossless", StaticFingerprint: runtimecontext.StaticFingerprint("lossless"),
			Instructions: "instructions", SourcePath: []string{"user", "assistant", "tool", "replay"},
			Entries: []runtimecontext.Entry{
				{ID: "user", SourceID: "user", Message: model.Message{Role: model.RoleUser, Content: model.TextParts("question")}},
				{ID: "assistant", SourceID: "assistant", Message: model.Message{Role: model.RoleAssistant, ToolCalls: []tools.Call{{
					ID: "call", ToolKey: "lookup", Arguments: json.RawMessage(`{"n":` + number + `}`),
				}}}},
				{ID: "tool", SourceID: "tool", Message: model.Message{Role: model.RoleTool, ToolCallID: "call", Content: model.TextParts("result")}},
				{ID: "replay", SourceID: "replay", Message: model.Message{Role: model.RoleAssistant, ProviderReplay: &model.ProviderReplay{
					Provider: "provider", Format: "opaque", Data: json.RawMessage(`{"n":` + number + `}`),
				}}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return checkpoint
	}
	boundary := open("9007199254740992")
	adjacent := open("9007199254740993")
	if boundary.ID == adjacent.ID || boundary.ContentHash == adjacent.ContentHash {
		t.Fatalf("checkpoint identity collision before PostgreSQL: boundary=%+v adjacent=%+v", boundary, adjacent)
	}
	for _, checkpoint := range []runtimecontext.Checkpoint{boundary, adjacent} {
		if _, fresh, err := writer.PutContextCheckpoint(t.Context(), checkpoint); err != nil || !fresh {
			t.Fatalf("persist checkpoint=%s fresh=%v err=%v", checkpoint.ID, fresh, err)
		}
	}

	reopenedDB, err := gorm.Open(postgres.Open(isolatedDSN), realHarnessPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := reopenedDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	reader, err := harnesspostgres.New(reopenedDB)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []runtimecontext.Checkpoint{boundary, adjacent} {
		got, err := reader.GetContextCheckpoint(t.Context(), want.ID)
		if err != nil || !runtimecontext.ValidCheckpoint(got) ||
			got.ContentHashEncoding != runtimecontext.CheckpointContentHashEncodingLosslessJSON ||
			string(got.Window.Entries[1].Message.ToolCalls[0].Arguments) != string(want.Window.Entries[1].Message.ToolCalls[0].Arguments) ||
			string(got.Window.Entries[3].Message.ProviderReplay.Data) != string(want.Window.Entries[3].Message.ProviderReplay.Data) {
			t.Fatalf("PostgreSQL changed lossless checkpoint: got=%+v want=%+v err=%v", got, want, err)
		}
	}
}
