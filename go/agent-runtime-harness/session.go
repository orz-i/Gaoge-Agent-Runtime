package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

var (
	ErrInvalidRequest = errors.New("invalid harness request")
	ErrNotFound       = errors.New("harness entity not found")
	ErrConflict       = errors.New("harness state conflict")
)

const (
	maxHostRefKindBytes = 64
	maxHostRefIDBytes   = 128
)

// HostRef is a stable product-owned identity. Harness never stores the product body behind this reference.
type HostRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Session binds one product Thread to a durable Harness execution session.
type Session struct {
	ID         string          `json:"id"`
	HostThread HostRef         `json:"hostThread"`
	Actor      kernel.ActorRef `json:"actor"`
	Revision   uint64          `json:"revision"`
	CreatedAt  time.Time       `json:"createdAt"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

func normalizeHostRef(value HostRef) (HostRef, error) {
	value.Kind = strings.TrimSpace(value.Kind)
	value.ID = strings.TrimSpace(value.ID)
	if value.Kind == "" || value.ID == "" || len(value.Kind) > maxHostRefKindBytes || len(value.ID) > maxHostRefIDBytes {
		return HostRef{}, ErrInvalidRequest
	}
	return value, nil
}

func validActor(actor kernel.ActorRef) bool {
	return strings.TrimSpace(actor.TenantID) != "" && strings.TrimSpace(actor.ActorID) != ""
}

func stableID(prefix string, values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(strings.TrimSpace(value)))
		_, _ = hash.Write([]byte{0})
	}
	return prefix + "_" + hex.EncodeToString(hash.Sum(nil))[:24]
}

// SessionID deterministically derives the Harness Session identity from a product Thread reference.
func SessionID(hostThread HostRef) (string, error) {
	normalized, err := normalizeHostRef(hostThread)
	if err != nil {
		return "", err
	}
	return stableID("hs", normalized.Kind, normalized.ID), nil
}

func cloneSession(value Session) Session { return value }
