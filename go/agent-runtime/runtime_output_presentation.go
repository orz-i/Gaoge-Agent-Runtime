package agentruntime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueCode57466930     = "code"
	valueFileBE372696     = "file"
	valueImage8FE9EE53    = "image"
	valueKind55B00946     = "kind"
	valueMarkdown6D2DD3CA = "markdown"
	valueOutput6DD2E13C   = "output"
	valueProjectionSource = "projection"
	valueFull             = "full"
	valueTextRange        = "text_range"
	valueSummaryFF969ED3  = "summary"
	valueTableF71860DA    = "table"
	valueText6CED98CE     = "text"
)

const (
	outputPreviewMaxBytes = 256 * 1024
	evidenceMaxRunes      = 20 * 1024
)

var (
	ErrEvidenceSelectionInvalid  = errors.New("evidence selection invalid")
	ErrEvidenceSelectionTooLarge = errors.New("evidence selection exceeds the rune limit")
)

type OutputPreview struct {
	Type      string     `json:"type"`
	Content   string     `json:"content,omitempty"`
	Language  string     `json:"language,omitempty"`
	FileID    string     `json:"fileID,omitempty"`
	MIMEType  string     `json:"mimeType,omitempty"`
	Rows      [][]string `json:"rows,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
}

type CreateEvidenceInput struct {
	Actor                  model.ActorRef
	Thread                 model.ThreadRef
	Projection             model.ProjectionRef
	SourceKind             string
	OutputID, Kind, Title  string
	Version                int
	Start, End             int
	RowStart, RowEnd       int
	ColumnStart, ColumnEnd int
}

func (s *Engine) GetOutputVersion(ctx context.Context, actor model.ActorRef, outputID string, version int) (*model.OutputListItem, error) {
	if !validActorRef(actor) || strings.TrimSpace(outputID) == "" {
		return nil, ErrInvalidInput
	}
	return s.repo.GetOutputVersion(ctx, actor, strings.TrimSpace(outputID), version)
}

func (s *Engine) ListOutputVersions(ctx context.Context, actor model.ActorRef, outputID string, beforeVersion, limit int) ([]model.OutputListItem, bool, error) {
	if !validActorRef(actor) || strings.TrimSpace(outputID) == "" {
		return nil, false, ErrInvalidInput
	}
	return s.repo.ListOutputVersions(ctx, actor, strings.TrimSpace(outputID), beforeVersion, limit)
}

func (s *Engine) BuildOutputPreview(ctx context.Context, actor model.ActorRef, outputID string, version int) (*model.OutputListItem, *OutputPreview, error) {
	item, err := s.GetOutputVersion(ctx, actor, outputID, version)
	if err != nil {
		return nil, nil, err
	}
	preview, err := s.buildOutputPreview(ctx, actor, *item)
	return item, preview, err
}

func (s *Engine) buildOutputPreview(ctx context.Context, actor model.ActorRef, item model.OutputListItem) (*OutputPreview, error) {
	output := item.OutputRef
	if output.FileID != "" {
		return s.buildFileOutputPreview(ctx, actor, output)
	}
	return buildInlineOutputPreview(output), nil
}

func (s *Engine) buildFileOutputPreview(ctx context.Context, actor model.ActorRef, output model.OutputRef) (*OutputPreview, error) {
	content, err := s.openOutputAttachment(ctx, actor, output.FileID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = content.Reader.Close() }()
	mimeType := firstNonEmptyString(output.FileMIMEType, content.ContentType)
	if !outputPreviewTextMIME(mimeType) {
		previewType := valueFileBE372696
		if strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			previewType = valueImage8FE9EE53
		}
		return &OutputPreview{Type: previewType, FileID: output.FileID, MIMEType: mimeType}, nil
	}
	data, truncated, err := readOutputPreview(content.Reader)
	if err != nil {
		return nil, err
	}
	return outputTextPreview(string(data), mimeType, output.Kind, output.FileID, truncated), nil
}

func buildInlineOutputPreview(output model.OutputRef) *OutputPreview {
	var payload map[string]interface{}
	if json.Unmarshal([]byte(output.PreviewJSON), &payload) == nil {
		if content, ok := payload["content"].(string); ok {
			bounded, truncated := truncateOutputPreview(content)
			return outputTextPreview(bounded, fmt.Sprint(payload["mimeType"]), output.Kind, "", truncated)
		}
		if rows := outputPreviewRows(payload["rows"]); len(rows) > 0 {
			return &OutputPreview{Type: valueTableF71860DA, Rows: rows}
		}
	}
	content, truncated := truncateOutputPreview(output.Summary)
	return &OutputPreview{Type: valueSummaryFF969ED3, Content: content, MIMEType: "text/plain; charset=utf-8", Truncated: truncated}
}

func (s *Engine) OpenOutputDownload(ctx context.Context, actor model.ActorRef, outputID string, version int) (*model.OutputListItem, *FileContentResult, error) {
	item, err := s.GetOutputVersion(ctx, actor, outputID, version)
	if err != nil {
		return nil, nil, err
	}
	if item.FileID != "" {
		content, openErr := s.openOutputAttachment(ctx, actor, item.FileID)
		return item, content, openErr
	}
	_, preview, previewErr := s.BuildOutputPreview(ctx, actor, outputID, version)
	if previewErr != nil {
		return nil, nil, previewErr
	}
	body := preview.Content
	if preview.Type == valueTableF71860DA {
		body = encodeOutputCSV(preview.Rows)
	}
	extension, contentType := ".txt", "text/plain; charset=utf-8"
	if preview.Type == valueMarkdown6D2DD3CA {
		extension, contentType = ".md", "text/markdown; charset=utf-8"
	}
	if preview.Type == valueTableF71860DA {
		extension, contentType = ".csv", "text/csv; charset=utf-8"
	}
	data := []byte(body)
	return item, &FileContentResult{FileName: safeOutputFileName(item.Title) + extension, Reader: io.NopCloser(bytes.NewReader(data)), ContentType: contentType, SizeBytes: int64(len(data))}, nil
}

func (s *Engine) openOutputAttachment(ctx context.Context, actor model.ActorRef, fileID string) (*FileContentResult, error) {
	if s.attachments == nil {
		return nil, ErrHostProjectionUnavailable
	}
	content, err := s.attachments.OpenAttachment(ctx, OpenAttachmentRequest{Actor: actor, Ref: model.ResourceRef{Kind: valueFileBE372696, ID: strings.TrimSpace(fileID)}})
	if err != nil {
		return nil, err
	}
	return &FileContentResult{FileName: strings.TrimSpace(fileID), Reader: io.NopCloser(bytes.NewReader(content.Data)), ContentType: content.MediaType, SizeBytes: int64(len(content.Data))}, nil
}

func (s *Engine) CreateEvidence(ctx context.Context, input CreateEvidenceInput) (*model.Evidence, error) {
	switch strings.TrimSpace(input.SourceKind) {
	case valueOutput6DD2E13C:
		return s.createOutputEvidence(ctx, input)
	case valueProjectionSource:
		return s.createProjectionEvidence(ctx, input)
	default:
		return nil, ErrEvidenceSelectionInvalid
	}
}

func (s *Engine) createOutputEvidence(ctx context.Context, input CreateEvidenceInput) (*model.Evidence, error) {
	item, preview, err := s.BuildOutputPreview(ctx, input.Actor, input.OutputID, input.Version)
	if err != nil {
		return nil, err
	}
	if item.Status != model.OutputPublished || item.SourceRunStatus != model.RunStatusCompleted {
		return nil, ErrEvidenceSelectionInvalid
	}
	kind := strings.TrimSpace(input.Kind)
	excerpt, selector, valid := evidenceExcerpt(input, preview, kind)
	if !valid {
		return nil, ErrEvidenceSelectionInvalid
	}
	if strings.TrimSpace(excerpt) == "" || len([]rune(excerpt)) > evidenceMaxRunes {
		return nil, ErrEvidenceSelectionInvalid
	}
	selector["outputID"], selector["version"] = item.OutputID, item.Version
	selectorJSON, marshalErr := json.Marshal(selector)
	if marshalErr != nil {
		return nil, ErrEvidenceSelectionInvalid
	}
	sum := sha256.Sum256([]byte(excerpt))
	evidence := &model.Evidence{EvidenceID: "evidence_" + strings.ReplaceAll(uuid.NewString(), "-", ""), SourceKind: valueOutput6DD2E13C, SourceID: item.OutputID, Actor: input.Actor, Projection: item.Projection, Kind: kind, SelectorJSON: string(selectorJSON), Title: strings.TrimSpace(input.Title), Excerpt: excerpt, ContentHash: hex.EncodeToString(sum[:]), SourceContentHash: outputEvidenceSourceHash(item.OutputRef, preview)}
	if evidence.Title == "" {
		evidence.Title = item.Title
	}
	if err = s.persistEvidence(ctx, evidence); err != nil {
		return nil, err
	}
	return evidence, nil
}

func (s *Engine) createProjectionEvidence(ctx context.Context, input CreateEvidenceInput) (*model.Evidence, error) {
	if s.projectionContent == nil || !validProjectionEvidenceInput(input) {
		return nil, ErrEvidenceSelectionInvalid
	}
	content, err := s.projectionContent.ResolveProjectionContent(ctx, ResolveProjectionContentRequest{Actor: input.Actor, Thread: input.Thread, Projection: input.Projection})
	if err != nil {
		return nil, err
	}
	evidence, err := buildProjectionEvidence(input, content)
	if err != nil {
		return nil, err
	}
	if err = s.persistEvidence(ctx, evidence); err != nil {
		return nil, err
	}
	return evidence, nil
}

func validProjectionEvidenceInput(input CreateEvidenceInput) bool {
	return validActorRef(input.Actor) && strings.TrimSpace(input.Thread.Kind) != "" && strings.TrimSpace(input.Thread.ID) != "" && strings.TrimSpace(input.Projection.Kind) != "" && strings.TrimSpace(input.Projection.ID) != ""
}

func buildProjectionEvidence(input CreateEvidenceInput, content ProjectionContent) (*model.Evidence, error) {
	excerpt, selector, valid := projectionEvidenceExcerpt(input, content)
	if !valid || strings.TrimSpace(excerpt) == "" {
		return nil, ErrEvidenceSelectionInvalid
	}
	if len([]rune(excerpt)) > evidenceMaxRunes {
		return nil, ErrEvidenceSelectionTooLarge
	}
	selector["thread"] = input.Thread
	selector["projection"] = input.Projection
	selectorJSON, err := json.Marshal(selector)
	if err != nil {
		return nil, ErrEvidenceSelectionInvalid
	}
	sum := sha256.Sum256([]byte(excerpt))
	evidence := &model.Evidence{
		EvidenceID: "evidence_" + strings.ReplaceAll(uuid.NewString(), "-", ""), SourceKind: valueProjectionSource,
		SourceID: input.Projection.ID, Actor: input.Actor, Projection: input.Projection, Kind: strings.TrimSpace(input.Kind),
		SelectorJSON: string(selectorJSON), Title: strings.TrimSpace(input.Title), Excerpt: excerpt,
		ContentHash: hex.EncodeToString(sum[:]), SourceContentHash: strings.TrimSpace(content.ContentHash),
	}
	if evidence.Title == "" {
		evidence.Title = strings.TrimSpace(content.Title)
	}
	if evidence.Title == "" {
		evidence.Title = "Referenced message"
	}
	return evidence, nil
}

func (s *Engine) persistEvidence(ctx context.Context, evidence *model.Evidence) error {
	if s.repo == nil {
		return ErrInvalidInput
	}
	return s.repo.CreateEvidence(ctx, evidence)
}

func projectionEvidenceExcerpt(input CreateEvidenceInput, content ProjectionContent) (string, map[string]interface{}, bool) {
	kind := strings.TrimSpace(input.Kind)
	selector := map[string]interface{}{valueKind55B00946: kind}
	if strings.TrimSpace(content.ContentHash) == "" || !projectionEvidenceContentType(content.ContentType) {
		return "", selector, false
	}
	switch kind {
	case valueFull:
		return content.Content, selector, true
	case valueTextRange:
		runes := []rune(content.Content)
		if input.Start < 0 || input.End <= input.Start || input.End > len(runes) {
			return "", selector, false
		}
		selector["start"], selector["end"] = input.Start, input.End
		return string(runes[input.Start:input.End]), selector, true
	default:
		return "", selector, false
	}
}

func projectionEvidenceContentType(value string) bool {
	value = strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
	return value == "" || value == valueText6CED98CE || value == valueMarkdown6D2DD3CA || value == "text/plain" || value == "text/markdown"
}

func outputEvidenceSourceHash(output model.OutputRef, preview *OutputPreview) string {
	payload := output.OutputID + "\x00" + fmt.Sprint(output.Version) + "\x00" + preview.Type + "\x00" + preview.Content
	if preview.Type == valueTableF71860DA {
		payload += "\x00" + encodeOutputCSV(preview.Rows)
	}
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func evidenceExcerpt(input CreateEvidenceInput, preview *OutputPreview, kind string) (string, map[string]interface{}, bool) {
	selector := map[string]interface{}{valueKind55B00946: kind}
	switch kind {
	case valueTextRange:
		return textEvidenceExcerpt(input, preview, selector)
	case "table_range":
		return tableEvidenceExcerpt(input, preview, selector)
	default:
		return "", selector, false
	}
}

func textEvidenceExcerpt(input CreateEvidenceInput, preview *OutputPreview, selector map[string]interface{}) (string, map[string]interface{}, bool) {
	unsupported := map[string]bool{valueImage8FE9EE53: true, "html": true, valueFileBE372696: true, valueTableF71860DA: true}
	if unsupported[preview.Type] {
		return "", selector, false
	}
	runes := []rune(preview.Content)
	if input.Start < 0 || input.End <= input.Start || input.End > len(runes) {
		return "", selector, false
	}
	selector["start"], selector["end"] = input.Start, input.End
	return string(runes[input.Start:input.End]), selector, true
}

func tableEvidenceExcerpt(input CreateEvidenceInput, preview *OutputPreview, selector map[string]interface{}) (string, map[string]interface{}, bool) {
	if preview.Type != valueTableF71860DA || input.RowStart < 0 || input.RowEnd <= input.RowStart || input.RowEnd > len(preview.Rows) || input.ColumnStart < 0 || input.ColumnEnd <= input.ColumnStart {
		return "", selector, false
	}
	selected, cells, valid := selectEvidenceCells(preview.Rows[input.RowStart:input.RowEnd], input.ColumnStart, input.ColumnEnd)
	if !valid || cells > 100 {
		return "", selector, false
	}
	selector["rowStart"], selector["rowEnd"] = input.RowStart, input.RowEnd
	selector["columnStart"], selector["columnEnd"] = input.ColumnStart, input.ColumnEnd
	return encodeOutputCSV(selected), selector, true
}

func selectEvidenceCells(rows [][]string, start, requestedEnd int) ([][]string, int, bool) {
	selected := make([][]string, 0, len(rows))
	cells := 0
	for _, row := range rows {
		if start >= len(row) {
			return nil, 0, false
		}
		end := min(requestedEnd, len(row))
		part := append([]string(nil), row[start:end]...)
		cells += len(part)
		selected = append(selected, part)
	}
	return selected, cells, true
}

func readOutputPreview(reader io.Reader) ([]byte, bool, error) {
	limited := io.LimitReader(reader, outputPreviewMaxBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if len(data) > outputPreviewMaxBytes {
		cut := outputPreviewMaxBytes
		for cut > 0 && !utf8.Valid(data[:cut]) {
			cut--
		}
		return data[:cut], true, nil
	}
	return data, false, nil
}
func truncateOutputPreview(value string) (string, bool) {
	if len(value) <= outputPreviewMaxBytes {
		return value, false
	}
	cut := 0
	for index, valueRune := range value {
		next := index + utf8.RuneLen(valueRune)
		if next > outputPreviewMaxBytes {
			break
		}
		cut = next
	}
	return value[:cut], true
}
func outputPreviewTextMIME(value string) bool {
	value = strings.ToLower(value)
	return strings.HasPrefix(value, "text/") || strings.Contains(value, "json") || strings.Contains(value, "xml") || strings.Contains(value, "csv") || strings.Contains(value, "javascript")
}
func outputTextPreview(content, mimeType, kind, fileID string, truncated bool) *OutputPreview {
	lower := strings.ToLower(firstNonEmptyString(mimeType, kind))
	typ := valueMarkdown6D2DD3CA
	language := ""
	switch {
	case strings.Contains(lower, "html"):
		typ = "html"
	case strings.Contains(lower, "csv") || strings.Contains(lower, valueTableF71860DA):
		rows, _ := csv.NewReader(strings.NewReader(content)).ReadAll()
		if len(rows) > 100 {
			rows = rows[:100]
			truncated = true
		}
		for i := range rows {
			if len(rows[i]) > 50 {
				rows[i] = rows[i][:50]
				truncated = true
			}
		}
		return &OutputPreview{Type: valueTableF71860DA, Rows: rows, FileID: fileID, MIMEType: mimeType, Truncated: truncated}
	case strings.Contains(lower, "json"):
		typ, language = valueCode57466930, "json"
	case strings.Contains(lower, "javascript"):
		typ, language = valueCode57466930, "javascript"
	case strings.Contains(lower, "plain"):
		typ = valueText6CED98CE
	}
	return &OutputPreview{Type: typ, Content: content, Language: language, FileID: fileID, MIMEType: mimeType, Truncated: truncated}
}
func outputPreviewRows(value interface{}) [][]string {
	raw, ok := value.([]interface{})
	if !ok {
		return nil
	}
	rows := make([][]string, 0, len(raw))
	for _, item := range raw {
		cells, ok := item.([]interface{})
		if !ok {
			continue
		}
		row := make([]string, 0, len(cells))
		for _, cell := range cells {
			row = append(row, fmt.Sprint(cell))
		}
		if len(row) > 50 {
			row = row[:50]
		}
		rows = append(rows, row)
		if len(rows) >= 100 {
			break
		}
	}
	return rows
}
func encodeOutputCSV(rows [][]string) string {
	var b strings.Builder
	w := csv.NewWriter(&b)
	_ = w.WriteAll(rows)
	w.Flush()
	return b.String()
}
func safeOutputFileName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return valueOutput6DD2E13C
	}
	replacer := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "-", "?", "-", "\"", "-", "<", "-", ">", "-", "|", "-")
	r := []rune(replacer.Replace(value))
	if len(r) > 80 {
		r = r[:80]
	}
	return string(r)
}
