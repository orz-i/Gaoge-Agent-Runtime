package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyEffectProfilesRefusedWithoutWrites(t *testing.T) {
	for _, version := range []int{3, 5, 6, 7, 8, 9, 10, 11, 12, 13} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := ownedPrivatePath(t, "legacy.sqlite")
			raw, e := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
			if e != nil {
				t.Fatal(e)
			}
			if _, e = raw.Exec(`CREATE TABLE runtime_child_effect_grants(grant_id TEXT, parent_run_id TEXT, child_run_id TEXT, settled_revision INTEGER, payload_json TEXT);
    INSERT INTO runtime_child_effect_grants VALUES('original','parent','child',1,'{"unknown":"retained"}');
    CREATE TABLE runtime_owned_retirements(grant_id TEXT,phase TEXT,payload_json TEXT);
    INSERT INTO runtime_owned_retirements VALUES('original','RETIRED','{"receipt":"original"}');
    CREATE TABLE runtime_grant_identities(grant_id TEXT,run_id TEXT);
    INSERT INTO runtime_grant_identities VALUES('original','child');` + fmt.Sprintf("PRAGMA user_version=%d", version)); e != nil {
				t.Fatal(e)
			}
			if e = raw.Close(); e != nil {
				t.Fatal(e)
			}
			if e = os.Chmod(path, 0600); e != nil {
				t.Fatal(e)
			}
			before, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			if opened, e := OpenExisting(t.Context(), path); !errors.Is(e, ErrUnsupportedSchema) {
				if opened != nil {
					opened.Close()
				}
				t.Fatalf("old profile admitted: %v", e)
			}
			after, e := os.ReadFile(path)
			if e != nil || string(before) != string(after) {
				t.Fatalf("old bytes changed: %v", e)
			}
		})
	}
}
