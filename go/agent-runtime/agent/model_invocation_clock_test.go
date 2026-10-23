package agent

import "time"

// modelRetryTestClock avoids wall-clock sleeps in internal RetryAfter tests.
// Each test owns an instance and advances it only between model attempts.
type modelRetryTestClock struct{ now time.Time }

func (clock *modelRetryTestClock) Now() time.Time { return clock.now }
func (clock *modelRetryTestClock) Advance(d time.Duration) {
	clock.now = clock.now.Add(d)
}
