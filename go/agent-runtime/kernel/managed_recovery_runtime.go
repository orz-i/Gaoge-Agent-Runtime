package kernel

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

func (r *Runtime) ManagedRecoveryAuthority(ctx context.Context) (ManagedAuthority, error) {
	if r == nil {
		return ManagedAuthority{}, ErrInvalidInput
	}
	s, ok := r.store.(ManagedRecoveryStore)
	if !ok {
		return ManagedAuthority{}, ErrExecutionRequired
	}
	return s.ManagedRecoveryAuthority(ctx)
}
func (r *Runtime) LoadManagedRecovery(ctx context.Context, id string) (ManagedTransfer, error) {
	if r == nil {
		return ManagedTransfer{}, ErrInvalidInput
	}
	s, ok := r.store.(ManagedRecoveryStore)
	if !ok {
		return ManagedTransfer{}, ErrExecutionRequired
	}
	return s.LoadManagedRecovery(ctx, id)
}

// BindManagedDispatch performs original Host reads. The resulting context is
// tied to this Runtime and actual Host storage authority; IDs alone are inert.
type managedDispatchKey struct{}
type managedDispatchBinding struct {
	runtime *Runtime
	host    ManagedRecoveryHostStore
	binding ManagedHostBinding
}

func (r *Runtime) BindManagedDispatch(ctx context.Context, b ManagedHostBinding) (context.Context, error) {
	if r == nil || r.managedHost == nil || !b.Valid() {
		return ctx, ErrInvalidInput
	}
	host := r.managedHost.store
	ka, e := r.ManagedRecoveryAuthority(ctx)
	if e != nil {
		return ctx, e
	}
	ha, e := host.ManagedRecoveryAuthority(ctx)
	if e != nil {
		return ctx, e
	}
	if ka != b.KernelAuthority || ha != b.HostAuthority {
		return ctx, ErrExecutionConflict
	}
	original, e := host.ReadManagedOriginalDispatch(ctx, b)
	if e != nil {
		return ctx, e
	}
	if !reflect.DeepEqual(original, b) {
		return ctx, ErrExecutionConflict
	}
	return context.WithValue(ctx, managedDispatchKey{}, managedDispatchBinding{r, host, managedClone(b)}), nil
}

// ManagedDispatch is read-only and does not mint lifecycle evidence. Scoped
// Runtime copies share the same actual Store and must have the original Run.
func (r *Runtime) ManagedDispatch(ctx context.Context, id string) (ManagedHostBinding, bool) {
	b, ok := ctx.Value(managedDispatchKey{}).(managedDispatchBinding)
	if !ok || b.runtime == nil || r == nil || b.runtime.store != r.store || b.runtime.managedHost != r.managedHost || r.managedHost == nil || b.host != r.managedHost.store || b.binding.RunID != id {
		return ManagedHostBinding{}, false
	}
	return managedClone(b.binding), true
}

// ApplyManagedModelIntent is callable only by a first-party implementation
// possessing an internal source proof. T0 still uses normal ApplyExecution.
func (r *Runtime) ApplyManagedModelIntent(ctx context.Context, id string, rev uint64, m Mutation, p ManagedSafePoint, proof *featurecontrol.Proof) (Snapshot, error) {
	if r == nil || r.execution == nil || r.physicalGrant == nil {
		return Snapshot{}, ErrExecutionRequired
	}
	current, e := r.Load(ctx, id)
	if e != nil {
		return Snapshot{}, e
	}
	if current.Run.Revision != rev || !proof.Matches(id, rev, "managed_intent_source", r.physicalGrant.ID, current.State, p) {
		return Snapshot{}, ErrExecutionConflict
	}
	scoped := *r
	scoped.managedIntent = &ManagedSafePointProof{point: managedClone(p), proof: proof}
	return scoped.Apply(ctx, id, rev, m)
}
func (r *Runtime) LeaveManagedSafePoint(ctx context.Context, id string) (ManagedTransfer, error) {
	if r == nil || r.execution == nil {
		return ManagedTransfer{}, ErrExecutionRequired
	}
	s, ok := r.store.(ManagedRecoveryStore)
	if !ok {
		return ManagedTransfer{}, ErrExecutionRequired
	}
	t, e := s.LoadManagedRecovery(ctx, id)
	if e != nil {
		return ManagedTransfer{}, e
	}
	if t.State == ManagedInvalidated {
		return t, nil
	}
	return s.LeaveManagedSafePoint(ctx, ManagedLeaveRequest{RunID: id, SuspensionID: t.SafePoint.ID, ExpectedRevision: t.SafePoint.Source.Run.Revision, ExpectedTransferRevision: t.Revision, Generation: t.Generation, Lease: *r.execution})
}

