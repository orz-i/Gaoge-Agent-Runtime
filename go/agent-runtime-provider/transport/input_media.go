package transport

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

var errUnsupportedNativeMediaInput = errors.New("unsupported native media input")

// Reject media that a transport would otherwise silently discard, before HTTP.
func validateNativeMediaInput(route RouteConfig, input GenerateInput) error {
	modalities := TextInputModalities(route.Protocol, route.ModelCapabilitiesJSON)
	var fileBytes int64
	for _, message := range input.Messages {
		for _, part := range message.Parts {
			if err := validateNativeMediaPart(route, message.Role, part, modalities, &fileBytes); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateNativeMediaPart(route RouteConfig, role string, part ContentPart, modalities []string, fileBytes *int64) error {
	if part.Kind == ContentPartFile && len(part.Data) > 0 {
		*fileBytes += int64(len(part.Data))
		return validateNativeFilePart(route, role, part, *fileBytes)
	}
	if part.Kind != ContentPartAudio && part.Kind != ContentPartVideo {
		return nil
	}
	if !slices.Contains(modalities, part.Kind) || len(part.Data) == 0 || !strings.HasPrefix(strings.ToLower(part.MimeType), part.Kind+"/") {
		return fmt.Errorf("%w: invalid %s input for %s", errUnsupportedNativeMediaInput, part.Kind, route.Protocol)
	}
	if part.Kind != ContentPartVideo {
		return nil
	}
	*fileBytes += int64(len(part.Data))
	return validateNativeVideoPart(route, role, part, *fileBytes)
}

func validateNativeFilePart(route RouteConfig, role string, part ContentPart, fileBytes int64) error {
	if normalizeRole(role) != valueUserD301E989 || strings.TrimSpace(part.FileName) == "" ||
		!SupportsNativeFileInput(route.Protocol, part.MimeType) || fileBytes > 20<<20 {
		return fmt.Errorf("%w: invalid file input for %s", errUnsupportedNativeMediaInput, route.Protocol)
	}
	return nil
}

func validateNativeVideoPart(route RouteConfig, role string, part ContentPart, fileBytes int64) error {
	if normalizeRole(role) != valueUserD301E989 || fileBytes > 20<<20 ||
		!slices.Contains(TextInputVideoMIMETypes(route.Protocol, route.ModelCapabilitiesJSON), strings.ToLower(strings.TrimSpace(part.MimeType))) {
		return fmt.Errorf("%w: invalid video input for %s", errUnsupportedNativeMediaInput, route.Protocol)
	}
	return nil
}
