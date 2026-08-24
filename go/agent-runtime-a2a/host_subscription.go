package a2a

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
)

const (
	hostedSubscriptionBuffer    = 64
	hostedSubscriptionKeepAlive = 15 * time.Second
)

type hostedSubscriptionUpdate struct {
	version  int64
	payload  []byte
	terminal bool
}

type hostedSubscriptionHub struct {
	mu          sync.Mutex
	nextID      uint64
	subscribers map[string]map[uint64]chan hostedSubscriptionUpdate
}

func newHostedSubscriptionHub() *hostedSubscriptionHub {
	return &hostedSubscriptionHub{subscribers: make(map[string]map[uint64]chan hostedSubscriptionUpdate)}
}

func (hub *hostedSubscriptionHub) subscribe(
	key string,
) (<-chan hostedSubscriptionUpdate, func()) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.nextID++
	id := hub.nextID
	updates := make(chan hostedSubscriptionUpdate, hostedSubscriptionBuffer)
	if hub.subscribers[key] == nil {
		hub.subscribers[key] = make(map[uint64]chan hostedSubscriptionUpdate)
	}
	hub.subscribers[key][id] = updates
	return updates, func() { hub.unsubscribe(key, id) }
}

func (hub *hostedSubscriptionHub) unsubscribe(key string, id uint64) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	subscribers := hub.subscribers[key]
	updates, exists := subscribers[id]
	if !exists {
		return
	}
	delete(subscribers, id)
	close(updates)
	if len(subscribers) == 0 {
		delete(hub.subscribers, key)
	}
}

func (hub *hostedSubscriptionHub) publish(key string, update hostedSubscriptionUpdate) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	subscribers := hub.subscribers[key]
	for id, updates := range subscribers {
		select {
		case updates <- update:
			if update.terminal {
				close(updates)
				delete(subscribers, id)
			}
		default:
			close(updates)
			delete(subscribers, id)
		}
	}
	if len(subscribers) == 0 {
		delete(hub.subscribers, key)
	}
}

func newHostedSubscriptionUpdate(event a2asdk.Event) (*hostedSubscriptionUpdate, error) {
	if event == nil {
		return nil, nil
	}
	if _, inboundMessage := event.(*a2asdk.Message); inboundMessage {
		return nil, nil
	}
	payload, err := json.Marshal(a2asdk.StreamResponse{Event: event})
	if err != nil {
		return nil, ErrHostedTaskStore
	}
	return &hostedSubscriptionUpdate{payload: payload, terminal: hostedEventTerminal(event)}, nil
}

func hostedEventTerminal(event a2asdk.Event) bool {
	switch value := event.(type) {
	case *a2asdk.Task:
		return value.Status.State.Terminal()
	case *a2asdk.TaskStatusUpdateEvent:
		return value.Status.State.Terminal()
	default:
		return false
	}
}

func hostedSubscriptionKey(principal HostedPrincipal, taskID string) string {
	return hostedOwnerKey(principal) + "\x00" + strings.TrimSpace(taskID)
}

type hostedProtocolHTTPHandler struct {
	next          http.Handler
	authenticator HostedAuthenticator
	store         HostedTaskStore
	tenant        string
	hub           *hostedSubscriptionHub
}

func newHostedProtocolHTTPHandler(
	next http.Handler,
	dependencies HostDependencies,
	hub *hostedSubscriptionHub,
) http.Handler {
	return hostedProtocolHTTPHandler{
		next: next, authenticator: dependencies.Authenticator, store: dependencies.TaskStore,
		tenant: strings.TrimSpace(dependencies.Card.Tenant), hub: hub,
	}
}

func (handler hostedProtocolHTTPHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if hostedUnsupportedProtocolVersion(request) {
		writeHostedProtocolError(
			writer, http.StatusBadRequest, "FAILED_PRECONDITION", "VERSION_NOT_SUPPORTED",
			"unsupported A2A protocol version",
		)
		return
	}
	taskID, tenant, subscribe := handler.subscriptionTarget(request)
	if !subscribe || handler.store == nil || handler.authenticator == nil || handler.hub == nil {
		handler.next.ServeHTTP(writer, request)
		return
	}
	handler.serveSubscription(writer, request, taskID, tenant)
}

func hostedUnsupportedProtocolVersion(request *http.Request) bool {
	if request == nil {
		return false
	}
	for _, value := range request.Header.Values(a2asdk.SvcParamVersion) {
		value = strings.TrimSpace(value)
		if value != "" && value != ProtocolVersion {
			return true
		}
	}
	return false
}