// ParkManagedContinuation reads no custom clock and writes no Host records.
func (r *Runtime) ParkManagedContinuation(ctx context.Context, id string) (ManagedParkedProof, error) {
	s, ok := r.store.(ManagedRecoveryStore)
	if !ok {
		return ManagedParkedProof{}, ErrExecutionRequired
	}
	t, e := s.LoadManagedRecovery(ctx, id)
	if e != nil {
		return ManagedParkedProof{}, e
	}
	if t.State != ManagedParked {
		t, e = s.ParkManagedRecovery(ctx, ManagedParkRequest{RunID: id, SuspensionID: t.SafePoint.ID, ExpectedRevision: t.SafePoint.Source.Run.Revision})
		if e != nil {
			return ManagedParkedProof{}, e
		}
	}
	a, e := s.ManagedRecoveryAuthority(ctx)
	if e != nil {
		return ManagedParkedProof{}, e
	}
	if a != t.SafePoint.Host.KernelAuthority {
		return ManagedParkedProof{}, ErrExecutionConflict
	}
	return ManagedParkedProof{transfer: managedClone(t), authority: a, valid: true}, nil
}

// SelectManagedContinuation only accepts the fixed committed Host selection.
func (r *Runtime) SelectManagedContinuation(ctx context.Context, p ManagedParkedProof) (ManagedSelectionProof, error) {
	if r == nil || r.managedHost == nil || !p.Matches(p.transfer.SafePoint.Host) {
		return ManagedSelectionProof{}, ErrExecutionConflict
	}
	host := r.managedHost.store
	current, e := r.LoadManagedRecovery(ctx, p.transfer.SafePoint.Host.RunID)
	if e != nil {
		return ManagedSelectionProof{}, e
	}
	if !reflect.DeepEqual(current, p.transfer) {
		return ManagedSelectionProof{}, ErrExecutionConflict
	}
	a, e := host.ManagedRecoveryAuthority(ctx)
	if e != nil {
		return ManagedSelectionProof{}, e
	}
	if a != p.transfer.SafePoint.Host.HostAuthority {
		return ManagedSelectionProof{}, ErrExecutionConflict
	}
	_, e = host.ReserveManagedRecovery(ctx, p)
	if e != nil {
		return ManagedSelectionProof{}, e
	}
	v, e := host.ReadManagedRecovery(ctx, p.transfer.SafePoint.Host, p.transfer.SafePoint.RecoveryID)
	if e != nil {
		return ManagedSelectionProof{}, e
	}
	proof := ManagedSelectionProof{reservation: managedClone(v), transfer: managedClone(current), valid: true}
	if !proof.Matches(current, v.Mode) {
		return ManagedSelectionProof{}, ErrExecutionConflict
	}
	return proof, nil
}
func (r *Runtime) EnableManagedContinuation(ctx context.Context, p ManagedSelectionProof, claimant, owner string, d time.Duration) (ManagedTransfer, error) {
	s, ok := r.store.(ManagedRecoveryStore)
	if !ok {
		return ManagedTransfer{}, ErrExecutionRequired
	}
	// Fixed internal entropy only: even after PARK, no configured callback may
	// run before Leave. These tokens identify THIS actual claimant.
	lid, e := (randomIDSource{}).NewID("managedlease")
	if e != nil {
		return ManagedTransfer{}, e
	}
	gid, e := (randomIDSource{}).NewID("managedgrant")
	if e != nil {
		return ManagedTransfer{}, e
	}
	t := p.transfer
	return s.EnableManagedRecovery(ctx, ManagedEnableRequest{RunID: t.SafePoint.Host.RunID, SuspensionID: t.SafePoint.ID, ExpectedTransferRevision: t.Revision, Generation: t.Generation, ClaimantID: claimant, OwnerID: owner, LeaseID: lid, GrantID: gid, LeaseDuration: d, Selection: p})
}
func (r *Runtime) CancelParkedManagedContinuation(ctx context.Context, p ManagedSelectionProof) (ManagedTransfer, error) {
	s, ok := r.store.(ManagedRecoveryStore)
	if !ok {
		return ManagedTransfer{}, ErrExecutionRequired
	}
	t := p.transfer
	return s.CancelParkedManagedContinuation(ctx, ManagedCancelRequest{RunID: t.SafePoint.Host.RunID, SuspensionID: t.SafePoint.ID, ExpectedTransferRevision: t.Revision, Generation: t.Generation, Selection: p})
}

