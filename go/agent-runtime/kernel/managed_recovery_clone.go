package kernel

import "slices"

// Keep this set limited to the actual persistence values exposed by managed
// recovery. Copying must not encode JSON or reinterpret authority evidence.
type managedCloneValue interface {
	ManagedHostBinding | ManagedSafePoint | ManagedHostReservation | ManagedTransfer | Snapshot
}

func managedClone[T managedCloneValue](value T) T {
	var copied any
	switch v := any(value).(type) {
	case ManagedHostBinding:
		copied = cloneManagedBinding(v)
	case ManagedSafePoint:
		copied = cloneManagedPoint(v)
	case ManagedHostReservation:
		copied = cloneManagedReservation(v)
	case ManagedTransfer:
		copied = cloneManagedTransfer(v)
	case Snapshot:
		copied = cloneManagedSnapshot(v)
	}
	return copied.(T)
}

func cloneManagedBinding(v ManagedHostBinding) ManagedHostBinding {
	v.Source = slices.Clone(v.Source)
	return v
}

func cloneManagedPoint(v ManagedSafePoint) ManagedSafePoint {
	v.Host = cloneManagedBinding(v.Host)
	v.Source = cloneManagedSnapshot(v.Source)
	v.LifecycleSource = slices.Clone(v.LifecycleSource)
	return v
}

func cloneManagedReservation(v ManagedHostReservation) ManagedHostReservation {
	v.Binding = cloneManagedBinding(v.Binding)
	v.Cancellation.Payload = slices.Clone(v.Cancellation.Payload)
	return v
}

func cloneManagedTransfer(v ManagedTransfer) ManagedTransfer {
	v.SafePoint = cloneManagedPoint(v.SafePoint)
	v.Selection = cloneManagedReservation(v.Selection)
	v.Activations = slices.Clone(v.Activations)
	for i := range v.Activations {
		v.Activations[i].Selection = cloneManagedReservation(v.Activations[i].Selection)
	}
	if v.Terminal != nil {
		terminal := cloneManagedSnapshot(*v.Terminal)
		v.Terminal = &terminal
	}
	return v
}

func cloneManagedSnapshot(v Snapshot) Snapshot {
	if v.Run.DeadlineAt != nil {
		at := *v.Run.DeadlineAt
		v.Run.DeadlineAt = &at
	}
	if v.Run.EndedAt != nil {
		at := *v.Run.EndedAt
		v.Run.EndedAt = &at
	}
	v.State = slices.Clone(v.State)
	if v.Checkpoint != nil {
		checkpoint := *v.Checkpoint
		checkpoint.Payload = slices.Clone(checkpoint.Payload)
		checkpoint.Response = slices.Clone(checkpoint.Response)
		if checkpoint.ResolvedAt != nil {
			at := *checkpoint.ResolvedAt
			checkpoint.ResolvedAt = &at
		}
		v.Checkpoint = &checkpoint
	}
	if v.Result != nil {
		result := *v.Result
		result.Content = slices.Clone(result.Content)
		v.Result = &result
	}
	return v
}
