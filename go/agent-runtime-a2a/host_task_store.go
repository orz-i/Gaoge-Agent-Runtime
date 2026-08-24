package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

const maxHostedTaskBytes = 32 * 1024 * 1024

var (
	ErrHostedTaskAlreadyExists = errors.New("A2A hosted task already exists")
	ErrHostedTaskConflict      = errors.New("A2A hosted task version conflict")
	ErrHostedTaskNotFound      = errors.New("A2A hosted task not found")
	ErrHostedTaskStore         = errors.New("A2A hosted task store failed")
)

// HostedTaskRecord is one owner-scoped opaque A2A Task revision.
type HostedTaskRecord struct {
	TaskID       string
	OwnerSubject string
	OwnerTenant  string
	Version      int64
	Task         json.RawMessage
}

// HostedTaskUpdate is one optimistic, owner-scoped task mutation.
type HostedTaskUpdate struct {
	TaskID          string
	OwnerSubject    string
	OwnerTenant     string
	Task            json.RawMessage
	Event           json.RawMessage
	PreviousTask    json.RawMessage
	PreviousVersion int64
}

// HostedTaskQuery is the product-owned persistence filter for tasks/list.
type HostedTaskQuery struct {
	OwnerSubject         string
	OwnerTenant          string
	ContextID            string
	State                string
	PageSize             int
	PageToken            string
	HistoryLength        *int
	StatusTimestampAfter *time.Time
	IncludeArtifacts     bool
}

// HostedTaskPage is one owner-filtered persistence page.
type HostedTaskPage struct {
	Tasks         []HostedTaskRecord
	TotalSize     int
	PageSize      int
	NextPageToken string
}

// HostedTaskStore persists official wire data without forcing the host to
// import official A2A types. Implementations must enforce owner and OCC fields.
type HostedTaskStore interface {
	Create(context.Context, HostedTaskRecord) (int64, error)
	Update(context.Context, HostedTaskUpdate) (int64, error)
	Get(context.Context, string, HostedPrincipal) (HostedTaskRecord, error)
	List(context.Context, HostedTaskQuery) (HostedTaskPage, error)
}

type protocolTaskStore struct{ store HostedTaskStore }

func newProtocolTaskStore(store HostedTaskStore) taskstore.Store {
	return &protocolTaskStore{store: store}
}

func (store *protocolTaskStore) Create(ctx context.Context, task *a2asdk.Task) (taskstore.TaskVersion, error) {
	principal, err := requiredHostedPrincipal(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, a2asdk.ErrUnauthenticated
	}
	if store == nil || store.store == nil || task == nil || strings.TrimSpace(string(task.ID)) == "" {
		return taskstore.TaskVersionMissing, ErrHostedTaskStore
	}
	raw, err := marshalHostedTaskValue(task)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	version, err := store.store.Create(ctx, HostedTaskRecord{
		TaskID: strings.TrimSpace(string(task.ID)), OwnerSubject: principal.Subject,
		OwnerTenant: principal.Tenant, Task: raw,
	})
	if err != nil {
		return taskstore.TaskVersionMissing, protocolTaskStoreError(err)
	}
	if version <= 0 {
		return taskstore.TaskVersionMissing, ErrHostedTaskStore
	}
	return taskstore.TaskVersion(version), nil
}

func (store *protocolTaskStore) Update(
	ctx context.Context,
	update *taskstore.UpdateRequest,
) (taskstore.TaskVersion, error) {
	principal, err := requiredHostedPrincipal(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, a2asdk.ErrUnauthenticated
	}
	if store == nil || store.store == nil || update == nil || update.Task == nil ||
		strings.TrimSpace(string(update.Task.ID)) == "" || update.PrevVersion <= 0 {
		return taskstore.TaskVersionMissing, ErrHostedTaskStore
	}
	taskRaw, err := marshalHostedTaskValue(update.Task)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	eventRaw, err := marshalHostedTaskValue(update.Event)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	previousRaw, err := marshalHostedTaskValue(update.PrevTask)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	version, err := store.store.Update(ctx, HostedTaskUpdate{
		TaskID: strings.TrimSpace(string(update.Task.ID)), OwnerSubject: principal.Subject,
		OwnerTenant: principal.Tenant, Task: taskRaw, Event: eventRaw, PreviousTask: previousRaw,
		PreviousVersion: int64(update.PrevVersion),
	})
	if err != nil {
		return taskstore.TaskVersionMissing, protocolTaskStoreError(err)
	}
	if version <= int64(update.PrevVersion) {
		return taskstore.TaskVersionMissing, ErrHostedTaskStore
	}
	return taskstore.TaskVersion(version), nil
}

