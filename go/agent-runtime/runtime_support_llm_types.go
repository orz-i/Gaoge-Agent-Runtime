package agentruntime

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	EndpointResponses       = "responses"
	EndpointChatCompletions = "chat_completions"

	ContentPartText  = "text"
	ContentPartImage = "image"
	ContentPartFile  = "file"
)

type RouteConfig struct {
	Protocol            string
	BaseURL             string
	APIKey              string
	HeadersJSON         string
	ConnectTimeoutMS    int
	ReadTimeoutMS       int
	StreamIdleTimeoutMS int
	Endpoint            string
	UpstreamModel       string
	AttributionReferer  string
	AttributionTitle    string
}

type ContentPart struct {
	Kind         string
	Text         string
	MimeType     string
	Data         []byte
	FileName     string
	CacheControl *CacheControl
}

type CacheControl struct {
	Type string
	TTL  string
}

type Message struct {
	Role             string
	Content          string
	Parts            []ContentPart
	ReasoningContent string
	ToolCalls        []ToolCall
	ToolResults      []ToolResult
	CacheControl     *CacheControl
}

// ToolChoiceMode is the provider-neutral function/tool calling mode.
type ToolChoiceMode string

const (
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceRequired ToolChoiceMode = "required"
	ToolChoiceNone     ToolChoiceMode = "none"
)

// ToolChoice constrains whether the model must emit native tool calls.
type ToolChoice struct {
	Mode             ToolChoiceMode
	AllowedToolNames []string
}

type GenerateInput struct {
	RequestID          string
	Thread             domain.ThreadRef
	Messages           []Message
	Instructions       string
	Tools              []ToolDefinition
	HostedTools        []HostedTool
	DisableTools       bool
	ToolChoice         ToolChoice
	Options            map[string]interface{}
	PreviousResponseID string
}

type HostedToolVariant struct {
	Protocol string                 `json:"protocol"`
	Payload  map[string]interface{} `json:"payload"`
}

type HostedTool struct {
	ToolKey  string                 `json:"toolKey"`
	Protocol string                 `json:"protocol"`
	Payload  map[string]interface{} `json:"payload"`
}

type ToolDefinition struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type Usage struct {
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	ReasoningTokens    int64
	Speed              string
	ServiceTier        string
	BillingRateClass   string
	RawUsageJSON       string
}

func MergeRawUsageJSON(left string, right string) string {
	left, right = strings.TrimSpace(left), strings.TrimSpace(right)
	if left == "" {
		return right
	}
	if right == "" || right == left {
		return left
	}
	items := make([]interface{}, 0, 2)
	items = appendRawUsageJSON(items, left)
	items = appendRawUsageJSON(items, right)
	if len(items) == 0 {
		return ""
	}
	var value interface{} = items
	if len(items) == 1 {
		value = items[0]
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func appendRawUsageJSON(items []interface{}, raw string) []interface{} {
	var decoded interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &decoded); err != nil {
		return items
	}
	switch value := decoded.(type) {
	case []interface{}:
		return append(items, value...)
	case map[string]interface{}:
		return append(items, value)
	default:
		return items
	}
}

type ToolCall struct {
	ToolCallID       string
	ToolType         string
	ToolName         string
	ArgumentsJSON    string
	ThoughtSignature string
	Status           string
	OutputJSON       string
	ErrorJSON        string
}

type ToolResult struct {
	ToolCallID string
	ToolName   string
	OutputJSON string
	Status     string
	Error      string
}

type ReasoningOutput struct {
	ItemID           string
	Status           string
	Summary          string
	Text             string
	Signature        string
	EncryptedContent string
}

type GenerateOutput struct {
	ResponseID          string
	Text                string
	Reasoning           *ReasoningOutput
	Usage               Usage
	ToolCalls           []ToolCall
	ServerToolCalls     []ToolCall
	ServerSideToolUsage map[string]int64
	Citations           []string
	RawJSON             string
	Debug               *UpstreamDebugSnapshot `json:"-"`
}

type ReasoningDelta struct {
	EventType        string
	ItemID           string
	Status           string
	Kind             string
	Text             string
	Signature        string
	EncryptedContent string
}

type GenerateStreamEvent struct {
	Delta          string
	Reasoning      *ReasoningDelta
	Usage          Usage
	ServerToolCall *ToolCall
	ResponseID     string
}

type UpstreamError struct {
	StatusCode int
	Message    string
	Body       string
	// Operator diagnostics from the model gateway (sanitized).
	ErrorType string
	RequestID string
	Debug     *UpstreamDebugSnapshot
}

type UpstreamDebugSnapshot struct {
	Request  UpstreamDebugRequest  `json:"request"`
	Response UpstreamDebugResponse `json:"response"`
}

type UpstreamDebugRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

type UpstreamDebugResponse struct {
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body"`
}

func (e *UpstreamError) Error() string {
	if strings.TrimSpace(e.Message) == "" {
		return fmt.Sprintf("upstream request failed: status=%d", e.StatusCode)
	}
	return fmt.Sprintf("upstream request failed: status=%d message=%s", e.StatusCode, e.Message)
}

func NormalizeToolName(name string) string {
	value := strings.TrimSpace(name)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	lastUnderscore := false
	for _, r := range value {
		if isToolNameRune(r) {
			builder.WriteRune(r)
			lastUnderscore = false
		} else if !lastUnderscore {
			builder.WriteByte('_')
			lastUnderscore = true
		}
	}
	normalized := strings.Trim(builder.String(), "_-")
	if normalized == "" {
		return ""
	}
	first := []rune(normalized)[0]
	if needsToolNamePrefix(first) {
		normalized = "tool_" + normalized
	}
	if len(normalized) > 64 {
		normalized = normalized[:64]
	}
	return normalized
}

func isToolNameRune(value rune) bool {
	return value == '_' || value == '-' || unicode.IsLetter(value) || unicode.IsDigit(value)
}

func needsToolNamePrefix(value rune) bool {
	return !unicode.IsLetter(value) && value != '_'
}

var _ error = (*UpstreamError)(nil)