func (handler hostedProtocolHTTPHandler) subscriptionTarget(request *http.Request) (string, string, bool) {
	if request == nil || (request.Method != http.MethodGet && request.Method != http.MethodPost) {
		return "", "", false
	}
	path := request.URL.Path
	tenant := ""
	if handler.tenant != "" {
		trimmed := strings.TrimPrefix(path, "/")
		captured, rest, found := strings.Cut(trimmed, "/")
		if !found || captured != handler.tenant {
			return "", captured, strings.HasSuffix(path, ":subscribe")
		}
		tenant, path = captured, "/"+rest
	}
	if !strings.HasPrefix(path, "/tasks/") || !strings.HasSuffix(path, ":subscribe") {
		return "", tenant, false
	}
	taskID := strings.TrimSuffix(strings.TrimPrefix(path, "/tasks/"), ":subscribe")
	if taskID == "" || strings.ContainsAny(taskID, "/\x00\r\n") {
		return "", tenant, false
	}
	return taskID, tenant, true
}

func (handler hostedProtocolHTTPHandler) serveSubscription(
	writer http.ResponseWriter,
	request *http.Request,
	taskID string,
	tenant string,
) {
	if tenant != handler.tenant {
		writeHostedProtocolError(writer, http.StatusForbidden, "PERMISSION_DENIED", "UNAUTHORIZED", "tenant is not allowed")
		return
	}
	principal, err := authenticateHostedPrincipal(request.Context(), handler.authenticator, handler.tenant, HostedCall{
		Method: "SubscribeToTask", Tenant: tenant, Headers: request.Header.Clone(),
	})
	if err != nil {
		handler.writeAuthenticationError(writer, err)
		return
	}
	key := hostedSubscriptionKey(principal, taskID)
	updates, unsubscribe := handler.hub.subscribe(key)
	defer unsubscribe()
	record, err := handler.store.Get(request.Context(), taskID, principal)
	if err != nil {
		handler.writeTaskStoreError(writer, err)
		return
	}
	stored, err := decodeHostedTaskRecord(record, taskID, principal)
	if err != nil {
		handler.writeTaskStoreError(writer, err)
		return
	}
	if stored.Task.Status.State.Terminal() {
		writeHostedProtocolError(
			writer, http.StatusBadRequest, "FAILED_PRECONDITION", "UNSUPPORTED_OPERATION",
			"terminal tasks cannot be subscribed",
		)
		return
	}
	initial, err := json.Marshal(a2asdk.StreamResponse{Event: stored.Task})
	if err != nil {
		writeHostedProtocolError(writer, http.StatusInternalServerError, "INTERNAL", "INTERNAL_ERROR", "subscription failed")
		return
	}
	handler.streamSubscription(writer, request, initial, record.Version, updates)
}

func (handler hostedProtocolHTTPHandler) streamSubscription(
	writer http.ResponseWriter,
	request *http.Request,
	initial []byte,
	initialVersion int64,
	updates <-chan hostedSubscriptionUpdate,
) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeHostedProtocolError(writer, http.StatusInternalServerError, "INTERNAL", "INTERNAL_ERROR", "streaming unavailable")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.WriteHeader(http.StatusOK)
	if writeHostedSSEData(writer, initial) != nil {
		return
	}
	flusher.Flush()
	keepAlive := time.NewTicker(hostedSubscriptionKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-request.Context().Done():
			return
		case <-keepAlive.C:
			if _, err := fmt.Fprint(writer, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case update, open := <-updates:
			if !open {
				return
			}
			if update.version <= initialVersion {
				continue
			}
			if writeHostedSSEData(writer, update.payload) != nil {
				return
			}
			flusher.Flush()
			if update.terminal {
				return
			}
		}
	}
}

func writeHostedSSEData(writer http.ResponseWriter, payload []byte) error {
	_, err := fmt.Fprintf(writer, "data: %s\n\n", payload)
	return err
}

func (hostedProtocolHTTPHandler) writeAuthenticationError(writer http.ResponseWriter, err error) {
	if errors.Is(err, a2asdk.ErrUnauthorized) {
		writeHostedProtocolError(writer, http.StatusForbidden, "PERMISSION_DENIED", "UNAUTHORIZED", "access denied")
		return
	}
	writeHostedProtocolError(writer, http.StatusUnauthorized, "UNAUTHENTICATED", "UNAUTHENTICATED", "authentication required")
}

func (hostedProtocolHTTPHandler) writeTaskStoreError(writer http.ResponseWriter, err error) {
	if errors.Is(err, ErrHostedTaskNotFound) || errors.Is(err, a2asdk.ErrTaskNotFound) {
		writeHostedProtocolError(writer, http.StatusNotFound, "NOT_FOUND", "TASK_NOT_FOUND", "task not found")
		return
	}
	writeHostedProtocolError(writer, http.StatusInternalServerError, "INTERNAL", "INTERNAL_ERROR", "task store failed")
}

func writeHostedProtocolError(writer http.ResponseWriter, code int, status, reason, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(code)
	response := map[string]any{"error": map[string]any{
		"code": code, "status": status, "message": message,
		"details": []map[string]any{{
			"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": reason, "domain": a2asdk.ProtocolDomain,
		}},
	}}
	_ = json.NewEncoder(writer).Encode(response)
}
