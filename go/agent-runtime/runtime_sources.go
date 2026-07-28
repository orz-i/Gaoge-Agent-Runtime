package agentruntime

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// RuntimeClock is the time boundary used by orchestration code. Hosts may
// inject a deterministic implementation for recovery and deadline tests.
type RuntimeClock interface {
	Now() time.Time
}

// RuntimeIDSource is the entropy boundary used for non-deterministic public
// identifiers. Durable workflow identifiers remain content-derived.
type RuntimeIDSource interface {
	NewID() string
}

type systemRuntimeClock struct{}

func (systemRuntimeClock) Now() time.Time { return time.Now() }

type uuidRuntimeIDSource struct{}

func (uuidRuntimeIDSource) NewID() string { return uuid.NewString() }

func (s *Engine) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}

func (s *Engine) newRuntimeID(prefix string) string {
	source := RuntimeIDSource(uuidRuntimeIDSource{})
	if s != nil && s.idSource != nil {
		source = s.idSource
	}
	return strings.TrimSpace(prefix) + "_" + normalizePublicID(source.NewID())
}
