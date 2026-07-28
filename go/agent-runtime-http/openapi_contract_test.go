package http

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/goccy/go-yaml"
)

type openAPIOperationExpectation struct {
	status      string
	requestBody string
	parameters  []string
}

const (
	openAPIThreadKind    = "threadKind"
	openAPIThreadID      = "threadID"
	openAPILimit         = "limit"
	openAPIQueueID       = "queueID"
	openAPIOutputID      = "outputID"
	openAPIVersion       = "version"
	openAPIJobID         = "jobID"
	openAPITenantID      = "tenantID"
	openAPIManifestID    = "manifestID"
	openAPIManifestScope = "scope"
	openAPIOwnerActorID  = "ownerActorID"
	openAPIOffset        = "offset"
	openAPIRevision      = "revision"
	openAPIStatus        = "status"
	openAPIJoinID        = "joinID"
	openAPIWorkflowID    = "workflowID"
)

func TestOpenAPIContractMatchesRuntimeRouterAndHandlerSemantics(t *testing.T) {
	document := loadOpenAPIDocument(t)
	paths := openAPIObject(t, document, "paths")
	components := openAPIObject(t, document, "components")
	schemas := openAPIObject(t, components, "schemas")
	parameters := openAPIObject(t, components, "parameters")

	expected := map[string]openAPIOperationExpectation{
		"GET /runs":                                                            {status: "200", parameters: []string{openAPIThreadKind, openAPIThreadID, "page", "pageSize"}},
		"POST /runs":                                                           {status: "202", requestBody: "CreateRunRequest"},
		"POST /workflows":                                                      {status: "202", requestBody: "StartWorkflowRequest"},
		"POST /agent-teams":                                                    {status: "202", requestBody: "StartAgentTeamRequest"},
		"GET /runs/{runID}":                                                    {status: "200", parameters: []string{valueRunID1DA2F0B6}},
		"GET /runs/{runID}/result":                                             {status: "200", parameters: []string{valueRunID1DA2F0B6}},
		"POST /runs/{runID}/cancel":                                            {status: "200", parameters: []string{valueRunID1DA2F0B6}},
		"POST /runs/{runID}/resume":                                            {status: "202", requestBody: "ResumeRunRequest", parameters: []string{valueRunID1DA2F0B6}},
		"POST /runs/{runID}/retire":                                            {status: "200", parameters: []string{valueRunID1DA2F0B6}},
		"POST /runs/{runID}/handoffs":                                          {status: "202", requestBody: "DelegateRunRequest", parameters: []string{valueRunID1DA2F0B6}},
		"POST /runs/{runID}/handoff-joins":                                     {status: "201", requestBody: "CreateRunHandoffJoinRequest", parameters: []string{valueRunID1DA2F0B6}},
		"GET /runs/{runID}/handoff-joins":                                      {status: "200", parameters: []string{valueRunID1DA2F0B6, openAPIStatus, openAPILimit, openAPIOffset}},
		"GET /runs/{runID}/handoff-joins/{joinID}":                             {status: "200", parameters: []string{valueRunID1DA2F0B6, openAPIJoinID}},
		"GET /runs/{runID}/task-tree":                                          {status: "200", parameters: []string{valueRunID1DA2F0B6}},
		"GET /runs/{runID}/events":                                             {status: "200", parameters: []string{valueRunID1DA2F0B6, "afterSeq"}},
		"GET /runs/{runID}/events/history":                                     {status: "200", parameters: []string{valueRunID1DA2F0B6, "beforeSeq", openAPILimit}},
		"GET /runs/{runID}/events/{eventID}":                                   {status: "200", parameters: []string{valueRunID1DA2F0B6, "eventID"}},
		"GET /runs/{runID}/plan":                                               {status: "200", parameters: []string{valueRunID1DA2F0B6}},
		"GET /runs/{runID}/interactions":                                       {status: "200", parameters: []string{valueRunID1DA2F0B6}},
		"POST /runs/{runID}/interactions/{interactionID}/resolve":              {status: "200", requestBody: "ResolveInteractionRequest", parameters: []string{valueRunID1DA2F0B6, "interactionID"}},
		"GET /runs/{runID}/checkpoints":                                        {status: "200", parameters: []string{valueRunID1DA2F0B6}},
		"GET /runs/{runID}/outputs":                                            {status: "200", parameters: []string{valueRunID1DA2F0B6}},
		"GET /runs/{runID}/workbench":                                          {status: "200", parameters: []string{valueRunID1DA2F0B6}},
		"GET /run-queue":                                                       {status: "200", parameters: []string{openAPIThreadKind, openAPIThreadID}},
		"POST /run-queue":                                                      {status: "202", requestBody: "QueueCreateRequest"},
		"PATCH /run-queue/{queueID}":                                           {status: "200", requestBody: "QueueUpdateRequest", parameters: []string{openAPIQueueID}},
		"DELETE /run-queue/{queueID}":                                          {status: "200", parameters: []string{openAPIQueueID, openAPIThreadKind, openAPIThreadID}},
		"POST /run-queue/{queueID}/prioritize":                                 {status: "200", parameters: []string{openAPIQueueID, openAPIThreadKind, openAPIThreadID}},
		"POST /run-queue/{queueID}/interrupt-and-send":                         {status: "200", parameters: []string{openAPIQueueID, openAPIThreadKind, openAPIThreadID}},
		"GET /outputs":                                                         {status: "200", parameters: []string{"q", "cursor", openAPILimit}},
		"GET /outputs/{outputID}":                                              {status: "200", parameters: []string{openAPIOutputID, openAPIVersion}},
		"GET /outputs/{outputID}/versions":                                     {status: "200", parameters: []string{openAPIOutputID, "cursor", openAPILimit}},
		"GET /outputs/{outputID}/versions/{version}/preview":                   {status: "200", parameters: []string{openAPIOutputID, openAPIVersion}},
		"GET /outputs/{outputID}/versions/{version}/download":                  {status: "200", parameters: []string{openAPIOutputID, openAPIVersion}},
		"POST /evidence":                                                       {status: "200", requestBody: "CreateEvidenceRequest"},
		"GET /agent-manifests":                                                 {status: "200", parameters: []string{openAPILimit, openAPIOffset}},
		"GET /agent-manifests/{manifestID}":                                    {status: "200", parameters: []string{openAPIManifestID, openAPIRevision}},
		"GET /workflow-definitions":                                            {status: "200", parameters: []string{openAPILimit, openAPIOffset}},
		"GET /workflow-definitions/{workflowID}":                               {status: "200", parameters: []string{openAPIWorkflowID, openAPIRevision}},
		"GET /admin/agentruntime/continuations":                                {status: "200", parameters: []string{openAPITenantID, "actorID", openAPIStatus, valueRunID1DA2F0B6, openAPIJobID, "source", openAPILimit, openAPIOffset}},
		"POST /admin/agentruntime/continuations/{jobID}/requeue":               {status: "200", requestBody: "RequeueContinuationRequest", parameters: []string{openAPIJobID}},
		"GET /admin/agentruntime/agent-manifests":                              {status: "200", parameters: []string{openAPIStatus, openAPIManifestScope, openAPITenantID, openAPIOwnerActorID, openAPILimit, openAPIOffset}},
		"POST /admin/agentruntime/agent-manifests":                             {status: "201", requestBody: "AgentManifestRevisionRequest"},
		"POST /admin/agentruntime/agent-manifests/{manifestID}/revisions":      {status: "201", requestBody: "AgentManifestRevisionRequest", parameters: []string{openAPIManifestID}},
		"GET /admin/agentruntime/workflow-definitions":                         {status: "200", parameters: []string{openAPIStatus, openAPIManifestScope, openAPITenantID, openAPIOwnerActorID, openAPILimit, openAPIOffset}},
		"POST /admin/agentruntime/workflow-definitions":                        {status: "201", requestBody: "WorkflowDefinitionRevisionRequest"},
		"POST /admin/agentruntime/workflow-definitions/validate":               {status: "200", requestBody: "WorkflowDefinitionRevisionRequest"},
		"POST /admin/agentruntime/workflow-definitions/{workflowID}/revisions": {status: "201", requestBody: "WorkflowDefinitionRevisionRequest", parameters: []string{openAPIWorkflowID}},
	}

	assertOpenAPIRouterCoverage(t, paths, expected)
	operationIDs := make(map[string]string, len(expected))
	for key, want := range expected {
		method, routePath, _ := strings.Cut(key, " ")
		operation := openAPIOperation(t, paths, method, routePath)
		operationID := openAPIString(t, operation, "operationId")
		if previous := operationIDs[operationID]; previous != "" {
			t.Fatalf("operationId %q is shared by %s and %s", operationID, previous, key)
		}
		operationIDs[operationID] = key
		assertOpenAPIResponse(t, key, operation, want.status)
		assertOpenAPIRequestBody(t, key, operation, want.requestBody)
		assertOpenAPIParameters(t, key, operation, want.parameters, parameters)
	}

	assertOpenAPISchemaRequired(t, schemas, "ResumeRunRequest", "clientResumeID")
	assertOpenAPISchemaOptional(t, schemas, "ResumeRunRequest", "checkpointID")
	assertOpenAPISchemaRequired(t, schemas, "QueueCreateRequest", "clientQueueID")
	assertOpenAPISchemaOptional(t, schemas, "CreateRunRequest", "agentManifest")
	assertOpenAPISchemaRequired(t, schemas, "StartAgentTeamRequest", "clientTeamID")
	assertOpenAPISchemaRequired(t, schemas, "StartAgentTeamRequest", "coordinatorManifest")
	assertOpenAPISchemaRequired(t, schemas, "StartAgentTeamRequest", "members")
	assertOpenAPISchemaRequired(t, schemas, "StartAgentTeamRequest", "join")
	assertOpenAPISchemaRequired(t, schemas, "AgentTeamMemberRequest", "memberID")
	assertOpenAPISchemaRequired(t, schemas, "AgentTeamMemberRequest", "agentManifest")
	assertOpenAPISchemaRequired(t, schemas, "AgentTeamStartResult", "rootRun")
	assertOpenAPISchemaRequired(t, schemas, "AgentTeamStartResult", "tasks")
	assertOpenAPISchemaRequired(t, schemas, "AgentTeamStartResult", "join")
	assertOpenAPISchemaOptional(t, schemas, "QueueCreateRequest", "agentManifest")
	assertOpenAPISchemaOptional(t, schemas, "QueueUpdateRequest", "agentManifest")
	assertOpenAPISchemaRequired(t, schemas, "QueueUpdateRequest", "expectedRevision")
	assertOpenAPISchemaRequired(t, schemas, "RequeueContinuationRequest", "reason")
	assertOpenAPISchemaRequired(t, schemas, "DelegateRunRequest", "clientHandoffID")
	assertOpenAPISchemaRequired(t, schemas, "DelegateRunRequest", "agentManifest")
	assertOpenAPISchemaRequired(t, schemas, "CreateRunHandoffJoinRequest", "clientJoinID")
	assertOpenAPISchemaRequired(t, schemas, "CreateRunHandoffJoinRequest", "handoffIDs")
	assertOpenAPISchemaRequired(t, schemas, "RunTaskTree", "joins")
	assertOpenAPISchemaRequired(t, schemas, "AgentManifestRevisionRequest", "name")
	assertOpenAPISchemaOptional(t, schemas, "AgentManifestRevisionRequest", openAPIManifestScope)
	assertOpenAPISchemaRequired(t, schemas, "AgentManifest", openAPIManifestScope)
	assertOpenAPISchemaRequired(t, schemas, "AgentManifest", openAPITenantID)
	assertOpenAPISchemaRequired(t, schemas, "AgentManifest", openAPIOwnerActorID)
	assertOpenAPISchemaRequired(t, schemas, "StartWorkflowRequest", "definition")
	assertOpenAPISchemaRequired(t, schemas, "StartWorkflowRequest", "input")
	assertOpenAPISchemaRequired(t, schemas, "WorkflowDefinitionRevisionRequest", "root")
	assertOpenAPISchemaRequired(t, schemas, "WorkflowDefinitionRevisionRequest", "limits")
	assertOpenAPISchemaRequired(t, schemas, "WorkflowDefinition", "dependencies")
	assertOpenAPISchemaReferencesExist(t, document, schemas)
}

