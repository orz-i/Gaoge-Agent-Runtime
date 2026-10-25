package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const maxPreflightDebugAnswerBytes = 64 << 10

// This is a visible, bounded failure diagnostic. It never participates in the
// probe's JSON/schema decision and contains no request headers or transport error text.
type preflightResponseDebug struct {
	Capture          wireCaptureState `json:"capture"`
	Body             string           `json:"body"`
	CapturedBytes    int              `json:"capturedBytes"`
	BodyTruncated    bool             `json:"bodyTruncated"`
	NormalizedAnswer string           `json:"normalizedAnswer"`
	AnswerTruncated  bool             `json:"answerTruncated"`
	Redacted         bool             `json:"redacted"`
}

func (observer *preflightHTTPObserver) responseDebugSnapshot(answer, key string) *preflightResponseDebug {
	observer.mu.Lock()
	capture := observer.responseWire
	observer.mu.Unlock()
	body, state := capture.snapshotPrefix()
	text, bodyRedacted := redactPreflightDebugText(string(body), key, state != wireComplete)
	answer, answerRedacted := redactPreflightDebugText(answer, key, false)
	text, bodyTruncated := limitPreflightDebugText(text, maxPreflightWireBytes)
	answer, answerTruncated := limitPreflightDebugText(answer, maxPreflightDebugAnswerBytes)
	return &preflightResponseDebug{
		Capture: state, Body: text, CapturedBytes: len(body),
		BodyTruncated:    state == wireTooLarge || bodyTruncated,
		NormalizedAnswer: answer, AnswerTruncated: answerTruncated,
		Redacted: bodyRedacted || answerRedacted,
	}
}

// Preserve the original formatting unless something actually needs redaction.
// Decoding JSON first also catches credentials hidden by JSON string escapes.
func redactPreflightDebugText(text, key string, partial bool) (string, bool) {
	marker := "[REDACTED]"
	if key != "" && strings.Contains(marker, key) {
		marker = ""
	}
	// Match a byte prefix before replacing a UTF-8 rune cut by the capture cap.
	prefixRedacted := false
	if partial {
		text, prefixRedacted = redactPreflightDebugKeyTail(text, key, marker)
	}
	text = strings.ToValidUTF8(text, "\uFFFD")
	replaceKey := preflightDebugKeyReplacer(key, marker)
	if json.Valid([]byte(text)) {
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) == nil {
			value, changed := redactPreflightDebugJSON(value, marker, replaceKey)
			if !changed {
				return text, prefixRedacted
			}
			if encoded, err := json.Marshal(value); err == nil {
				return string(encoded), true
			}
		}
	}
	// A truncated body may still contain complete JSON strings, including an
	// escaped key or a credential field. Inspect those without hiding the rest.
	text, changed := redactPreflightDebugFragments(text, key, marker, replaceKey)
	text, plainChanged := replaceKey(text)
	if partial {
		var tailChanged bool
		text, tailChanged = redactPreflightDebugKeyTail(text, key, marker)
		changed = changed || tailChanged
	}
	return text, prefixRedacted || changed || plainChanged
}

func redactPreflightDebugJSON(value any, marker string, replaceKey func(string) (string, bool)) (any, bool) {
	switch item := value.(type) {
	case string:
		return replaceKey(item)
	case []any:
		changed := false
		for index, child := range item {
			next, redacted := redactPreflightDebugJSON(child, marker, replaceKey)
			item[index], changed = next, changed || redacted
		}
		return item, changed
	case map[string]any:
		result := make(map[string]any, len(item))
		changed := false
		for name, child := range item {
			nextName, nameChanged := replaceKey(name)
			next, childChanged := redactPreflightDebugJSON(child, marker, replaceKey)
			if preflightCredentialField(name) {
				childChanged = childChanged || child != marker
				next = marker
			}
			result[nextName], changed = next, changed || nameChanged || childChanged
		}
		return result, changed
	default:
		return value, false
	}
}

func preflightCredentialField(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "api_key", "apikey", "x-api-key", "access_token", "refresh_token":
		return true
	default:
		return false
	}
}

// Match literal keys and their JSON-escaped equivalents even when the surrounding
// body is malformed. A per-render closure compiles the generated matcher once.
func preflightDebugKeyReplacer(key, marker string) func(string) (string, bool) {
	var escaped *regexp.Regexp
	return func(text string) (string, bool) {
		if key == "" {
			return text, false
		}
		if !strings.Contains(text, "\\") {
			if !strings.Contains(text, key) {
				return text, false
			}
			return strings.ReplaceAll(text, key, marker), true
		}
		if escaped == nil {
			var pattern strings.Builder
			for _, character := range key {
				pattern.WriteString("(?:")
				pattern.WriteString(regexp.QuoteMeta(string(character)))
				encoded, _ := json.Marshal(string(character))
				if jsonEscape := string(encoded[1 : len(encoded)-1]); jsonEscape != string(character) {
					pattern.WriteByte('|')
					pattern.WriteString(regexp.QuoteMeta(jsonEscape))
				}
				if character == '/' {
					pattern.WriteByte('|')
					pattern.WriteString(regexp.QuoteMeta("\\/"))
				}
				if character <= 0xFFFF {
					fmt.Fprintf(&pattern, "|(?i:\\\\u%04x)", character)
				} else {
					high, low := utf16.EncodeRune(character)
					fmt.Fprintf(&pattern, "|(?i:\\\\u%04x\\\\u%04x)", high, low)
				}
				pattern.WriteByte(')')
			}
			// Every component is an escaped literal or a fixed hex escape.
			escaped = regexp.MustCompile(pattern.String())
		}
		if !escaped.MatchString(text) {
			return text, false
		}
		return escaped.ReplaceAllStringFunc(text, func(string) string { return marker }), true
	}
}

