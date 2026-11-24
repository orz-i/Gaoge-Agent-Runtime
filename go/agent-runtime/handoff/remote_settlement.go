package handoff

import (
	"context"
	"time"
)

// remoteCallbackSettlementTimeout bounds the original physical callback's
// signed receipt commit independently of the caller's expired request.
// This is local accounting, never an extension of external execution rights.
const remoteCallbackSettlementTimeout = 5 * time.Second

// RemoteCallbackSettlementContext is used exclusively AFTER an already-admitted
// remote RPC callback has returned, to persist its original identity, returned
// flag and terminal observation. Do not use it for an initial admission,
// provider I/O, physical Cancel/Resume/Prompt or any other new side effect.
//
// A caller disconnect or deadline cannot erase the original callback's proof.
// Store failures and CAS conflicts still fail closed, and this bounded context
// must not be used to retry an ambiguous external operation.
func RemoteCallbackSettlementContext(caller context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(caller), remoteCallbackSettlementTimeout)
}