func loadOpenAPIDocument(t *testing.T) map[string]any {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "contracts", "agent-runtime", "v1", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read OpenAPI contract: %v", err)
	}
	var document map[string]any
	if err = yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("parse OpenAPI contract: %v", err)
	}
	return document
}

func assertOpenAPIRouterCoverage(t *testing.T, paths map[string]any, expected map[string]openAPIOperationExpectation) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	module := NewModule(&Handler{})
	module.RegisterRoutes(router.Group("/api/v1"))
	module.RegisterAdminRoutes(router.Group("/api/v1/admin"))
	actual := make([]string, 0, len(router.Routes()))
	for _, route := range router.Routes() {
		path := strings.TrimPrefix(route.Path, "/api/v1")
		path = strings.NewReplacer(":run_id", "{runID}", ":event_id", "{eventID}", ":interaction_id", "{interactionID}", ":queue_id", "{queueID}", ":output_id", "{outputID}", ":version", "{version}", ":job_id", "{jobID}", ":manifest_id", "{manifestID}", ":join_id", "{joinID}", ":workflow_id", "{workflowID}").Replace(path)
		key := route.Method + " " + path
		actual = append(actual, key)
		if _, ok := expected[key]; !ok {
			t.Errorf("Gin route %s is missing from the semantic expectation table", key)
		}
	}
	if len(actual) != len(expected) {
		sort.Strings(actual)
		t.Fatalf("Gin/OpenAPI operation count mismatch: router=%d expected=%d routes=%v", len(actual), len(expected), actual)
	}
	for key := range expected {
		method, routePath, _ := strings.Cut(key, " ")
		_ = openAPIOperation(t, paths, method, routePath)
	}
}

