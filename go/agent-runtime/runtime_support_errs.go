// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import "errors"

var (
	ErrEnvironmentBindingNotAllowed = errors.New("environment binding is not allowed")
	ErrNotFound                     = errors.New("record not found")
	ErrDuplicate                    = errors.New("duplicate record")
	ErrInvalidInput                 = errors.New("invalid input")
	ErrModelPricingRequired         = errors.New("model pricing required")
	ErrUsageBalanceInsufficient     = errors.New("usage balance insufficient")

	ErrThreadNotFound               = errors.New("thread not found")
	ErrInvalidAttachmentReference   = errors.New("invalid attachment reference")
	ErrAttachmentNotFound           = errors.New("attachment not found")
	ErrAttachmentProcessingNotReady = errors.New("attachment processing not ready")
	ErrAttachmentProcessingFailed   = errors.New("attachment processing failed")
	ErrTooManyAttachments           = errors.New("too many attachments")
	ErrTooManySelectedTools         = errors.New("too many selected tools")
	ErrTooManySelectedSkills        = errors.New("too many selected skills")
	ErrSkillNotFound                = errors.New("skill not found")
	ErrInvalidSkillUse              = errors.New("invalid skill use")
	ErrInvalidThreadBranch          = errors.New("invalid thread branch")
	ErrContextArtifactNotFound      = errors.New("context artifact not found")
	ErrContextBudgetExceeded        = errors.New("context budget exceeded")
	ErrModelRouteNotConfigured      = errors.New("model route not configured")
	ErrStructuredOutputUnsupported  = errors.New("structured output is not supported by the selected model")
	ErrLLMAllRoutesUnavailable      = errors.New("all llm routes unavailable")
	ErrUpstreamRequestFailed        = errors.New("upstream request failed")
	ErrUpstreamEmptyResponse        = errors.New("upstream returned empty response")
	ErrToolRunFinalAnswerMissing    = errors.New("tool run ended without a final answer")
	ErrRunCanceled                  = errors.New("run canceled")
	ErrDuplicateRun                 = errors.New("duplicate run")
	ErrEngineAlreadyStarted         = errors.New("agent runtime engine already started")
	ErrEngineClosed                 = errors.New("agent runtime engine is closed")
	ErrMissingDependency            = errors.New("agent runtime dependency is missing")
)
