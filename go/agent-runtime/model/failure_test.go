package model_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestClassifiedModelErrorsPreserveCauseAndIndependentRetryPolicy(t *testing.T) {
	t.Parallel()
	secret := errors.New("Bearer sk-secret request body")
	for _, tc := range []struct {
		name string
		code string
	}{
		{"provider-auth", "model.upstream_auth_failed"},
		{"quota", "model.upstream_quota_exhausted"},
		{"rate-limit", "model.upstream_rate_limited"},
		{"gateway-permission", "model.access_denied"},
		{"gateway-entitlement", "model.entitlement_denied"},
		{"context-archive-limit", "context.artifact_payload_too_large"},
		{"context-input-limit", "context.input_budget_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := fmt.Errorf("gateway: %w", model.NewClassifiedError(secret, tc.code))
			if !errors.Is(wrapped, secret) || model.ClassifiedErrorCode(wrapped) != tc.code {
				t.Fatalf("typed cause was lost: code=%q wrapped=%v", model.ClassifiedErrorCode(wrapped), wrapped)
			}
			if model.IsRetryableError(wrapped) {
				t.Fatal("classification must not opt into retry")
			}
			retryable := model.NewRetryableError(wrapped)
			if !model.IsRetryableError(retryable) || model.ClassifiedErrorCode(retryable) != tc.code {
				t.Fatalf("retryability must remain independent of typed cause: %q", model.ClassifiedErrorCode(retryable))
			}
		})
	}
	if model.NewClassifiedError(nil, "model.upstream_auth_failed") != nil {
		t.Fatal("nil cause must remain nil")
	}
}

func TestClassifiedModelErrorRejectsUnknownAndSensitiveCodes(t *testing.T) {
	t.Parallel()
	cause := errors.New("provider failed")
	for _, candidate := range []string{"", "agent.model_failed", "model.401", "provider.PrivateCustomer",
		"model.upstream_auth_failed.private", "model.upstream_auth_failed\nBearer secret",
		strings.Repeat("A", 200)} {
		result := model.NewClassifiedError(cause, candidate)
		if !errors.Is(result, cause) || model.ClassifiedErrorCode(result) != "" || model.IsRetryableError(result) {
			t.Fatalf("untrusted code %q escaped classification: %v", candidate, result)
		}
	}
}