func assertOpenAPIResponse(t *testing.T, key string, operation map[string]any, status string) {
	t.Helper()
	responses := openAPIObject(t, operation, "responses")
	response := openAPIObject(t, responses, status)
	if key != "GET /outputs/{outputID}/versions/{version}/download" {
		content := openAPIObject(t, response, "content")
		mediaType := "application/json"
		if key == "GET /runs/{runID}/events" {
			mediaType = "application/x-ndjson"
		}
		schema := openAPIObject(t, openAPIObject(t, content, mediaType), "schema")
		if !strings.HasPrefix(openAPIString(t, schema, "$ref"), "#/components/schemas/") {
			t.Fatalf("%s success response must use a named schema", key)
		}
	}
	defaultResponse := openAPIObject(t, responses, "default")
	if got := openAPIString(t, defaultResponse, "$ref"); got != "#/components/responses/Error" {
		t.Fatalf("%s default response = %q, want stable Error response", key, got)
	}
}

func assertOpenAPIRequestBody(t *testing.T, key string, operation map[string]any, schemaName string) {
	t.Helper()
	body, exists := operation["requestBody"]
	if schemaName == "" {
		if exists {
			t.Fatalf("%s unexpectedly declares a request body: %#v", key, body)
		}
		return
	}
	requestBody, ok := body.(map[string]any)
	if !ok || requestBody["required"] != true {
		t.Fatalf("%s request body must be required", key)
	}
	schema := openAPIObject(t, openAPIObject(t, openAPIObject(t, requestBody, "content"), "application/json"), "schema")
	if got := openAPIString(t, schema, "$ref"); got != "#/components/schemas/"+schemaName {
		t.Fatalf("%s request schema = %q, want %s", key, got, schemaName)
	}
}

