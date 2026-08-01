package agentruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

const (
	maxToolContractBytes = 256 * 1024
	schemaKeywordAnyOf   = "anyOf"
)

var (
	ErrToolSchemaInvalid    = errors.New("tool schema is invalid")
	ErrToolArgumentsInvalid = errors.New("tool arguments are invalid")
	ErrToolOutputInvalid    = errors.New("tool output is invalid")
	errExternalSchemaRef    = errors.New("external schema references are not allowed")
)

// ToolContractError reports only the failed contract boundary and instance
// path. Argument and output values are intentionally excluded from the error.
type ToolContractError struct {
	Kind   string
	Path   string
	Reason string
}

// missingDiscriminatedToolContractFields resolves an anyOf/oneOf branch from
// an exact discriminator such as operation.type before reporting nested
// required-field failures. Without this preflight, generic JSON Schema error
// trees can select a deeper error from an unrelated branch and tell an agent
// to remove valid parameters instead of supplying the actually missing field.
func missingDiscriminatedToolContractFields(schema json.RawMessage, value interface{}) (string, []string) {
	var root map[string]interface{}
	if json.Unmarshal(schema, &root) != nil {
		return "", nil
	}
	return findMissingDiscriminatedToolContractFields(root, value, "$")
}

func findMissingDiscriminatedToolContractFields(schema map[string]interface{}, value interface{}, path string) (string, []string) {
	switch typed := value.(type) {
	case map[string]interface{}:
		return findMissingDiscriminatedObjectFields(schema, typed, path)
	case []interface{}:
		return findMissingDiscriminatedArrayFields(schema, typed, path)
	}
	return "", nil
}

func findMissingDiscriminatedObjectFields(schema map[string]interface{}, value map[string]interface{}, path string) (string, []string) {
	candidates := discriminatedToolContractCandidates(schema, value)
	if missing := firstMissingToolContractFields(candidates, value); len(missing) > 0 {
		return path, missing
	}
	return findMissingDiscriminatedObjectChild(mergedToolContractProperties(candidates), value, path)
}

func discriminatedToolContractCandidates(schema map[string]interface{}, value map[string]interface{}) []map[string]interface{} {
	candidates := []map[string]interface{}{schema}
	if branch := matchingDiscriminatedToolContractBranch(schema, value); branch != nil {
		candidates = append(candidates, branch)
	}
	return candidates
}

func firstMissingToolContractFields(candidates []map[string]interface{}, value map[string]interface{}) []string {
	for _, candidate := range candidates {
		if missing := missingToolContractFields(candidate, value); len(missing) > 0 {
			return missing
		}
	}
	return nil
}

func mergedToolContractProperties(candidates []map[string]interface{}) map[string][]map[string]interface{} {
	properties := make(map[string][]map[string]interface{})
	for _, candidate := range candidates {
		for name, property := range directToolContractProperties(candidate) {
			properties[name] = append(properties[name], property)
		}
	}
	return properties
}

func findMissingDiscriminatedObjectChild(properties map[string][]map[string]interface{}, value map[string]interface{}, path string) (string, []string) {
	for _, name := range sortedToolContractObjectNames(value) {
		propertySchemas := properties[name]
		if len(propertySchemas) == 0 {
			continue
		}
		childPath, missing := findMissingDiscriminatedToolContractFields(
			combinedToolContractSchema(propertySchemas),
			value[name],
			path+"/"+escapeToolContractJSONPointer(name),
		)
		if len(missing) > 0 {
			return childPath, missing
		}
	}
	return "", nil
}

