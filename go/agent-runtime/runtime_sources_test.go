package agentruntime

import (
	"testing"
	"time"
)

type mutableRuntimeClock struct {
	now time.Time
}

func (c *mutableRuntimeClock) Now() time.Time { return c.now }

type fixedRuntimeIDSource struct {
	value string
}

func (s fixedRuntimeIDSource) NewID() string { return s.value }

func TestEngineUsesInjectedRuntimeSources(t *testing.T) {
	now := time.Date(2026, time.July, 28, 9, 30, 0, 0, time.UTC)
	engine := &Engine{
		clock:    &mutableRuntimeClock{now: now},
		idSource: fixedRuntimeIDSource{value: "ABC-123"},
	}
	if got := engine.now(); !got.Equal(now) {
		t.Fatalf("engine.now() = %v, want %v", got, now)
	}
	if got := engine.newRuntimeID("run"); got != "run_ABC123" {
		t.Fatalf("engine.newRuntimeID() = %q", got)
	}
}
