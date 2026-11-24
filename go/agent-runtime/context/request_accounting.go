package context

import (
	stdcontext "context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"unicode/utf8"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// AccountingProjection is Host-owned route accounting material, not an
// executable request. SystemPrompt is the route's complete leading System
// prompt, even when the original transcript already contains it. ModelOptions
// replaces the options in the accounting view (nil means no options).
type AccountingProjection struct {
	ModelOptions         json.RawMessage
	SystemPrompt         string
	ReservedOutputTokens int64
}

// RequestAccounting resolves the accounting projection for a frozen request
// header. The Manager supplies an isolated request WITHOUT Messages or an
// InvocationID. Provider defaults, Tool accounting and output reservation
// must depend on that header and the immutable route, not transcript contents.
// The port never grants execution or claims an exact token count.
type RequestAccounting func(stdcontext.Context, model.Request, ModelWindow) (AccountingProjection, error)

// FrozenAccounting records only byte facts and identity digests for the request
// header and the optional leading System message. It contains no provider
// options, private prompt text, or transcript. A request's messages and
// invocation ID may vary; all other fields must match the frozen header.
// This makes every candidate, including a reconstructed summary target,
// measurable with the same recipe without reentering Host code.
type FrozenAccounting struct {
	WindowFingerprint    string `json:"windowFingerprint"`
	HeaderHash           string `json:"headerHash"`
	OriginalHeaderBytes  int64  `json:"originalHeaderBytes"`
	CanonicalHeaderBytes int64  `json:"canonicalHeaderBytes"`
	SystemPromptHash     string `json:"systemPromptHash,omitempty"`
	SystemPromptBytes    int64  `json:"systemPromptBytes,omitempty"`
}

func accountingHeader(request model.Request) model.Request {
	request.Messages = nil
	request.InvocationID = ""
	return model.CloneRequest(request)
}

func accountingDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func validAccountingHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// Valid checks the representation of a frozen accounting recipe.
func (frame FrozenAccounting) Valid() bool {
	return validAccountingHash(frame.WindowFingerprint) && validAccountingHash(frame.HeaderHash) && frame.OriginalHeaderBytes > 0 &&
		frame.CanonicalHeaderBytes > 0 && frame.SystemPromptBytes >= 0 &&
		((frame.SystemPromptBytes == 0 && frame.SystemPromptHash == "") ||
			(frame.SystemPromptBytes > 0 && validAccountingHash(frame.SystemPromptHash)))
}

// FreezeRequestAccounting resolves the optional Host port exactly once for a
// request header. A nil Manager or absent port is the identity projection.
func (manager *Manager) FreezeRequestAccounting(ctx stdcontext.Context, request model.Request, window ModelWindow) (FrozenAccounting, error) {
	if _, err := EffectiveInputLimit(window, 0); err != nil {
		return FrozenAccounting{}, err
	}
	header := accountingHeader(request)
	raw, err := model.MarshalRequestForInputCount(header)
	if err != nil {
		return FrozenAccounting{}, errors.Join(ErrInvalidInput, err)
	}
	frame := FrozenAccounting{WindowFingerprint: ModelWindowFingerprint(window), HeaderHash: accountingDigest(raw), OriginalHeaderBytes: int64(len(raw)), CanonicalHeaderBytes: int64(len(raw))}
	if manager == nil || manager.accounting == nil {
		return frame, nil
	}
	projected, err := manager.accounting(ctx, model.CloneRequest(header), window)
	if err != nil {
		return FrozenAccounting{}, err
	}
	if projected.ReservedOutputTokens < 0 || projected.ReservedOutputTokens != window.ReservedOutputTokens ||
		!utf8.ValidString(projected.SystemPrompt) {
		return FrozenAccounting{}, ErrInvalidInput
	}
	header.ModelOptions = append(json.RawMessage(nil), projected.ModelOptions...)
	header.MaxOutputTokens = projected.ReservedOutputTokens
	canonical, err := model.MarshalRequestForInputCount(header)
	if err != nil {
		return FrozenAccounting{}, errors.Join(ErrInvalidInput, err)
	}
	frame.CanonicalHeaderBytes = int64(len(canonical))
	if projected.SystemPrompt != "" {
		header.Messages = []model.Message{{Role: model.RoleSystem, Content: model.TextParts(projected.SystemPrompt)}}
		withPrompt, err := model.MarshalRequestForInputCount(header)
		if err != nil {
			return FrozenAccounting{}, errors.Join(ErrInvalidInput, err)
		}
		frame.SystemPromptBytes = int64(len(withPrompt) - len(canonical))
		frame.SystemPromptHash = accountingDigest([]byte(projected.SystemPrompt))
	}
	return frame, nil
}

// MatchesWindow binds the frozen reservation and context limit.
func (frame FrozenAccounting) MatchesWindow(window ModelWindow) bool {
	return frame.WindowFingerprint == ModelWindowFingerprint(window)
}

// MatchesRequest verifies the frame's scope without accessing the Host.
func (frame FrozenAccounting) MatchesRequest(request model.Request) bool {
	if !frame.Valid() {
		return false
	}
	header, err := model.MarshalRequestForInputCount(accountingHeader(request))
	return err == nil && int64(len(header)) == frame.OriginalHeaderBytes && accountingDigest(header) == frame.HeaderHash
}

// canonicalBytes derives the exact serialized size of the canonical
// provider-neutral accounting request. JSON encodes the unchanged transcript
// and invocation ID identically; only the header and optional leading message
// differ. The digest implements the route's leading-prompt de-duplication
// without persisting the private prompt. No projection replaces original bytes.
func (frame FrozenAccounting) canonicalBytes(request model.Request, originalBytes int64) (int64, error) {
	if !frame.MatchesRequest(request) || originalBytes < frame.OriginalHeaderBytes {
		return 0, ErrInvalidInput
	}
	remainder := originalBytes - frame.OriginalHeaderBytes
	if remainder > math.MaxInt64-frame.CanonicalHeaderBytes {
		return 0, ErrInvalidInput
	}
	result := remainder + frame.CanonicalHeaderBytes
	if frame.SystemPromptBytes > 0 && (len(request.Messages) == 0 ||
		request.Messages[0].Role != model.RoleSystem ||
		accountingDigest([]byte(model.TextValue(request.Messages[0].Content))) != frame.SystemPromptHash) {
		if frame.SystemPromptBytes > math.MaxInt64-result {
			return 0, ErrInvalidInput
		}
		result += frame.SystemPromptBytes
		if len(request.Messages) > 0 {
			if result == math.MaxInt64 {
				return 0, ErrInvalidInput
			}
			result++ // comma separating the leading prompt from the transcript
		}
	}
	return result, nil
}

// SerializedBytes returns the conservative maximum of the original typed
// request and the canonical accounting form. It is safe to use on recovery.
func (frame FrozenAccounting) SerializedBytes(request model.Request) (int64, error) {
	request = model.CloneRequest(request)
	raw, err := model.MarshalRequestForInputCount(request)
	if err != nil {
		return 0, errors.Join(ErrInvalidInput, err)
	}
	canonical, err := frame.canonicalBytes(request, int64(len(raw)))
	if err != nil {
		return 0, err
	}
	return max(int64(len(raw)), canonical), nil
}

// AssessModelRequest is the deterministic, counter-free assessment of a
// frozen accounting frame. It cannot admit media using text estimates.
func (frame FrozenAccounting) AssessModelRequest(request model.Request, window ModelWindow, policy Policy) (Assessment, error) {
	return (*Manager)(nil).AssessModelRequestWithAccounting(stdcontext.Background(), request, window, policy, frame)
}
