package transport

import (
	"net/url"
	"strings"
)

func buildVersionedEndpointURL(baseURL string, defaultVersion string, endpointPath string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return ""
	}
	path := "/" + strings.TrimLeft(strings.TrimSpace(endpointPath), "/")
	if llmBaseEndsWithVersionSegment(base) {
		return base + path
	}
	version := strings.Trim(strings.TrimSpace(defaultVersion), "/")
	if version == "" {
		return base + path
	}
	return base + "/" + version + path
}

func llmBaseEndsWithVersionSegment(raw string) bool {
	parsed, err := url.Parse(raw)
	path := raw
	if err == nil {
		path = parsed.Path
	}
	path = strings.Trim(path, "/")
	if path == "" {
		return false
	}
	segments := strings.Split(path, "/")
	return isLLMAPIVersionSegment(segments[len(segments)-1])
}

func isLLMAPIVersionSegment(segment string) bool {
	value := strings.ToLower(strings.TrimSpace(segment))
	if len(value) < 2 || value[0] != 'v' {
		return false
	}
	suffix, ok := splitLLMAPIVersionSuffix(value)
	if !ok {
		return false
	}
	return suffix == "" || isLLMAPIVersionSuffix(suffix)
}

func splitLLMAPIVersionSuffix(value string) (string, bool) {
	index := consumeLLMAPIVersionDigits(value)
	if index == 1 {
		return "", false
	}
	return value[index:], true
}

func consumeLLMAPIVersionDigits(value string) int {
	index := 1
	for index < len(value) && value[index] >= '0' && value[index] <= '9' {
		index++
	}
	return index
}

func isLLMAPIVersionSuffix(suffix string) bool {
	for _, prefix := range []string{"alpha", "beta", "preview"} {
		if suffix == prefix {
			return true
		}
		if strings.HasPrefix(suffix, prefix) && allASCIIDigits(suffix[len(prefix):]) {
			return true
		}
	}
	return false
}

func allASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}