func (store *protocolTaskStore) Get(
	ctx context.Context,
	taskID a2asdk.TaskID,
) (*taskstore.StoredTask, error) {
	principal, err := requiredHostedPrincipal(ctx)
	id := strings.TrimSpace(string(taskID))
	if err != nil {
		return nil, a2asdk.ErrUnauthenticated
	}
	if store == nil || store.store == nil || id == "" {
		return nil, ErrHostedTaskStore
	}
	record, err := store.store.Get(ctx, id, principal)
	if err != nil {
		return nil, protocolTaskStoreError(err)
	}
	return decodeHostedTaskRecord(record, id, principal)
}

func (store *protocolTaskStore) List(
	ctx context.Context,
	request *a2asdk.ListTasksRequest,
) (*a2asdk.ListTasksResponse, error) {
	principal, err := requiredHostedPrincipal(ctx)
	if err != nil {
		return nil, a2asdk.ErrUnauthenticated
	}
	if store == nil || store.store == nil || request == nil {
		return nil, ErrHostedTaskStore
	}
	page, err := store.store.List(ctx, HostedTaskQuery{
		OwnerSubject: principal.Subject, OwnerTenant: principal.Tenant,
		ContextID: strings.TrimSpace(request.ContextID), State: string(request.Status),
		PageSize: request.PageSize, PageToken: strings.TrimSpace(request.PageToken),
		HistoryLength: cloneInt(request.HistoryLength), StatusTimestampAfter: cloneTime(request.StatusTimestampAfter),
		IncludeArtifacts: request.IncludeArtifacts,
	})
	if err != nil {
		return nil, protocolTaskStoreError(err)
	}
	if len(page.Tasks) > 100 || page.TotalSize < 0 || page.PageSize < 0 {
		return nil, ErrHostedTaskStore
	}
	response := &a2asdk.ListTasksResponse{
		Tasks: make([]*a2asdk.Task, 0, len(page.Tasks)), TotalSize: page.TotalSize,
		PageSize: page.PageSize, NextPageToken: strings.TrimSpace(page.NextPageToken),
	}
	for _, record := range page.Tasks {
		stored, decodeErr := decodeHostedTaskRecord(record, strings.TrimSpace(record.TaskID), principal)
		if decodeErr != nil {
			return nil, decodeErr
		}
		response.Tasks = append(response.Tasks, stored.Task)
	}
	return response, nil
}

func decodeHostedTaskRecord(
	record HostedTaskRecord,
	expectedID string,
	principal HostedPrincipal,
) (*taskstore.StoredTask, error) {
	if record.Version <= 0 || strings.TrimSpace(record.TaskID) != expectedID ||
		record.OwnerSubject != principal.Subject || record.OwnerTenant != principal.Tenant ||
		len(record.Task) == 0 || len(record.Task) > maxHostedTaskBytes || !json.Valid(record.Task) {
		return nil, a2asdk.ErrTaskNotFound
	}
	var task a2asdk.Task
	if err := json.Unmarshal(record.Task, &task); err != nil || strings.TrimSpace(string(task.ID)) != expectedID {
		return nil, ErrHostedTaskStore
	}
	return &taskstore.StoredTask{
		Task: &task, Version: taskstore.TaskVersion(record.Version), User: hostedOwnerKey(principal),
	}, nil
}

func requiredHostedPrincipal(ctx context.Context) (HostedPrincipal, error) {
	principal := hostedPrincipalFromContext(ctx)
	if !validPrincipalValue(principal.Subject) || !validOptionalPrincipalValue(principal.Tenant) {
		return HostedPrincipal{}, ErrHostedUnauthenticated
	}
	return principal, nil
}

func marshalHostedTaskValue(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 || len(raw) > maxHostedTaskBytes || !json.Valid(raw) {
		return nil, ErrHostedTaskStore
	}
	return append(json.RawMessage(nil), raw...), nil
}

func protocolTaskStoreError(err error) error {
	switch {
	case errors.Is(err, ErrHostedTaskAlreadyExists):
		return taskstore.ErrTaskAlreadyExists
	case errors.Is(err, ErrHostedTaskConflict):
		return taskstore.ErrConcurrentModification
	case errors.Is(err, ErrHostedTaskNotFound):
		return a2asdk.ErrTaskNotFound
	default:
		return ErrHostedTaskStore
	}
}

var _ taskstore.Store = (*protocolTaskStore)(nil)