func assertOpenAPIParameters(t *testing.T, key string, operation map[string]any, expected []string, components map[string]any) {
	t.Helper()
	values, _ := operation["parameters"].([]any)
	actual := make([]string, 0, len(values))
	for _, value := range values {
		parameter, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%s contains an invalid parameter", key)
		}
		if ref, ok := parameter["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/components/parameters/")
			parameter = openAPIObject(t, components, name)
		}
		actual = append(actual, openAPIString(t, parameter, "name"))
	}
	if fmt.Sprint(actual) != fmt.Sprint(expected) {
		t.Fatalf("%s parameters = %v, want %v", key, actual, expected)
	}
}

func assertOpenAPISchemaRequired(t *testing.T, schemas map[string]any, schemaName, field string) {
	t.Helper()
	schema := openAPIObject(t, schemas, schemaName)
	for _, required := range openAPIRequiredFields(schema) {
		if required == field {
			return
		}
	}
	for _, member := range openAPIAllOfMembers(schema) {
		for _, required := range openAPIRequiredFields(member) {
			if required == field {
				return
			}
		}
	}
	t.Fatalf("schema %s must require %s", schemaName, field)
}

func assertOpenAPISchemaOptional(t *testing.T, schemas map[string]any, schemaName, field string) {
	t.Helper()
	for _, required := range openAPIRequiredFields(openAPIObject(t, schemas, schemaName)) {
		if required == field {
			t.Fatalf("schema %s must not require %s", schemaName, field)
		}
	}
}

