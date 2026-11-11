package model

import (
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

// ValidateRequestBinding checks nonsecret Host/provider continuation identities.
func ValidateRequestBinding(request Request) error {
	if jsoncontract.ValidateCanonicalUTF8(request) != nil || !validBindingValue(request.BindingRef, 512, true) || !validBindingValue(request.PromptCacheKey, 512, true) || !validBindingValue(request.PreviousResponseID, 512, true) {
		return ErrInvalidContent
	}
	return nil
}
func validBindingValue(value string, limit int, optional bool) bool {
	return (optional || value != "") && len(value) <= limit && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}