// ManagedRecoveryAbsent is only a compatibility helper for non-opted-in Runs.
func ManagedRecoveryAbsent(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrExecutionRequired)
}

func (r *Runtime) CompleteManagedRecovery(ctx context.Context, g ChildExecutionGrant, s Snapshot, proof *featurecontrol.Proof) error {
	store, ok := r.store.(ManagedRecoveryStore)
	if !ok {
		return nil
	}
	p := ManagedCompletionProof{proof: proof}
	if !p.Matches(g, s) {
		return ErrExecutionConflict
	}
	return store.CompleteManagedRecovery(ctx, g, s, p)
}
func (r *Runtime) ManagedRecoveryClosure(ctx context.Context, id string) (ManagedClosureProof, error) {
	store, ok := r.store.(ManagedRecoveryStore)
	if !ok {
		return ManagedClosureProof{}, ErrExecutionRequired
	}
	t, e := store.LoadManagedRecoveryByID(ctx, id)
	if e != nil {
		return ManagedClosureProof{}, e
	}
	if (t.State != ManagedClosed && t.State != ManagedCancelled) || t.Terminal == nil || !t.Selection.Valid() {
		return ManagedClosureProof{}, ErrExecutionConflict
	}
	a, e := store.ManagedRecoveryAuthority(ctx)
	if e != nil {
		return ManagedClosureProof{}, e
	}
	if a != t.SafePoint.Host.KernelAuthority {
		return ManagedClosureProof{}, ErrExecutionConflict
	}
	s, e := r.Load(ctx, t.SafePoint.Host.RunID)
	if e != nil {
		return ManagedClosureProof{}, e
	}
	if ManagedDigest(s) != ManagedDigest(*t.Terminal) {
		return ManagedClosureProof{}, ErrExecutionConflict
	}
	return ManagedClosureProof{transfer: managedClone(t), snapshot: s, valid: true}, nil
}

func (r *Runtime) LoadManagedRecoveryByID(ctx context.Context, id string) (ManagedTransfer, error) {
	s, ok := r.store.(ManagedRecoveryStore)
	if !ok {
		return ManagedTransfer{}, ErrExecutionRequired
	}
	return s.LoadManagedRecoveryByID(ctx, id)
}

func (r *Runtime) SupportsManagedRecovery() bool {
	if r == nil {
		return false
	}
	_, ok := r.store.(ManagedRecoveryStore)
	return ok
}

// Registration is immutable trusted configuration, like the Kernel Store.
// Reconstructing a Runtime with a malicious Store changes that trust boundary;
// an existing Runtime cannot replace it during a recovery call.
type managedHostRegistration struct{ store ManagedRecoveryHostStore }

func newManagedHostRegistration(host ManagedRecoveryHostStore) (*managedHostRegistration, error) {
	if host == nil {
		return nil, nil
	}
	value := reflect.ValueOf(host)
	if value.Kind() != reflect.Pointer || value.IsNil() || !value.Type().Comparable() {
		return nil, ErrInvalidInput
	}
	return &managedHostRegistration{store: host}, nil
}
func (r *Runtime) UsesManagedRecoveryHost(host ManagedRecoveryHostStore) bool {
	if r == nil || r.managedHost == nil || host == nil {
		return false
	}
	value := reflect.ValueOf(host)
	return value.Kind() == reflect.Pointer && !value.IsNil() && value.Type().Comparable() && r.managedHost.store == host
}
func (r *Runtime) ReadManagedReservation(ctx context.Context, b ManagedHostBinding, id string) (ManagedHostReservation, error) {
	if r == nil || r.managedHost == nil {
		return ManagedHostReservation{}, ErrExecutionRequired
	}
	a, e := r.managedHost.store.ManagedRecoveryAuthority(ctx)
	if e != nil {
		return ManagedHostReservation{}, e
	}
	if a != b.HostAuthority {
		return ManagedHostReservation{}, ErrExecutionConflict
	}
	return r.managedHost.store.ReadManagedRecovery(ctx, b, id)
}
