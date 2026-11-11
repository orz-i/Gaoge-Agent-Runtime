package transport

import "slices"

const (
	InputVideoMIMEMP4       = "video/mp4"
	InputVideoMIMEAVI       = "video/x-msvideo"
	InputVideoMIMEQuickTime = "video/quicktime"
	InputVideoMIMEMOV       = "video/mov"
	InputVideoMIMEMatroska  = "video/x-matroska"
)

// TextInputVideoMIMETypes describes implemented formats after model admission.
func TextInputVideoMIMETypes(protocol, capabilitiesJSON string) []string {
	if !slices.Contains(TextInputModalities(protocol, capabilitiesJSON), InputModalityVideo) {
		return nil
	}
	result := []string{InputVideoMIMEMP4, InputVideoMIMEAVI, "video/avi", InputVideoMIMEQuickTime, InputVideoMIMEMOV, InputVideoMIMEMatroska}
	if NormalizeAdapter(protocol) == AdapterGoogleInteractions {
		result = append(result, "video/webm", "video/mpeg", "video/3gpp")
	}
	return result
}

func PublicTextInputVideoMIMETypes(protocolsJSON, capabilitiesJSON string) []string {
	return publicTextInputMIMETypes(protocolsJSON, capabilitiesJSON, TextInputVideoMIMETypes)
}
