package harnesshttp

import (
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func hardcutGET(engine interface {
	ServeHTTP(stdhttp.ResponseWriter, *stdhttp.Request)
}, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(stdhttp.MethodGet, path, nil))
	return response
}

func TestBoundedHarnessHTTPStateAndTimelineOwnerAccess(t *testing.T) {
	host, _, started, _, actor := newAllowanceHTTPFixture(t)
	owner := newAllowanceHTTPEngine(host, actor)
	stateURL := "/v1/harness/turns/" + started.Turn.ID + "/state"
	itemsURL := "/v1/harness/turns/" + started.Turn.ID + "/items"

	light := hardcutGET(owner, stateURL)
	if light.Code != stdhttp.StatusOK {
		t.Fatalf("light status=%d body=%s", light.Code, light.Body)
	}
	var state TurnStateResponse
	if err := json.Unmarshal(light.Body.Bytes(), &state); err != nil || state.Turn.ID != started.Turn.ID ||
		state.Turn.Status != harness.TurnPausedBudget {
		t.Fatalf("light status %+v err=%v", state, err)
	}
	if strings.Contains(light.Body.String(), "admissionHash") || strings.Contains(light.Body.String(), "invocations") ||
		strings.Contains(light.Body.String(), "items") {
		t.Fatalf("private or unbounded detail in light state: %s", light.Body)
	}
	var last uint64
	for pageCount := 0; pageCount < 64; pageCount++ {
		pageResponse := hardcutGET(owner, fmt.Sprintf("%s?afterSeq=%d&limit=2", itemsURL, last))
		if pageResponse.Code != stdhttp.StatusOK {
			t.Fatalf("page status=%d body=%s", pageResponse.Code, pageResponse.Body)
		}
		var page TimelinePageResponse
		if err := json.Unmarshal(pageResponse.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 0 {
			if last == 0 {
				t.Fatal("missing durable timeline")
			}
			break
		}
		for _, item := range page.Items {
			if item.Seq <= last {
				t.Fatalf("nonmonotonic timeline seq=%d last=%d", item.Seq, last)
			}
			last = item.Seq
		}
		if page.NextAfterSeq != last {
			t.Fatalf("bad cursor: %+v last=%d", page, last)
		}
		if pageCount == 63 {
			t.Fatal("Timeline pagination did not terminate")
		}
	}
	for _, invalid := range []string{"?limit=0", "?limit=501", "?limit=bad", "?afterSeq=-1", "?afterSeq=bad"} {
		result := hardcutGET(owner, itemsURL+invalid)
		if result.Code != stdhttp.StatusBadRequest {
			t.Fatalf("unbounded/malformed Timeline accepted: %s status=%d", invalid, result.Code)
		}
	}
	stranger := newAllowanceHTTPEngine(host, kernel.ActorRef{TenantID: "other", ActorID: "intruder"})
	for _, suffix := range []string{"/state", "/items"} {
		denied := hardcutGET(stranger, "/v1/harness/turns/"+started.Turn.ID+suffix)
		missing := hardcutGET(stranger, "/v1/harness/turns/missing-turn"+suffix)
		if denied.Code != stdhttp.StatusNotFound || missing.Code != stdhttp.StatusNotFound ||
			denied.Body.String() != missing.Body.String() {
			t.Fatalf("cross-tenant resource leak: denied=%d %s missing=%d %s",
				denied.Code, denied.Body, missing.Code, missing.Body)
		}
	}
}
