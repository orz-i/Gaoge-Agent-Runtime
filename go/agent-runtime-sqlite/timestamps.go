package sqlite

import (
	"fmt"
	"math"
	"time"
)

var (
	minPersistableTime = time.Unix(0, math.MinInt64).UTC()
	maxPersistableTime = time.Unix(0, math.MaxInt64).UTC()
)

// storedNanoseconds rejects time.Time values that silently overflow UnixNano
// (pre-1678, post-2262, or zero). A far-future retry must not become an
// immediately eligible negative SQLite timestamp.
func storedNanoseconds(at time.Time) (int64, error) {
	if at.IsZero() || at.Before(minPersistableTime) || at.After(maxPersistableTime) {
		return 0, fmt.Errorf("%w: %s", ErrInvalidTime, at.UTC().Format(time.RFC3339Nano))
	}
	return at.UTC().UnixNano(), nil
}
