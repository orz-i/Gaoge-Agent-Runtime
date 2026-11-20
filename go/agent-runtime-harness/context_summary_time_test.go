package harness

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func TestSummaryCursorUsesOneStorageTimestampRepresentation(t *testing.T) {
	instant := time.Date(2026, 11, 20, 0, 0, 0, 123456000, time.UTC)
	original := Turn{ID: "turn", CreatedAt: instant, UpdatedAt: instant.Add(time.Second)}
	stored := original
	stored.CreatedAt = stored.CreatedAt.In(time.FixedZone("database session", 8*60*60))
	stored.UpdatedAt = stored.UpdatedAt.In(time.FixedZone("database session", 8*60*60))
	before, err := json.Marshal(summarySamplingTurn(original))
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(summarySamplingTurn(stored))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("equivalent stored time changed exact summary cursor bytes: before=%s after=%s err=%v", before, after, err)
	}
	if _, offset := stored.UpdatedAt.Zone(); offset != 8*60*60 {
		t.Fatal("summary projection mutated the Host's original stored timestamp")
	}
}

func TestSummaryCursorWriteTimePreservesStoredFloorBeforeFreezing(t *testing.T) {
	instant := time.Date(2040, 1, 2, 3, 4, 5, 123456000, time.UTC)
	for _, status := range []TurnStatus{TurnAccepted, TurnRunning} {
		for _, test := range []struct {
			name     string
			stored   time.Time
			observed time.Time
			want     time.Time
		}{
			{
				name:   "Harness clock ahead of wall clock",
				stored: instant.Add(time.Hour + 789*time.Nanosecond), observed: instant,
				want: instant.Add(time.Hour + 789*time.Nanosecond),
			},
			{
				name:   "wall clock moved backwards",
				stored: instant, observed: instant.Add(-time.Minute), want: instant,
			},
			{
				name:   "Memory source has finer stored precision",
				stored: instant.Add(789 * time.Nanosecond), observed: instant.Add(900 * time.Nanosecond),
				want: instant.Add(789 * time.Nanosecond),
			},
			{
				name:   "PostgreSQL write precision and local offset",
				stored: instant.In(time.FixedZone("database session", 8*60*60)), observed: instant.Add(10987 * time.Nanosecond),
				want: instant.Add(10 * time.Microsecond),
			},
		} {
			t.Run(string(status)+"/"+test.name, func(t *testing.T) {
				original := Turn{Status: status, UpdatedAt: test.stored}
				frozen := summaryCursorWriteTime(original, test.observed)
				if !frozen.Equal(test.want) || frozen.Before(original.UpdatedAt) || frozen.Location() != time.UTC {
					t.Fatalf("frozen write time lost the exact stored floor: got=%s want=%s source=%s", frozen, test.want, original.UpdatedAt)
				}
				raw, err := json.Marshal(frozen)
				var restored time.Time
				if err != nil || json.Unmarshal(raw, &restored) != nil || !restored.Equal(test.want) {
					t.Fatalf("frozen time cannot roundtrip through the summary recipe: %s err=%v", raw, err)
				}
				if original.UpdatedAt != test.stored {
					t.Fatal("freezing a write time mutated the original source Turn")
				}
			})
		}
	}
}
