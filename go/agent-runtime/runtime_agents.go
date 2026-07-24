package agentruntime

import "errors"

var (
	ErrAgentManifestConflict   = errors.New("agent manifest revision conflict")
	ErrAgentManifestDisabled   = errors.New("agent manifest is disabled")
	ErrRunHandoffConflict      = errors.New("run handoff idempotency conflict")
	ErrRunHandoffLimit         = errors.New("run handoff child limit reached")
	ErrRunHandoffDepth         = errors.New("run handoff depth limit reached")
	ErrRunHandoffParentBlocked = errors.New("parent run cannot delegate")
)