func sortedToolContractObjectNames(value map[string]interface{}) []string {
	names := make([]string, 0, len(value))
	for name := range value {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func findMissingDiscriminatedArrayFields(schema map[string]interface{}, value []interface{}, path string) (string, []string) {
	items := directToolContractItems(schema)
	if len(items) == 0 {
		return "", nil
	}
	itemSchema := combinedToolContractSchema(items)
	for index, item := range value {
		childPath, missing := findMissingDiscriminatedToolContractFields(
			itemSchema,
			item,
			fmt.Sprintf("%s/%d", path, index),
		)
		if len(missing) > 0 {
			return childPath, missing
		}
	}
	return "", nil
}

func matchingDiscriminatedToolContractBranch(schema map[string]interface{}, value map[string]interface{}) map[string]interface{} {
	for _, keyword := range []string{"oneOf", schemaKeywordAnyOf} {
		if branch := matchingToolContractBranchSet(toolContractBranches(schema, keyword), value); branch != nil {
			return branch
		}
	}
	return nil
}

func toolContractBranches(schema map[string]interface{}, keyword string) []interface{} {
	branches, _ := schema[keyword].([]interface{})
	return branches
}

func matchingToolContractBranchSet(branches []interface{}, value map[string]interface{}) map[string]interface{} {
	if len(branches) == 0 {
		return nil
	}
	if branch := uniquelyMatchingToolContractBranch(branches, value, "type"); branch != nil {
		return branch
	}
	return highestScoringToolContractBranch(branches, value)
}

func highestScoringToolContractBranch(branches []interface{}, value map[string]interface{}) map[string]interface{} {
	bestScore := 0
	var best map[string]interface{}
	ambiguous := false
	for _, raw := range branches {
		branch, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		score := toolContractBranchDiscriminatorScore(branch, value)
		if score > bestScore {
			bestScore, best, ambiguous = score, branch, false
			continue
		}
		if score > 0 && score == bestScore {
			ambiguous = true
		}
	}
	if bestScore == 0 || ambiguous {
		return nil
	}
	return best
}

func uniquelyMatchingToolContractBranch(branches []interface{}, value map[string]interface{}, propertyName string) map[string]interface{} {
	item, exists := value[propertyName]
	if !exists {
		return nil
	}
	var matched map[string]interface{}
	for _, raw := range branches {
		branch, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		property := directToolContractProperties(branch)[propertyName]
		if property == nil || !toolContractSchemaAcceptsExactValue(property, item) {
			continue
		}
		if matched != nil {
			return nil
		}
		matched = branch
	}
	return matched
}

func toolContractBranchDiscriminatorScore(branch map[string]interface{}, value map[string]interface{}) int {
	score := 0
	for name, property := range directToolContractProperties(branch) {
		item, exists := value[name]
		if !exists || !toolContractSchemaHasExactValues(property) {
			continue
		}
		if toolContractSchemaAcceptsExactValue(property, item) {
			score++
		}
	}
	return score
}

func toolContractSchemaHasExactValues(schema map[string]interface{}) bool {
	if _, exists := schema["const"]; exists {
		return true
	}
	values, _ := schema["enum"].([]interface{})
	return len(values) > 0
}

func toolContractSchemaAcceptsExactValue(schema map[string]interface{}, value interface{}) bool {
	if expected, exists := schema["const"]; exists {
		return fmt.Sprint(expected) == fmt.Sprint(value)
	}
	values, _ := schema["enum"].([]interface{})
	for _, expected := range values {
		if fmt.Sprint(expected) == fmt.Sprint(value) {
			return true
		}
	}
	return false
}

func missingToolContractFields(schema map[string]interface{}, value map[string]interface{}) []string {
	required, _ := schema["required"].([]interface{})
	missing := make([]string, 0, len(required))
	for _, raw := range required {
		name, ok := raw.(string)
		if !ok {
			continue
		}
		if _, exists := value[name]; !exists {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}

func directToolContractProperties(schema map[string]interface{}) map[string]map[string]interface{} {
	raw, _ := schema["properties"].(map[string]interface{})
	result := make(map[string]map[string]interface{}, len(raw))
	for name, property := range raw {
		if typed, ok := property.(map[string]interface{}); ok {
			result[name] = typed
		}
	}
	return result
}

func directToolContractItems(schema map[string]interface{}) []map[string]interface{} {
	items, ok := schema["items"].(map[string]interface{})
	if !ok {
		return nil
	}
	return []map[string]interface{}{items}
}

func escapeToolContractJSONPointer(value string) string {
	value = strings.ReplaceAll(value, "~", "~0")
	return strings.ReplaceAll(value, "/", "~1")
}

func validateToolContractSchemas(inputSchema, outputSchema json.RawMessage) error {
	if _, err := compileToolContractSchema(inputSchema); err != nil {
		return err
	}
	if optionalToolContractSchemaAbsent(outputSchema) {
		return nil
	}
	_, err := compileToolContractSchema(outputSchema)
	return err
}

func optionalToolContractSchemaAbsent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

func (e *ToolContractError) Error() string {
	if e == nil {
		return "tool contract validation failed"
	}
	path := strings.TrimSpace(e.Path)
	if path == "" {
		path = "$"
	}
	reason := strings.TrimSpace(e.Reason)
	if reason == "" {
		reason = "contract validation failed"
	}
	return fmt.Sprintf("tool %s validation failed at %s: %s", e.Kind, path, reason)
}

func (e *ToolContractError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.Kind {
	case "schema":
		return ErrToolSchemaInvalid
	case "output":
		return ErrToolOutputInvalid
	default:
		return ErrToolArgumentsInvalid
	}
}

func (e *ToolContractError) DeterministicToolFailure() bool { return e != nil }

func normalizeToolArgumentsAgainstSchema(raw string, schema json.RawMessage) (string, error) {
	value, err := decodeToolContractJSON(raw, true, "arguments")
	if err != nil {
		return "", err
	}
	if _, ok := value.(map[string]interface{}); !ok {
		return "", newToolContractError("arguments", "$", "root value must be an object")
	}
	return validateAndCanonicalizeToolContract(schema, value, "arguments")
}

func normalizeToolOutputAgainstSchema(raw string, schema json.RawMessage, providerKind string) (string, error) {
	if optionalToolContractSchemaAbsent(schema) {
		if !utf8.ValidString(raw) {
			return "", newToolContractError("output", "$", "value is not valid UTF-8")
		}
		return raw, nil
	}
	value, err := decodeToolContractJSON(raw, false, "output")
	if err != nil {
		return "", err
	}
	if strings.EqualFold(strings.TrimSpace(providerKind), valueMcpCE1A7808) {
		envelope, ok := value.(map[string]interface{})
		if !ok {
			return "", newToolContractError("output", "$", "MCP result must be an object")
		}
		structured, ok := envelope["structuredContent"]
		if !ok {
			return "", newToolContractError("output", "$/structuredContent", "MCP result is missing structuredContent required by outputSchema")
		}
		if _, err = validateAndCanonicalizeToolContract(schema, structured, "output"); err != nil {
			return "", err
		}
		encoded, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return "", newToolContractError("output", "$", "value cannot be canonicalized")
		}
		return string(encoded), nil
	}
	return validateAndCanonicalizeToolContract(schema, value, "output")
}

func validateAndCanonicalizeToolContract(schema json.RawMessage, value interface{}, kind string) (string, error) {
	compiled, err := compileToolContractSchema(schema)
	if err != nil {
		return "", err
	}
	value = canonicalizeToolContractPropertyNames(schema, value)
	if missing := missingRootToolContractFields(schema, value); len(missing) > 0 {
		return "", newToolContractError(kind, "$", "required parameters are missing: "+strings.Join(missing, ", "))
	}
	if unexpected := unexpectedRootToolContractFields(schema, value); len(unexpected) > 0 {
		return "", newToolContractError(kind, "$/"+unexpected[0], "unexpected parameters are not allowed: "+strings.Join(unexpected, ", "))
	}
	if path, missing := missingDiscriminatedToolContractFields(schema, value); len(missing) > 0 {
		return "", newToolContractError(kind, path, "required parameters are missing: "+strings.Join(missing, ", "))
	}
	if err = compiled.Validate(value); err != nil {
		return "", toolContractValidationError(kind, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", newToolContractError(kind, "$", "value cannot be canonicalized")
	}
	return string(encoded), nil
}

// canonicalizeToolContractPropertyNames repairs provider casing drift without
// weakening the frozen contract. Unknown keys remain untouched and are still
// rejected by additionalProperties:false; only a unique case-insensitive match
// to a declared property is rewritten before validation.
func canonicalizeToolContractPropertyNames(schema json.RawMessage, value interface{}) interface{} {
	var node map[string]interface{}
	if json.Unmarshal(schema, &node) != nil {
		return value
	}
	return canonicalizeToolContractValue(node, value)
}

func canonicalizeToolContractValue(schema map[string]interface{}, value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return canonicalizeToolContractObject(schema, typed)
	case []interface{}:
		return canonicalizeToolContractArray(schema, typed)
	default:
		return value
	}
}

func canonicalizeToolContractObject(schema map[string]interface{}, value map[string]interface{}) map[string]interface{} {
	properties := collectToolContractProperties(schema)
	canonicalByFold := canonicalToolContractPropertyNames(properties)
	result := make(map[string]interface{}, len(value))
	for name, item := range value {
		canonical := canonicalToolContractPropertyName(name, properties, canonicalByFold)
		if _, collision := result[canonical]; collision && canonical != name {
			result[name] = item
			continue
		}
		result[canonical] = canonicalizeToolContractValue(combinedToolContractSchema(properties[canonical]), item)
	}
	return result
}

func canonicalToolContractPropertyNames(properties map[string][]map[string]interface{}) map[string]string {
	canonicalByFold := make(map[string]string, len(properties))
	for name := range properties {
		folded := strings.ToLower(name)
		if existing, ok := canonicalByFold[folded]; ok && existing != name {
			canonicalByFold[folded] = ""
			continue
		}
		canonicalByFold[folded] = name
	}
	return canonicalByFold
}

func canonicalToolContractPropertyName(
	name string,
	properties map[string][]map[string]interface{},
	canonicalByFold map[string]string,
) string {
	if _, exact := properties[name]; exact {
		return name
	}
	if matched := canonicalByFold[strings.ToLower(name)]; matched != "" {
		return matched
	}
	return name
}

func canonicalizeToolContractArray(schema map[string]interface{}, value []interface{}) []interface{} {
	itemSchema := combinedToolContractSchema(collectToolContractItems(schema))
	for index := range value {
		value[index] = canonicalizeToolContractValue(itemSchema, value[index])
	}
	return value
}

func collectToolContractProperties(schema map[string]interface{}) map[string][]map[string]interface{} {
	result := make(map[string][]map[string]interface{})
	for _, candidate := range toolContractSchemaCandidates(schema) {
		raw, _ := candidate["properties"].(map[string]interface{})
		for name, property := range raw {
			if propertySchema, ok := property.(map[string]interface{}); ok {
				result[name] = append(result[name], propertySchema)
			}
		}
	}
	return result
}

func collectToolContractItems(schema map[string]interface{}) []map[string]interface{} {
	var result []map[string]interface{}
	for _, candidate := range toolContractSchemaCandidates(schema) {
		if items, ok := candidate["items"].(map[string]interface{}); ok {
			result = append(result, items)
		}
	}
	return result
}

func toolContractSchemaCandidates(schema map[string]interface{}) []map[string]interface{} {
	result := []map[string]interface{}{schema}
	for _, keyword := range []string{"oneOf", schemaKeywordAnyOf, "allOf"} {
		branches, _ := schema[keyword].([]interface{})
		for _, branch := range branches {
			if candidate, ok := branch.(map[string]interface{}); ok {
				result = append(result, toolContractSchemaCandidates(candidate)...)
			}
		}
	}
	return result
}

func combinedToolContractSchema(candidates []map[string]interface{}) map[string]interface{} {
	switch len(candidates) {
	case 0:
		return map[string]interface{}{}
	case 1:
		return candidates[0]
	default:
		branches := make([]interface{}, len(candidates))
		for index := range candidates {
			branches[index] = candidates[index]
		}
		return map[string]interface{}{schemaKeywordAnyOf: branches}
	}
}

func unexpectedRootToolContractFields(schema json.RawMessage, value interface{}) []string {
	object, ok := value.(map[string]interface{})
	if !ok {
		return nil
	}
	var contract struct {
		AdditionalProperties json.RawMessage            `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(schema, &contract) != nil || !bytes.Equal(bytes.TrimSpace(contract.AdditionalProperties), []byte("false")) {
		return nil
	}
	unexpected := make([]string, 0)
	for name := range object {
		if _, exists := contract.Properties[name]; !exists {
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(unexpected)
	return unexpected
}

func missingRootToolContractFields(schema json.RawMessage, value interface{}) []string {
	object, ok := value.(map[string]interface{})
	if !ok {
		return nil
	}
	var contract struct {
		Required []string `json:"required"`
	}
	if json.Unmarshal(schema, &contract) != nil || len(contract.Required) == 0 {
		return nil
	}
	missing := make([]string, 0, len(contract.Required))
	for _, name := range contract.Required {
		if _, exists := object[name]; !exists {
			missing = append(missing, name)
		}
	}
	return missing
}

func compileToolContractSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, newToolContractError("schema", "$", "schema is required")
	}
	if len(trimmed) > maxToolContractBytes {
		return nil, newToolContractError("schema", "$", "schema exceeds the contract size limit")
	}
	const schemaURL = "https://gaoge.local/runtime/tool-contract.json"
	compiler := jsonschema.NewCompiler()
	compiler.LoadURL = func(string) (io.ReadCloser, error) {
		return nil, errExternalSchemaRef
	}
	if err := compiler.AddResource(schemaURL, bytes.NewReader(trimmed)); err != nil {
		return nil, newToolContractError("schema", "$", "schema must be valid JSON Schema")
	}
	compiled, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, newToolContractError("schema", "$", "schema must be valid JSON Schema")
	}
	return compiled, nil
}

func decodeToolContractJSON(raw string, emptyObject bool, kind string) (interface{}, error) {
	value := strings.TrimSpace(raw)
	if value == "" && emptyObject {
		value = "{}"
	}
	if value == "" {
		return nil, newToolContractError(kind, "$", "value must contain one JSON document")
	}
	if len(value) > maxToolContractBytes {
		return nil, newToolContractError(kind, "$", "value exceeds the contract size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	var decoded interface{}
	if err := decoder.Decode(&decoded); err != nil {
		return nil, newToolContractError(kind, "$", "value must be valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, newToolContractError(kind, "$", "value must contain one JSON document")
	}
	return decoded, nil
}

func toolContractValidationError(kind string, err error) error {
	path := "$"
	var validation *jsonschema.ValidationError
	reason := "value does not satisfy the frozen schema"
	if errors.As(err, &validation) {
		specific := mostSpecificToolContractValidation(validation)
		if location := strings.TrimSpace(specific.InstanceLocation); location != "" {
			path = "$" + location
		}
		reason = toolContractValidationReason(specific.KeywordLocation)
	}
	return newToolContractError(kind, path, reason)
}

func mostSpecificToolContractValidation(root *jsonschema.ValidationError) *jsonschema.ValidationError {
	best := root
	for _, cause := range root.Causes {
		candidate := mostSpecificToolContractValidation(cause)
		candidateDepth := strings.Count(candidate.InstanceLocation, "/")
		bestDepth := strings.Count(best.InstanceLocation, "/")
		if candidateDepth > bestDepth ||
			(candidateDepth == bestDepth && len(candidate.Causes) == 0) {
			best = candidate
		}
	}
	return best
}

func toolContractValidationReason(keywordLocation string) string {
	switch {
	case strings.HasSuffix(keywordLocation, "/required"):
		return "required parameters are missing"
	case strings.HasSuffix(keywordLocation, "/additionalProperties"):
		return "unexpected parameters are not allowed"
	case strings.HasSuffix(keywordLocation, "/minProperties"):
		return "object must include at least one property"
	case strings.HasSuffix(keywordLocation, "/minItems"):
		return "array does not contain enough items"
	case strings.HasSuffix(keywordLocation, "/minLength"):
		return "string is shorter than allowed"
	case strings.HasSuffix(keywordLocation, "/type"):
		return "value has the wrong type"
	case strings.HasSuffix(keywordLocation, "/enum"):
		return "value is not one of the allowed values"
	case strings.HasSuffix(keywordLocation, "/oneOf"),
		strings.HasSuffix(keywordLocation, "/anyOf"):
		return "value does not match an allowed schema"
	default:
		return "value does not satisfy the frozen schema"
	}
}

func newToolContractError(kind, path, reason string) error {
	return &ToolContractError{Kind: kind, Path: path, Reason: reason}
}
