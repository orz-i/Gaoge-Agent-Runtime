package transport

import (
	"encoding/json"
	"slices"
	"strings"
)

const PDFInputMIME = "application/pdf"

// SupportsNativeFileInput reports implemented wire support, not a model capability.
func SupportsNativeFileInput(protocol, mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case AdapterOpenAIChatCompletions:
		return mime == PDFInputMIME
	case AdapterOpenAIResponses:
		return slices.Contains(openAIResponsesFileMIMETypes, mime)
	default:
		return false
	}
}

// TextInputFileMIMETypes requires an explicit model declaration for each file format.
// An OpenAI-compatible endpoint or an image capability alone does not establish file support.
func TextInputFileMIMETypes(protocol, capabilitiesJSON string) []string {
	var capabilities struct {
		InputFileMIMETypes []string `json:"inputFileMIMETypes"`
	}
	if json.Unmarshal([]byte(capabilitiesJSON), &capabilities) != nil {
		return nil
	}
	var result []string
	for _, mime := range capabilities.InputFileMIMETypes {
		mime = strings.ToLower(strings.TrimSpace(mime))
		if !SupportsNativeFileInput(protocol, mime) || slices.Contains(result, mime) {
			continue
		}
		if mime == PDFInputMIME && !slices.Contains(TextInputModalities(protocol, capabilitiesJSON), InputModalityImage) {
			continue
		}
		result = append(result, mime)
	}
	return result
}

// PreferNativePDFInput is explicit per-model opt-in and requires real route support.
func PreferNativePDFInput(protocol, capabilitiesJSON string) bool {
	var capability struct {
		PreferNativePDF bool `json:"preferNativePDF"`
	}
	return json.Unmarshal([]byte(capabilitiesJSON), &capability) == nil &&
		capability.PreferNativePDF &&
		slices.Contains(TextInputFileMIMETypes(protocol, capabilitiesJSON), PDFInputMIME)
}

// PublicTextInputFileMIMETypes advertises formats supported by every possible text route.
func PublicTextInputFileMIMETypes(protocolsJSON, capabilitiesJSON string) []string {
	return publicTextInputMIMETypes(protocolsJSON, capabilitiesJSON, TextInputFileMIMETypes)
}

func publicTextInputMIMETypes(protocolsJSON, capabilitiesJSON string, formats func(string, string) []string) []string {
	var protocols []string
	if json.Unmarshal([]byte(protocolsJSON), &protocols) != nil {
		return nil
	}
	var result []string
	initialized := false
	for _, protocol := range protocols {
		if !isTextInputProtocol(protocol) {
			continue
		}
		current := formats(protocol, capabilitiesJSON)
		if !initialized {
			result, initialized = current, true
		} else {
			result = slices.DeleteFunc(result, func(mime string) bool { return !slices.Contains(current, mime) })
		}
	}
	return result
}

// The file formats also admitted by Knowledge uploads. Keep this narrower than the
// upstream endpoint's general file catalog; unverified formats fail closed.
var openAIResponsesFileMIMETypes = []string{
	PDFInputMIME,
	"application/msword",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.ms-excel",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"text/csv",
	"text/plain",
	"text/markdown",
	"application/json",
}