func assertOpenAPISchemaReferencesExist(t *testing.T, value any, schemas map[string]any) {
	t.Helper()
	for _, ref := range openAPISchemaReferences(value) {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		if _, exists := schemas[name]; !exists {
			t.Errorf("OpenAPI references missing schema %s", name)
		}
	}
}

func openAPISchemaReferences(value any) []string {
	result := make([]string, 0)
	stack := []any{value}
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		references, children := openAPISchemaNode(current)
		result = append(result, references...)
		stack = append(stack, children...)
	}
	return result
}

func openAPISchemaNode(value any) ([]string, []any) {
	switch typed := value.(type) {
	case map[string]any:
		references := make([]string, 0, 1)
		children := make([]any, 0, len(typed))
		for key, child := range typed {
			if key == "$ref" {
				if ref, ok := child.(string); ok && strings.HasPrefix(ref, "#/components/schemas/") {
					references = append(references, ref)
				}
			}
			children = append(children, child)
		}
		return references, children
	case []any:
		return nil, typed
	default:
		return nil, nil
	}
}

func openAPIOperation(t *testing.T, paths map[string]any, method, routePath string) map[string]any {
	t.Helper()
	pathItem := openAPIObject(t, paths, routePath)
	return openAPIObject(t, pathItem, strings.ToLower(method))
}

func openAPIObject(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI object %q is missing or invalid", key)
	}
	return value
}

func openAPIString(t *testing.T, parent map[string]any, key string) string {
	t.Helper()
	value, ok := parent[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		t.Fatalf("OpenAPI string %q is missing or invalid", key)
	}
	return value
}

func openAPIRequiredFields(schema map[string]any) []string {
	values, _ := schema["required"].([]any)
	result := make([]string, 0, len(values))
	for _, value := range values {
		if field, ok := value.(string); ok {
			result = append(result, field)
		}
	}
	return result
}

func openAPIAllOfMembers(schema map[string]any) []map[string]any {
	values, _ := schema["allOf"].([]any)
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if member, ok := value.(map[string]any); ok {
			result = append(result, member)
		}
	}
	return result
}
