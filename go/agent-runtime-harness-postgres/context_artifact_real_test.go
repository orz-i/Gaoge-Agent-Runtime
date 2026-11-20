package harnesspostgres_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRealPostgresContextArtifactPreservesLegacyAndLosslessEncoding(t *testing.T) {
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
	for _, test := range []struct{ body, encoding string }{
		{`{"id":9007199254740992,"name":"🌱"}`, ""},
		{`{"id":9007199254740993,"name":"🌱"}`, runtimecontext.ArtifactEncodingLosslessJSON},
	} {
		artifact, err := runtimecontext.NewArtifact(runtimecontext.ArtifactCompaction, "test-scope", 2, "test-source", "", json.RawMessage(test.body))
		if err != nil {
			t.Fatal(err)
		}
		if _, fresh, err := writer.PutContextArtifact(t.Context(), artifact); err != nil || !fresh {
			t.Fatalf("put %q: fresh=%v err=%v", test.encoding, fresh, err)
		}
		loaded, err := reader.GetContextArtifact(t.Context(), artifact.ID)
		if err != nil || !reflect.DeepEqual(artifact, loaded) || loaded.Encoding != test.encoding || string(loaded.ContentJSON) != test.body {
			t.Fatalf("reload %q: artifact=%+v err=%v", test.encoding, loaded, err)
		}
		if _, fresh, err := reader.PutContextArtifact(t.Context(), artifact); err != nil || fresh {
			t.Fatalf("original write not replayable %q: fresh=%v err=%v", test.encoding, fresh, err)
		}
	}
}
