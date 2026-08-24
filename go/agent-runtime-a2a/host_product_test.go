package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
)

func TestProductionHostRejectsImplicitSecurityAndMemoryState(t *testing.T) {
	t.Parallel()
	_, err := NewHost(HostDependencies{
		Card:  HostedCard{PublicURL: "https://agent.example/a2a", Name: "agent", Version: "1"},
		Agent: &testHostedAgent{}, Policy: HostPolicy{Production: true},
	})
	if !errors.Is(err, ErrInvalidHost) {
		t.Fatalf("expected fail-closed production host, got %v", err)
	}
	_, err = NewHost(HostDependencies{
		Card: productionHostedCard("http://agent.example/a2a"), Agent: &testHostedAgent{},
		Authenticator: hostedBearerAuthenticator(), TaskStore: newTestHostedTaskStore(),
		Policy: HostPolicy{Production: true},
	})
	if !errors.Is(err, ErrInvalidHost) {
		t.Fatalf("expected HTTPS production host, got %v", err)
	}
}

func TestProductionHostAuthenticatesIsolatesAndRecoversDurableTasks(t *testing.T) {
	t.Parallel()
	store := newTestHostedTaskStore()
	firstAgent := &testHostedAgent{}
	first := newA2AProductionTestServer(t, store, firstAgent)
	alice := newHostedAuthClient(t, first.Client(), "alice")
	discovery, err := alice.Discover(t.Context(), first.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.SecuritySchemes) != 1 || discovery.SecuritySchemes[0].Name != "bearer" ||
		discovery.Descriptor.Tenant != "tenant-a" {
		t.Fatalf("production discovery = %#v", discovery)
	}
	interaction, err := alice.SendMessage(t.Context(), discovery, SendRequest{
		MessageID: "production-message", Text: "persist this",
	})
	if err != nil || interaction.Task == nil || !interaction.Task.Terminal {
		t.Fatalf("interaction=%#v err=%v", interaction, err)
	}
	if len(firstAgent.requests) != 1 || firstAgent.requests[0].Principal.Subject != "alice" ||
		firstAgent.requests[0].Principal.Tenant != "tenant-a" {
		t.Fatalf("hosted principal = %#v", firstAgent.requests)
	}
	page, err := alice.ListTasks(t.Context(), discovery, ListTasksRequest{PageSize: 10, IncludeArtifacts: true})
	if err != nil || len(page.Tasks) != 1 {
		t.Fatalf("alice page=%#v err=%v", page, err)
	}
	taskID := interaction.Task.ID
	first.Close()

	second := newA2AProductionTestServer(t, store, &testHostedAgent{})
	aliceAfterRestart := newHostedAuthClient(t, second.Client(), "alice")
	secondDiscovery, err := aliceAfterRestart.Discover(t.Context(), second.URL)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := aliceAfterRestart.GetTask(t.Context(), secondDiscovery, taskID)
	if err != nil || recovered.ID != taskID || !recovered.Terminal {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
	bob := newHostedAuthClient(t, second.Client(), "bob")
	bobDiscovery, err := bob.Discover(t.Context(), second.URL)
	if err != nil {
		t.Fatal(err)
	}
	bobPage, err := bob.ListTasks(t.Context(), bobDiscovery, ListTasksRequest{PageSize: 10})
	if err != nil || len(bobPage.Tasks) != 0 {
		t.Fatalf("bob page=%#v err=%v", bobPage, err)
	}
	if _, err = bob.GetTask(t.Context(), bobDiscovery, taskID); err == nil {
		t.Fatal("cross-principal task read unexpectedly succeeded")
	}
}

func newA2AProductionTestServer(
	t *testing.T,
	store HostedTaskStore,
	agent HostedAgent,
) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(nil)
	publicURL := "https://" + server.Listener.Addr().String()
	host, err := NewHost(HostDependencies{
		Card: productionHostedCard(publicURL), Agent: agent,
		Authenticator: hostedBearerAuthenticator(), TaskStore: store,
		Policy: HostPolicy{
			Production: true, AgentInactivityTimeout: 5 * time.Second, MaxConcurrentExecutions: 4,
		},
	})
	if err != nil {
		server.Close()
		t.Fatal(err)
	}
	server.Config.Handler = host.Handler()
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func productionHostedCard(publicURL string) HostedCard {
	return HostedCard{
		PublicURL: publicURL, Name: "production-agent", Description: "production fixture",
		Version: testAgentVersion, Tenant: "tenant-a",
		DefaultInputModes: []string{"text/plain"}, DefaultOutputModes: []string{"text/plain"},
		Skills: []RemoteAgentSkill{{ID: "hosted", Name: "Hosted", Description: "Hosted skill", Tags: []string{"test"}}},
		SecuritySchemes: []HostedSecurityScheme{{
			Name: "bearer", Kind: HostedSecurityHTTP, Scheme: "Bearer", BearerFormat: "opaque",
		}},
		SecurityRequirements: []HostedSecurityRequirement{{Schemes: map[string][]string{"bearer": {}}}},
	}
}

func hostedBearerAuthenticator() HostedAuthenticator {
	return HostedAuthenticatorFunc(func(_ context.Context, call HostedCall) (HostedPrincipal, error) {
		value := strings.TrimSpace(call.Headers.Get("Authorization"))
		if !strings.HasPrefix(value, "Bearer ") {
			return HostedPrincipal{}, ErrHostedUnauthenticated
		}
		subject := strings.TrimSpace(strings.TrimPrefix(value, "Bearer "))
		if subject == "" {
			return HostedPrincipal{}, ErrHostedUnauthenticated
		}
		return HostedPrincipal{
			Subject: subject, Tenant: call.Tenant, Attributes: json.RawMessage(`{"source":"test"}`),
		}, nil
	})
}

func newHostedAuthClient(t *testing.T, httpClient *http.Client, subject string) *Client {
	t.Helper()
	transport, err := NewTransport(TransportDependencies{
		HTTPClient: httpClient, EndpointValidator: EndpointValidatorFunc(func(string) error { return nil }),
		Headers: HeaderProviderFunc(func(context.Context) (http.Header, error) {
			return http.Header{"Authorization": []string{"Bearer " + subject}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientDependencies{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type testHostedTaskStore struct {
	mu      sync.Mutex
	records map[string]HostedTaskRecord
}

func newTestHostedTaskStore() *testHostedTaskStore {
	return &testHostedTaskStore{records: make(map[string]HostedTaskRecord)}
}

func (store *testHostedTaskStore) Create(_ context.Context, record HostedTaskRecord) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := testHostedTaskKey(record.OwnerTenant, record.OwnerSubject, record.TaskID)
	if _, exists := store.records[key]; exists {
		return 0, ErrHostedTaskAlreadyExists
	}
	record.Version = 1
	store.records[key] = cloneHostedTaskRecord(record)
	return record.Version, nil
}

func (store *testHostedTaskStore) Update(_ context.Context, update HostedTaskUpdate) (int64, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	key := testHostedTaskKey(update.OwnerTenant, update.OwnerSubject, update.TaskID)
	record, exists := store.records[key]
	if !exists {
		return 0, ErrHostedTaskNotFound
	}
	if record.Version != update.PreviousVersion {
		return 0, ErrHostedTaskConflict
	}
	record.Version++
	record.Task = append(json.RawMessage(nil), update.Task...)
	store.records[key] = record
	return record.Version, nil
}

func (store *testHostedTaskStore) Get(
	_ context.Context,
	taskID string,
	principal HostedPrincipal,
) (HostedTaskRecord, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, exists := store.records[testHostedTaskKey(principal.Tenant, principal.Subject, taskID)]
	if !exists {
		return HostedTaskRecord{}, ErrHostedTaskNotFound
	}
	return cloneHostedTaskRecord(record), nil
}

func (store *testHostedTaskStore) List(_ context.Context, query HostedTaskQuery) (HostedTaskPage, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	records := make([]HostedTaskRecord, 0)
	for _, record := range store.records {
		if record.OwnerSubject != query.OwnerSubject || record.OwnerTenant != query.OwnerTenant {
			continue
		}
		var task a2asdk.Task
		if json.Unmarshal(record.Task, &task) != nil ||
			(query.ContextID != "" && task.ContextID != query.ContextID) ||
			(query.State != "" && string(task.Status.State) != query.State) {
			continue
		}
		if !query.IncludeArtifacts {
			task.Artifacts = nil
		}
		if query.HistoryLength != nil && *query.HistoryLength >= 0 && len(task.History) > *query.HistoryLength {
			task.History = task.History[len(task.History)-*query.HistoryLength:]
		}
		record.Task, _ = json.Marshal(task)
		records = append(records, cloneHostedTaskRecord(record))
	}
	sort.Slice(records, func(left, right int) bool { return records[left].TaskID < records[right].TaskID })
	total := len(records)
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	if len(records) > pageSize {
		records = records[:pageSize]
	}
	return HostedTaskPage{Tasks: records, TotalSize: total, PageSize: pageSize}, nil
}

func testHostedTaskKey(tenant, subject, taskID string) string {
	return tenant + "\x00" + subject + "\x00" + taskID
}

func cloneHostedTaskRecord(record HostedTaskRecord) HostedTaskRecord {
	record.Task = append(json.RawMessage(nil), record.Task...)
	return record
}

var _ HostedTaskStore = (*testHostedTaskStore)(nil)
