package http

import (
	"bytes"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestBindStrictJSONRejectsMalformedAndInvalidRequestFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name string
		body string
	}{
		{"missing required revision", `{"reason":"why"}`},
		{"zero revision", `{"expectedRevision":0}`},
		{"reason exceeds binding max", `{"expectedRevision":1,"reason":"` + strings.Repeat("a", 2001) + `"}`},
		{"unknown field", `{"expectedRevision":1,"other":true}`},
		{"second JSON document", `{"expectedRevision":1}true`},
		{"trailing object delimiter", `{"expectedRevision":1}}`},
		{"trailing array delimiter", `{"expectedRevision":1}]`},
		{"trailing garbage", `{"expectedRevision":1}garbage`},
		{"empty body", " \n \t "},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(stdhttp.MethodPost, "/", strings.NewReader(testCase.body))
			var request CancelRunRequest
			if err := BindStrictJSON(context, &request); !errors.Is(err, errInvalidJSONBody) {
				t.Fatalf("body was not rejected with invalid request error: %v", err)
			}
		})
	}
}

func TestBindStrictJSONValidatesNestedFieldsAndCollections(t *testing.T) {
	type input struct {
		Thread ThreadRequest `json:"thread" binding:"required"`
		Keys   []string      `json:"keys" binding:"required,min=1,max=2,dive,max=5"`
	}
	gin.SetMode(gin.TestMode)
	for _, body := range []string{
		`{"thread":{"kind":"","id":"thread"},"keys":["tool"]}`,
		`{"thread":{"kind":"conversation","id":""},"keys":["tool"]}`,
		`{"thread":{"kind":"conversation","id":"thread"},"keys":[]}`,
		`{"thread":{"kind":"conversation","id":"thread"},"keys":["tool","tool","tool"]}`,
		`{"thread":{"kind":"conversation","id":"thread"},"keys":["too-long"]}`,
	} {
		context, _ := gin.CreateTestContext(httptest.NewRecorder())
		context.Request = httptest.NewRequest(stdhttp.MethodPost, "/", strings.NewReader(body))
		if err := BindStrictJSON(context, &input{}); !errors.Is(err, errInvalidJSONBody) {
			t.Errorf("invalid nested body %q accepted: err=%v", body, err)
		}
	}
}

func TestBindStrictJSONRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// The same limit applies to raw/unbounded fields, not just binding-tagged DTOs.
	type input struct {
		Blob string `json:"blob"`
	}
	body := `{"blob":"` + strings.Repeat("a", int(maxStrictJSONBodyBytes)) + `"}`
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(stdhttp.MethodPost, "/", strings.NewReader(body))
	if err := BindStrictJSON(context, &input{}); !errors.Is(err, errJSONBodyTooLarge) {
		t.Fatalf("oversized JSON body accepted or misclassified: %v", err)
	}
}

func TestBindStrictJSONAcceptsExactEightMiBBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	type input struct {
		Blob string `json:"blob"`
	}
	prefix, suffix := `{"blob":"`, `"}`
	blobSize := int(maxStrictJSONBodyBytes) - len(prefix) - len(suffix)
	body := prefix + strings.Repeat("x", blobSize) + suffix
	if len(body) != int(maxStrictJSONBodyBytes) {
		t.Fatalf("test input has %d bytes, expected %d", len(body), maxStrictJSONBodyBytes)
	}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(stdhttp.MethodPost, "/", strings.NewReader(body))
	var value input
	if err := BindStrictJSON(context, &value); err != nil || len(value.Blob) != blobSize {
		t.Fatalf("boundary-size JSON failed: blob=%d want=%d err=%v", len(value.Blob), blobSize, err)
	}
}

func TestOversizedStrictJSONReturnsHTTP413(t *testing.T) {
	gin.SetMode(gin.TestMode)
	type input struct {
		Blob string `json:"blob"`
	}
	body := `{"blob":"` + strings.Repeat("x", int(maxStrictJSONBodyBytes)) + `"}`
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(stdhttp.MethodPost, "/", strings.NewReader(body))
	err := BindStrictJSON(context, &input{})
	if err == nil {
		t.Fatal("oversized body accepted")
	}
	InvalidBody(context, err)
	if recorder.Code != stdhttp.StatusRequestEntityTooLarge || !bytes.Contains(recorder.Body.Bytes(), []byte("request.body_too_large")) {
		t.Fatalf("oversized body response: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestBindStrictJSONAcceptsBoundaryAndRestoresBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"expectedRevision":1,"reason":"` + strings.Repeat("a", 2000) + `"} ` + "\n\t"
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest(stdhttp.MethodPost, "/", strings.NewReader(body))
	var request CancelRunRequest
	if err := BindStrictJSON(context, &request); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	if request.ExpectedRevision != 1 || len(request.Reason) != 2000 {
		t.Fatalf("incorrect request values: revision=%d reason length=%d", request.ExpectedRevision, len(request.Reason))
	}
	replayed, err := io.ReadAll(context.Request.Body)
	if err != nil || !bytes.Equal(replayed, []byte(body)) {
		t.Fatalf("valid request body was not restored: err=%v", err)
	}
}

func TestCancelRunDoesNotPersistInvalidBinding(t *testing.T) {
	actor := kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}
	engine, runtime := newAuthorizationHTTPTest(t, actor, nil)
	createAuthorizationRun(t, runtime, actor, "validation-run")

	for _, body := range []string{
		`{"expectedRevision":1,"reason":"` + strings.Repeat("a", 2001) + `"}`,
		`{"expectedRevision":0,"reason":"why"}`,
		`{"expectedRevision":1}]`,
	} {
		response := performAuthorizationRequest(
			t, engine, stdhttp.MethodPost, "/api/v1/runs/validation-run/cancel", bytes.NewBufferString(body),
		)
		if response.Code != stdhttp.StatusBadRequest {
			t.Fatalf("invalid cancellation succeeded: status=%d body=%s", response.Code, response.Body.String())
		}
		snapshot, err := runtime.Load(t.Context(), "validation-run")
		if err != nil || snapshot.Run.Status != kernel.RunStatusRunning || snapshot.Run.Revision != 1 {
			t.Fatalf("invalid cancellation changed run: %#v err=%v", snapshot.Run, err)
		}
	}
}
