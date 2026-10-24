package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const (
	lookupKey  = "task.lookup_release"
	draftKey   = "task.save_draft"
	hostTurnID = "release-R-17"
	taskPrompt = "Find release R-17's deployment code and deployment window in the release dossier. Recover exact evidence from its Context artifact, save one local draft, and return JSON with release, deploymentCode, window, artifactID and draftID."
)

var answerSchema = json.RawMessage(`{"type":"object","properties":{"release":{"type":"string"},"deploymentCode":{"type":"string"},"window":{"type":"string"},"artifactID":{"type":"string"},"draftID":{"type":"string"}},"required":["release","deploymentCode","window","artifactID","draftID"],"additionalProperties":false}`)

type sourceFixture struct {
	code, window string
}

func defaultSource() sourceFixture {
	return sourceFixture{code: "LIME-42", window: "Wednesday 03:00-03:30 UTC"}
}

func (source sourceFixture) record() string {
	return fmt.Sprintf("RELEASE R-17 DEPLOYMENT_CODE=%s; WINDOW=%s;", source.code, source.window)
}

func (source sourceFixture) document() string {
	// Both ends are uninformative. The fact is deep inside the exact payload,
	// beyond either bounded compaction excerpt and the reader's first page.
	padding := strings.Repeat("audit log: routine checks passed; 核验记录。\n", 1200)
	return padding + source.record() + "\n" + padding
}

type taskAnswer struct {
	Release        string `json:"release"`
	DeploymentCode string `json:"deploymentCode"`
	Window         string `json:"window"`
	ArtifactID     string `json:"artifactID"`
	DraftID        string `json:"draftID"`
}

type artifactPage struct {
	ArtifactID  string `json:"artifactID"`
	ContentHash string `json:"contentHash"`
	Content     string `json:"content"`
	Offset      int    `json:"offset"`
	Found       bool   `json:"found"`
}

type taskState struct {
	store       harness.Store
	clock       *demoClock
	source      sourceFixture
	mu          sync.Mutex
	lookupCalls int
	reads       []artifactPage
	drafts      []taskAnswer
	requests    []model.Request
}

func (state *taskState) registrations() []tools.Registration {
	return []tools.Registration{
		{
			Definition: tools.Definition{
				Key: lookupKey, Name: "lookup_release", Description: "Load the synthetic release dossier. Large results are archived; search the exact artifact for RELEASE R-17.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"release":{"type":"string"}},"required":["release"],"additionalProperties":false}`),
			},
			Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				var input struct {
					Release string `json:"release"`
				}
				if err := json.Unmarshal(request.Call.Arguments, &input); err != nil {
					return tools.ExecutionResult{}, err
				}
				if input.Release != "R-17" {
					return tools.ExecutionResult{}, tools.NewRecoverableCallError("task.unknown_release", "The available synthetic release is R-17.", nil)
				}
				state.mu.Lock()
				state.lookupCalls++
				state.mu.Unlock()
				return toolResult(request.Call.ID, "read", map[string]string{"release": input.Release, "document": state.source.document()})
			}),
		},
		{
			Definition: tools.Definition{
				Key: draftKey, Name: "save_draft", Description: "Append one local in-memory draft, using exact release facts and the artifact ID just read. This operation is deliberately not deduplicated; do it once.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"release":{"type":"string"},"deploymentCode":{"type":"string"},"window":{"type":"string"},"artifactID":{"type":"string"}},"required":["release","deploymentCode","window","artifactID"],"additionalProperties":false}`),
			},
			Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
				var draft taskAnswer
				if err := json.Unmarshal(request.Call.Arguments, &draft); err != nil {
					return tools.ExecutionResult{}, err
				}
				state.mu.Lock()
				draft.DraftID = fmt.Sprintf("draft-%d", len(state.drafts)+1)
				state.drafts = append(state.drafts, draft)
				state.mu.Unlock()
				return toolResult(request.Call.ID, "committed", draft)
			}),
		},
	}
}

func toolResult(callID, disposition string, value any) (tools.ExecutionResult, error) {
	content, err := json.Marshal(value)
	return tools.ExecutionResult{Content: content, Receipt: tools.Receipt{ExecutionID: callID, Disposition: disposition}}, err
}

// scriptedModel has no cursor, expected answer or access to the source fixture.
// Each choice is derived from the actual model-visible request, including after
// replacement by a new model instance. It tests mechanics, not model quality.
type scriptedModel struct{}

func (scriptedModel) Capabilities(context.Context, string) (model.Capabilities, error) {
	return model.Capabilities{StructuredJSONSchema: true}, nil
}

var (
	artifactPattern = regexp.MustCompile(`\[tool_result_compacted artifact_id=([^\s]+)`)
	recordPattern   = regexp.MustCompile(`RELEASE R-17 DEPLOYMENT_CODE=([^;]+); WINDOW=([^;]+);`)
)

func (scriptedModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	if saved, ok := toolMessage(request.Messages, draftKey); ok {
		var answer taskAnswer
		if err := json.Unmarshal([]byte(saved), &answer); err != nil {
			return model.Response{}, fmt.Errorf("decode saved draft: %w", err)
		}
		content, err := json.Marshal(answer)
		return model.Response{Content: string(content)}, err
	}
	if archived, ok := toolMessage(request.Messages, harness.ContextArtifactToolKey); ok {
		var page artifactPage
		if err := json.Unmarshal([]byte(archived), &page); err != nil {
			return model.Response{}, fmt.Errorf("decode artifact page: %w", err)
		}
		facts := recordPattern.FindStringSubmatch(page.Content)
		if !page.Found || len(facts) != 3 {
			return model.Response{}, errors.New("scripted model did not receive the deep release record")
		}
		return toolCall("save-release-draft", draftKey, map[string]string{
			"release": "R-17", "deploymentCode": facts[1], "window": facts[2], "artifactID": page.ArtifactID,
		})
	}
	if dossier, ok := toolMessage(request.Messages, lookupKey); ok {
		marker := artifactPattern.FindStringSubmatch(dossier)
		if len(marker) != 2 || recordPattern.MatchString(dossier) {
			return model.Response{}, errors.New("lookup result was not archived with its deep facts excluded")
		}
		return toolCall("read-release-evidence", harness.ContextArtifactToolKey, map[string]any{
			"artifactID": marker[1], "query": "RELEASE R-17", "limit": 300,
		})
	}
	return toolCall("lookup-release", lookupKey, map[string]string{"release": "R-17"})
}

func toolCall(id, key string, arguments any) (model.Response, error) {
	body, err := json.Marshal(arguments)
	return model.Response{ToolCalls: []tools.Call{{ID: id, ToolKey: key, Arguments: body}}}, err
}

func toolMessage(messages []model.Message, key string) (string, bool) {
	calls := make(map[string]string)
	var content string
	found := false
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			calls[call.ID] = call.ToolKey
		}
		if message.Role == model.RoleTool && calls[message.ToolCallID] == key {
			content, found = message.Content, true
		}
	}
	return content, found
}
