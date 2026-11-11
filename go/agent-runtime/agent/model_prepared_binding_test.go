package agent

import (
	"encoding/json"

	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestPreparedHostBindingAndDefaultsFreezeAtDispatch(t *testing.T) {
	original := model.Request{RunID: "run", Model: "m", ModelOptions: json.RawMessage(`{"reasoning":{"effort":"high"}}`), Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("hello")}}}
	original = model.CloneRequest(original)
	prepared := model.CloneRequest(original)
	prepared.BindingRef = "snapshot"
	prepared.PromptCacheKey = "cache"
	prepared.PreviousResponseID = "response"
	prepared.ModelOptions = json.RawMessage(`{"reasoning":{"effort":"high","summary":"auto"},"temperature":0.4}`)
	if !validPreparedModelRequest(original, prepared) {
		t.Fatal("safe default materialization rejected")
	}
	if !validDispatchModelRequest(prepared, model.CloneRequest(prepared)) {
		t.Fatal("unchanged sealed input rejected")
	}
	for name, change := range map[string]func(*model.Request){
		"binding":      func(r *model.Request) { r.BindingRef = "changed" },
		"cache":        func(r *model.Request) { r.PromptCacheKey = "changed" },
		"continuation": func(r *model.Request) { r.PreviousResponseID = "changed" },
		"options":      func(r *model.Request) { r.ModelOptions = json.RawMessage(`{"temperature":0.1}`) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := model.CloneRequest(prepared)
			change(&changed)
			if validDispatchModelRequest(prepared, changed) {
				t.Fatal("sealed request changed")
			}
		})
	}
	already := model.CloneRequest(original)
	already.BindingRef = "original"
	if validPreparedModelRequest(already, prepared) {
		t.Fatal("preparer overwrote existing binding")
	}
	for _, invalid := range []string{`{}`, `{"reasoning":{"effort":"low"}}`, `{"reasoning":{"effort":"high","effort":"high"}}`, `null`, `[]`} {
		bad := model.CloneRequest(prepared)
		bad.ModelOptions = json.RawMessage(invalid)
		if validPreparedModelRequest(original, bad) {
			t.Fatalf("accepted unauthorized options: %s", invalid)
		}
	}
}
