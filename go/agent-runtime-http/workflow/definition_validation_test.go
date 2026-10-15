package workflowhttp_test

import (
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	runtimehttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http"
	workflowhttp "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-http/workflow"
)

func TestDefinitionRequestsEnforceRequiredDraftMembers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	validDraft := `{"id":"audit","revision":0,"name":"Audit","nodes":[{"id":"done","type":"return","return":{"value":true}}]}`
	for _, requestKind := range []struct {
		name   string
		target func() any
	}{
		{"compile", func() any { return &workflowhttp.CompileDefinitionRequest{} }},
		{"publish", func() any { return &workflowhttp.PublishDefinitionRequest{} }},
	} {
		t.Run(requestKind.name, func(t *testing.T) {
			for _, testCase := range []struct {
				name, body string
				valid      bool
			}{
				{"missing draft", `{"scope":{"kind":"actor"}}`, false},
				{"null draft", `{"scope":{"kind":"actor"},"draft":null}`, false},
				{"empty draft", `{"scope":{"kind":"actor"},"draft":{}}`, false},
				{"missing revision", `{"scope":{"kind":"actor"},"draft":{"id":"audit","name":"Audit","nodes":[{"id":"done","type":"return","return":{"value":true}}]}}`, false},
				{"missing nodes", `{"scope":{"kind":"actor"},"draft":{"id":"audit","revision":0,"name":"Audit"}}`, false},
				{"null nodes", `{"scope":{"kind":"actor"},"draft":{"id":"audit","revision":0,"name":"Audit","nodes":null}}`, false},
				{"invalid ID", `{"scope":{"kind":"actor"},"draft":{"id":"","revision":0,"name":"Audit","nodes":[{"id":"done","type":"return","return":{"value":true}}]}}`, false},
				{"valid revision zero", `{"scope":{"kind":"actor"},"draft":` + validDraft + `}`, true},
			} {
				t.Run(testCase.name, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					ctx, _ := gin.CreateTestContext(recorder)
					ctx.Request = httptest.NewRequest(stdhttp.MethodPost, "/", strings.NewReader(testCase.body))
					err := runtimehttp.BindStrictJSON(ctx, requestKind.target())
					if (err == nil) != testCase.valid {
						t.Fatalf("body %q: accepted=%t expected=%t err=%v", testCase.body, err == nil, testCase.valid, err)
					}
				})
			}
		})
	}
}
