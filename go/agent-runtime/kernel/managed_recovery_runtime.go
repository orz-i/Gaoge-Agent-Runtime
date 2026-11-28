package kernel

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

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
	if e = host.VerifyManagedOriginalDispatch(ctx, b); e != nil {
		return ctx, e
	}
	return context.WithValue(ctx, managedDispatchKey{}, managedDispatchBinding{r, managedClone(b)}), nil
}

// ManagedDispatch is read-only and does not mint lifecycle evidence. Scoped
// Runtime copies share the same actual Store and must have the original Run.
func (r *Runtime) ManagedDispatch(ctx context.Context, id string) (ManagedHostBinding, bool) {
	b, ok := ctx.Value(managedDispatchKey{}).(managedDispatchBinding)
	if !ok || b.runtime == nil || r == nil || b.runtime.store != r.store || b.runtime.managedHost != r.managedHost || r.managedHost == nil || b.binding.RunID != id {
		return ManagedHostBinding{}, false
	}
	return managedClone(b.binding), true
}

// ApplyManagedNativeTransition commits a qualified native phase and its
// exact safe point through the same existing ApplyExecution transaction.
func (r *Runtime) ApplyManagedNativeTransition(ctx context.Context, id string, rev uint64, m Mutation, p ManagedSafePoint, proof *featurecontrol.Proof) (Snapshot, error) {
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

// AuthorizeManagedContinuation observes the original Host command. The native
// admission transaction alone checks and consumes execution eligibility.
func (r *Runtime) AuthorizeManagedContinuation(ctx context.Context, t ManagedTransfer, cancelReason ...string) (ManagedAdmissionProof, error) {
	return r.authorizeManaged(ctx, t, ManagedFrontierQualification{}, cancelReason...)
}

func (r *Runtime) AuthorizeQualifiedManagedContinuation(ctx context.Context, prior ManagedTransfer, point ManagedSafePoint, proof *featurecontrol.Proof, cancelReason ...string) (ManagedAdmissionProof, error) {
	if proof == nil {
		return ManagedAdmissionProof{}, ErrExecutionRequired
	}
	return r.authorizeManaged(ctx, prior, ManagedFrontierQualification{prior: managedClone(prior), point: managedClone(point), proof: proof}, cancelReason...)
}

func (r *Runtime) authorizeManaged(ctx context.Context, t ManagedTransfer, qualification ManagedFrontierQualification, cancelReason ...string) (ManagedAdmissionProof, error) {
	if r == nil || ctx == nil || r.managedHost == nil || !t.Valid() || t.Terminal != nil || len(cancelReason) > 1 {
		return ManagedAdmissionProof{}, ErrExecutionConflict
	}
	q := ManagedRecoverySourceRequest{RunID: t.SafePoint.Host.RunID, SuspensionID: t.SafePoint.ID, ExpectedRevision: t.SafePoint.Source.Run.Revision, ExpectedTransferDigest: ManagedDigest(t), Qualification: qualification}
	current, err := r.LoadManagedRecovery(ctx, q.RunID)
	if err != nil {
		return ManagedAdmissionProof{}, err
	}
	if q.ExpectedTransferDigest == "" || ManagedDigest(current) != q.ExpectedTransferDigest {
		return ManagedAdmissionProof{}, ErrExecutionConflict
	}
	authority, err := r.ManagedRecoveryAuthority(ctx)
	if err != nil {
		return ManagedAdmissionProof{}, err
	}
	if authority != t.SafePoint.Host.KernelAuthority {
		return ManagedAdmissionProof{}, ErrExecutionConflict
	}
	if qualification.proof != nil {
		if !qualification.matches(t, qualification.point.Source) {
			return ManagedAdmissionProof{}, ErrExecutionConflict
		}
		t.SafePoint = managedClone(qualification.point)
		t.Revision = t.SafePoint.Source.Run.Revision
		q.ExpectedRevision = t.Revision
	}
	host := r.managedHost.store
	a, err := host.ManagedRecoveryAuthority(ctx)
	if err != nil {
		return ManagedAdmissionProof{}, err
	}
	if a != t.SafePoint.Host.HostAuthority {
		return ManagedAdmissionProof{}, ErrExecutionConflict
	}
	v := t.Selection
	if v.Mode == "" {
		if err = host.VerifyManagedOriginalDispatch(ctx, t.SafePoint.Host); err != nil {
			return ManagedAdmissionProof{}, err
		}
		v = ManagedRecoveryAuthorization{Mode: ManagedResume}
		if len(cancelReason) == 1 {
			if !utf8.ValidString(cancelReason[0]) {
				return ManagedAdmissionProof{}, ErrInvalidInput
			}
			v.Mode = ManagedCancel
			v.CancellationReason = strings.TrimSpace(cancelReason[0])
		}
	}
	selected := ManagedAdmissionProof{reservation: v, transfer: managedClone(t), request: q, valid: true}
	if !selected.Matches(t, v.Mode) {
		return ManagedAdmissionProof{}, ErrExecutionConflict
	}
	return selected, nil
}

func (r *Runtime) AdmitManagedContinuation(ctx context.Context, p ManagedAdmissionProof, owner string, d time.Duration) (*Runtime, PhysicalAdmissionResult, error) {
	var zero PhysicalAdmissionResult
	if r == nil || ctx == nil || r.execution != nil || r.physicalGrant != nil {
		return nil, zero, ErrExecutionConflict
	}
	lid, e := (randomIDSource{}).NewID("managedlease")
	if e != nil {
		return nil, zero, e
	}
	gid, e := (randomIDSource{}).NewID("managedgrant")
	if e != nil {
		return nil, zero, e
	}
	source := p.transfer.SafePoint.Source
	admission := p.OwnedAdmission(ExecutionAcquireRequest{RunID: source.Run.ID, OwnerID: owner, LeaseDuration: d}, lid, gid)
	return r.admitPhysical(ctx, source, admission)
}
func (r *Runtime) CommitManagedCancellation(ctx context.Context, p ManagedAdmissionProof) (ManagedTransfer, error) {
	s, ok := r.store.(ManagedRecoveryStore)
	if !ok {
		return ManagedTransfer{}, ErrExecutionRequired
	}
	t := p.transfer
	return s.CommitManagedCancellation(ctx, ManagedCancelRequest{RunID: t.SafePoint.Host.RunID, SuspensionID: t.SafePoint.ID, Selection: p})
}

// ManagedRecoveryAbsent is only a compatibility helper for non-opted-in Runs.
func ManagedRecoveryAbsent(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrExecutionRequired)
}

