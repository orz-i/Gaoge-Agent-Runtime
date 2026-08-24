package main

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"sync"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	a2a "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-a2a"
)

const defaultPageSize = 50

type memoryTaskStore struct {
	mu      sync.RWMutex
	records map[string]a2a.HostedTaskRecord
}

func newMemoryTaskStore() *memoryTaskStore {
	return &memoryTaskStore{records: make(map[string]a2a.HostedTaskRecord)}
}

func (store *memoryTaskStore) Create(_ context.Context, record a2a.HostedTaskRecord) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := taskKey(record.OwnerTenant, record.OwnerSubject, record.TaskID)
	if _, exists := store.records[key]; exists {
		return 0, a2a.ErrHostedTaskAlreadyExists
	}
	record.Version = 1
	store.records[key] = cloneRecord(record)
	return record.Version, nil
}

func (store *memoryTaskStore) Update(_ context.Context, update a2a.HostedTaskUpdate) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := taskKey(update.OwnerTenant, update.OwnerSubject, update.TaskID)
	record, exists := store.records[key]
	if !exists {
		return 0, a2a.ErrHostedTaskNotFound
	}
	if record.Version != update.PreviousVersion {
		return 0, a2a.ErrHostedTaskConflict
	}
	record.Version++
	record.Task = append(json.RawMessage(nil), update.Task...)
	store.records[key] = record
	return record.Version, nil
}

func (store *memoryTaskStore) Get(
	_ context.Context,
	taskID string,
	principal a2a.HostedPrincipal,
) (a2a.HostedTaskRecord, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, exists := store.records[taskKey(principal.Tenant, principal.Subject, taskID)]
	if !exists {
		return a2a.HostedTaskRecord{}, a2a.ErrHostedTaskNotFound
	}
	return cloneRecord(record), nil
}

func (store *memoryTaskStore) List(_ context.Context, query a2a.HostedTaskQuery) (a2a.HostedTaskPage, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	records := store.filteredRecords(query)
	offset, err := pageOffset(query.PageToken, len(records))
	if err != nil {
		return a2a.HostedTaskPage{}, err
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	end := min(offset+pageSize, len(records))
	nextPageToken := ""
	if end < len(records) {
		nextPageToken = strconv.Itoa(end)
	}
	return a2a.HostedTaskPage{
		Tasks: records[offset:end], TotalSize: len(records), PageSize: pageSize, NextPageToken: nextPageToken,
	}, nil
}

func (store *memoryTaskStore) filteredRecords(query a2a.HostedTaskQuery) []a2a.HostedTaskRecord {
	records := make([]a2a.HostedTaskRecord, 0, len(store.records))
	for _, record := range store.records {
		if record.OwnerSubject != query.OwnerSubject || record.OwnerTenant != query.OwnerTenant {
			continue
		}
		projected, ok := projectRecord(record, query)
		if ok {
			records = append(records, projected)
		}
	}
	sort.Slice(records, func(left, right int) bool { return records[left].TaskID < records[right].TaskID })
	return records
}

func projectRecord(record a2a.HostedTaskRecord, query a2a.HostedTaskQuery) (a2a.HostedTaskRecord, bool) {
	var task a2asdk.Task
	if json.Unmarshal(record.Task, &task) != nil ||
		(query.ContextID != "" && task.ContextID != query.ContextID) ||
		(query.State != "" && string(task.Status.State) != query.State) {
		return a2a.HostedTaskRecord{}, false
	}
	if !query.IncludeArtifacts {
		task.Artifacts = nil
	}
	if query.HistoryLength != nil && *query.HistoryLength >= 0 && len(task.History) > *query.HistoryLength {
		task.History = task.History[len(task.History)-*query.HistoryLength:]
	}
	record.Task, _ = json.Marshal(task)
	return cloneRecord(record), true
}

func pageOffset(token string, total int) (int, error) {
	if token == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(token)
	if err != nil || offset < 0 || offset > total {
		return 0, a2a.ErrHostedTaskStore
	}
	return offset, nil
}

func taskKey(tenant, subject, taskID string) string {
	return tenant + "\x00" + subject + "\x00" + taskID
}

func cloneRecord(record a2a.HostedTaskRecord) a2a.HostedTaskRecord {
	record.Task = append(json.RawMessage(nil), record.Task...)
	return record
}

var _ a2a.HostedTaskStore = (*memoryTaskStore)(nil)
