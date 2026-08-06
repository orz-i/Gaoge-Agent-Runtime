package kernel

import (
	"context"
	"encoding/json"
	"errors"
)

var (
	ErrInvalidInput  = errors.New("invalid kernel input")
	ErrNotFound      = errors.New("kernel run not found")
	ErrAlreadyExists = errors.New("kernel run already exists")
	ErrConflict      = errors.New("kernel run revision conflict")
	ErrTerminal      = errors.New("kernel run is terminal")
	ErrDeadline      = errors.New("kernel run deadline exceeded")
)

// Record is the feature-neutral data persisted atomically with a Run.
type Record struct {
	Run        Run
	State      json.RawMessage
	Checkpoint *Checkpoint
	Result     *Result
}

// StoreMutation is the fully validated replacement record for one CAS transition.
type StoreMutation struct {
	Record Record
	Events []EventDraft
}

// Store persists the minimal Run root and opaque feature state.
type Store interface {
	Create(context.Context, Record, []EventDraft) (Snapshot, error)
	Load(context.Context, string) (Snapshot, error)
	Apply(context.Context, string, uint64, StoreMutation) (Snapshot, error)
}
