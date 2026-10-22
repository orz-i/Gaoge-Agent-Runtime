package model

import (
	"errors"
	"strings"
)

// ClassifiedModelError is the provider-neutral, bounded failure fact produced by
// a host model adapter. Its public code is separate from the internal cause.
// It does NOT imply that another physical request is safe to retry.
type ClassifiedModelError interface {
	error
	ModelFailureCode() string
}

type classifiedModelError struct {
	cause error
	code  string
}

func (e classifiedModelError) Error() string            { return e.cause.Error() }
func (e classifiedModelError) Unwrap() error            { return e.cause }
func (e classifiedModelError) ModelFailureCode() string { return e.code }

// NewClassifiedError marks a known model failure without changing its
// retryability. Unregistered/error-text-derived codes fall back to the original
// error; the Agent will retain its generic agent.model_failed classification.
func NewClassifiedError(cause error, code string) error {
	if cause == nil {
		return nil
	}
	if !knownModelFailureCode(code) {
		return cause
	}
	return classifiedModelError{cause: cause, code: code}
}

// ClassifiedErrorCode traverses wrapped/joined errors. Never use Error() text
// to infer this code: raw Provider responses can contain secrets or untrusted
// text. An unregistered ModelFailureCode implementation is ignored.
func ClassifiedErrorCode(err error) string {
	if err == nil {
		return ""
	}
	var classified ClassifiedModelError
	if errors.As(err, &classified) {
		if code := classified.ModelFailureCode(); knownModelFailureCode(code) {
			return code
		}
	}
	return ""
}

// This is an SDK-level *syntax and semantic* allowlist, not a host-specific
// recovery policy. Hosts interpret the code using their own authorized
// category/owner contracts. Never accept arbitrary dotted Provider identifiers.
func knownModelFailureCode(code string) bool {
	if strings.TrimSpace(code) != code {
		return false
	}
	switch code {
	case "model.upstream_auth_failed",
		"model.upstream_access_denied",
		"model.upstream_quota_exhausted",
		"model.upstream_rate_limited",
		"model.upstream_unavailable",
		"model.upstream_timeout",
		"model.upstream_context_exceeded",
		"model.upstream_request_invalid",
		"model.upstream_request_rejected",
		"model.upstream_model_unavailable",
		"model.upstream_safety_rejected",
		"model.post_generation_failed",
		"model_route_not_configured",
		"model.access_denied",
		"model.entitlement_denied",
		"context.artifact_payload_too_large",
		"context.input_budget_exceeded":
		return true
	default:
		return false
	}
}