// A capture can end midway through a literal or JSON-escaped key. Decode only
// the bounded tail for matching, keeping source offsets so all other text stays.
func redactPreflightDebugKeyTail(text, key, marker string) (string, bool) {
	if key == "" {
		return text, false
	}
	start := max(0, len(text)-6*len(key)-6)
	tail := text[start:]
	var decoded strings.Builder
	var offsets []int
	for index := 0; index < len(tail); {
		chunk, width := tail[index:index+1], 1
		if tail[index] == '\\' {
			if index+1 == len(tail) {
				break
			}
			escapeWidth := 2
			if tail[index+1] == 'u' {
				escapeWidth = 6
				if index+escapeWidth > len(tail) {
					break
				}
				code, err := strconv.ParseUint(tail[index+2:index+6], 16, 16)
				if err == nil && code >= 0xD800 && code <= 0xDBFF && strings.HasPrefix(tail[index+6:], "\\u") {
					escapeWidth = 12
					if index+escapeWidth > len(tail) {
						break
					}
				}
			}
			var unescaped string
			if json.Unmarshal([]byte("\""+tail[index:index+escapeWidth]+"\""), &unescaped) == nil {
				chunk, width = unescaped, escapeWidth
			}
		}
		decoded.WriteString(chunk)
		for range len(chunk) {
			offsets = append(offsets, index)
		}
		index += width
	}
	value := decoded.String()
	for length := min(len(value), len(key)); length > 0; length-- {
		if strings.HasSuffix(value, key[:length]) {
			return text[:start+offsets[len(value)-length]] + marker, true
		}
	}
	return text, false
}

func redactPreflightDebugFragments(text, key, marker string, replaceKey func(string) (string, bool)) (string, bool) {
	var output strings.Builder
	changed := false
	for offset := 0; offset < len(text); {
		start := strings.IndexByte(text[offset:], '"')
		if start < 0 {
			output.WriteString(text[offset:])
			break
		}
		start += offset
		output.WriteString(text[offset:start])
		end := preflightDebugStringEnd(text, start)
		if end == 0 {
			tail, redacted := redactPreflightDebugStringPrefix(text[start:], key, marker, replaceKey)
			output.WriteString(tail)
			changed = changed || redacted
			break
		}
		var decoded string
		if json.Unmarshal([]byte(text[start:end]), &decoded) != nil {
			output.WriteString(text[start:end])
			offset = end
			continue
		}
		next, redacted := replaceKey(decoded)
		if redacted {
			encoded, _ := json.Marshal(next)
			output.Write(encoded)
		} else {
			output.WriteString(text[start:end])
		}
		changed = changed || redacted
		offset = end
		colon := skipPreflightDebugSpace(text, end)
		if !preflightCredentialField(decoded) || colon == len(text) || text[colon] != ':' {
			continue
		}
		startValue := skipPreflightDebugSpace(text, colon+1)
		output.WriteString(text[end:startValue])
		if startValue == len(text) {
			offset = startValue
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(text[startValue:]))
		decoder.UseNumber()
		var value any
		valueEnd := len(text)
		if decoder.Decode(&value) == nil {
			valueEnd = startValue + int(decoder.InputOffset())
		} else if text[startValue] != '"' {
			// Malformed scalar values need not suppress later diagnostic fields.
			if separator := strings.IndexAny(text[startValue:], ",}]\r\n"); separator >= 0 {
				valueEnd = startValue + separator
			}
		} else if closed := preflightDebugStringEnd(text, startValue); closed != 0 {
			valueEnd = closed
		}
		encoded, _ := json.Marshal(marker)
		output.Write(encoded)
		changed = true
		offset = valueEnd
	}
	return output.String(), changed
}

func preflightDebugStringEnd(text string, start int) int {
	for index := start + 1; index < len(text); index++ {
		switch text[index] {
		case '\\':
			index++
		case '"':
			return index + 1
		}
	}
	return 0
}

func skipPreflightDebugSpace(text string, offset int) int {
	for offset < len(text) && strings.ContainsRune(" \t\r\n", rune(text[offset])) {
		offset++
	}
	return offset
}

// Complete the last, unfinished JSON string only for redaction. A truncated
// escape occupies at most five trailing bytes; all other captured text remains.
func redactPreflightDebugStringPrefix(raw, key, marker string, replaceKey func(string) (string, bool)) (string, bool) {
	for trim := 0; trim <= 5 && len(raw)-trim >= 1; trim++ {
		var decoded string
		if json.Unmarshal([]byte(raw[:len(raw)-trim]+"\""), &decoded) != nil {
			continue
		}
		next, changed := replaceKey(decoded)
		next, tailChanged := redactPreflightDebugKeyTail(next, key, marker)
		if !changed && !tailChanged {
			return raw, false
		}
		encoded, _ := json.Marshal(next)
		// Keep an unfinished string unfinished. A trailing partial escape is
		// part of the masked key only when its preceding prefix matched.
		result := string(encoded[:len(encoded)-1])
		if !tailChanged {
			result += raw[len(raw)-trim:]
		}
		return result, true
	}
	return replaceKey(raw)
}

func limitPreflightDebugText(text string, limit int) (string, bool) {
	text = strings.ToValidUTF8(text, "\uFFFD")
	if len(text) <= limit {
		return text, false
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit], true
}