// VerifyManagedApproval reads the exact original native journal decision;
// neither recovery admission nor result observation can use a Host receipt.
func (r *Runtime) VerifyManagedApproval(ctx context.Context, b ManagedHostBinding) error {
	if b.Approval == nil {
		return nil
	}
	applied, err := r.CommittedApprovalDispatch(ctx, b.RunID, b.DispatchID, *b.Approval)
	if err != nil || !applied {
		return errors.Join(ErrExecutionConflict, err)
	}
	return nil
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
	if !t.Resolved() || !t.SafePoint.Valid() ||
		(t.Selection.Mode == ManagedCancel && t.Terminal.Run.Status != RunStatusCancelled) {
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
	if e = r.VerifyManagedApproval(ctx, t.SafePoint.Host); e != nil {
		return ManagedClosureProof{}, e
	}
	if t.Selection.Mode == ManagedResume {
		receipt, err := r.LoadOwnedRetirement(ctx, t.SuccessorGrant.ID)
		if err != nil {
			return ManagedClosureProof{}, err
		}
		if !receipt.MatchesManagedClosure(t, s) {
			return ManagedClosureProof{}, ErrExecutionConflict
		}
	}
	p, _ := t.SafePoint.AgentObligations()
	if p.LedgerID != "" {
		ledger := r.BudgetStore()
		if ledger == nil {
			return ManagedClosureProof{}, ErrExecutionRequired
		}
		l, e := ledger.LoadBudget(ctx, p.LedgerID)
		if e != nil {
			return ManagedClosureProof{}, e
		}
		if e = ValidateManagedBudgetClosure(t.SafePoint, s, t.Selection.Mode, l); e != nil {
			return ManagedClosureProof{}, e
		}
	}
	t.Terminal = &s
	return ManagedClosureProof{transfer: managedClone(t)}, nil
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

// HasManagedRecoveryHost reports immutable construction, not a recovery grant.
func (r *Runtime) HasManagedRecoveryHost() bool {
	return r != nil && r.managedHost != nil
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
