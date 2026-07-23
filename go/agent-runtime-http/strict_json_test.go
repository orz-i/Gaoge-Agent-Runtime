package http

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const (
	evidenceOutputKindTest     = "output"
	evidenceProjectionKindTest = "projection"
)

func TestTextRunRejectsRemovedStoryWorkspaceFields(t *testing.T) {
	for _, body := range []string{
		`{"input":{"content":"hello"},"workspace":{"schemaVersion":6,"type":"story"}}`,
		`{"input":{"content":"hello"},"workspace":{"schemaVersion":7,"type":"story","publicID":"story_1"}}`,
		`{"input":{"content":"hello"},"workspace":{"schemaVersion":7,"type":"story","task":"ask"}}`,
		`{"input":{"content":"hello"},"workspace":{"schemaVersion":7,"type":"story","baseRevision":1}}`,
		`{"input":{"content":"hello"},"workspace":{"schemaVersion":7,"type":"story","intent":{}}}`,
	} {
		ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginContext.Request = httptest.NewRequestWithContext(context.Background(), "POST", "/conversations/id/runs", strings.NewReader(body))
		var request StartTextRunRequest
		err := bindStrictJSON(ginContext, &request)
		if err == nil {
			t.Fatalf("legacy Story workspace accepted: %s", body)
		}
	}
}

func TestEvidenceRequestUsesNeutralSourceContract(t *testing.T) {
	tests := []struct {
		name string
		body string
		ok   bool
	}{
		{name: evidenceOutputKindTest, body: `{"source":{"kind":"output","id":"output-1","version":1},"selection":{"kind":"text_range","start":0,"end":2}}`, ok: true},
		{name: evidenceProjectionKindTest, body: `{"source":{"kind":"projection","thread":{"kind":"conversation","id":"thread-1"},"projection":{"kind":"conversation.message","id":"message-1"}},"selection":{"kind":"full"}}`, ok: true},
		{name: "legacy message source", body: `{"source":{"kind":"conversation.message","id":"message-1"},"selection":{"kind":"full_message"}}`},
		{name: "legacy response fields", body: `{"source":{"kind":"projection","thread":{"kind":"conversation","id":"thread-1"},"projection":{"kind":"conversation.message","id":"message-1"},"messageID":"message-1"},"selection":{"kind":"full"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
			ginContext.Request = httptest.NewRequestWithContext(context.Background(), "POST", "/api/v1/evidence", strings.NewReader(test.body))
			var request CreateEvidenceRequest
			err := bindStrictJSON(ginContext, &request)
			if test.ok && err != nil {
				t.Fatalf("neutral evidence request rejected: %v", err)
			}
			if !test.ok && err == nil {
				t.Fatalf("legacy evidence request accepted: %s", test.body)
			}
		})
	}
}
