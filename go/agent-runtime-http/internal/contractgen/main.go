// Command contractgen emits the edge client's types from the public OpenAPI.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func main() {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../../.."))
	raw, err := os.ReadFile(filepath.Join(root, "contracts/agent-runtime/v1/openapi.yaml"))
	must(err)
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	must(yaml.Unmarshal(raw, &doc))
	var out strings.Builder
	out.WriteString("// Code generated from contracts/agent-runtime/v1/openapi.yaml; DO NOT EDIT.\n")
	// Harness contracts are frozen and deliberately remain outside this cut.
	for _, name := range []string{"ActorRef", "ThreadRef", "StartAgentRunRequest", "CancelRunRequest", "Run", "Event", "RunFeedEvent", "Checkpoint", "Result", "RunSnapshot", "PendingTool", "RunEventPage"} {
		fmt.Fprintf(&out, "export type %s = %s;\n", typeName(name), ts(doc.Components.Schemas[name], doc.Components.Schemas))
	}
	out.WriteString("export type RuntimeKind = RunDTO[\"kind\"];\nexport type RunStatus = RunDTO[\"status\"];\n")
	path := filepath.Join(root, "ts/agent-runtime-client/src/types.gen.ts")
	if len(os.Args) > 1 {
		if len(os.Args) != 2 || os.Args[1] != "-check" {
			panic("usage: contractgen [-check]")
		}
		current, err := os.ReadFile(path)
		must(err)
		if !bytes.Equal(current, []byte(out.String())) {
			panic("edge types are stale: go run ./go/agent-runtime-http/internal/contractgen")
		}
		return
	}
	must(os.WriteFile(path, []byte(out.String()), 0644))
}

func typeName(name string) string {
	if strings.HasSuffix(name, "Request") {
		return name
	}
	return name + "DTO"
}

func ts(schema map[string]any, schemas map[string]map[string]any) string {
	if schema == nil {
		panic("missing edge schema")
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf", "not"} {
		if _, found := schema[key]; found {
			panic("unsupported edge schema keyword: " + key)
		}
	}
	var result string
	if ref, ok := schema["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		if schemas[name] == nil {
			panic("unknown schema reference: " + ref)
		}
		if name == "ThreadRequest" || name == "TextInputRequest" {
			return ts(schemas[name], schemas)
		}
		return typeName(name)
	}
	if values, ok := schema["enum"].([]any); ok {
		parts := make([]string, len(values))
		for i, value := range values {
			raw, err := json.Marshal(value)
			must(err)
			parts[i] = string(raw)
		}
		result = strings.Join(parts, " | ")
	} else {
		switch schema["type"] {
		case "string", "boolean":
			result = schema["type"].(string)
		case "integer", "number":
			result = "number"
		case "array":
			result = "Array<" + ts(schema["items"].(map[string]any), schemas) + ">"
		case "object":
			required := map[string]bool{}
			if fields, ok := schema["required"].([]any); ok {
				for _, field := range fields {
					required[field.(string)] = true
				}
			}
			properties, _ := schema["properties"].(map[string]any)
			keys := make([]string, 0, len(properties))
			for key := range properties {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, key := range keys {
				optional := "?"
				if required[key] {
					optional = ""
				}
				parts = append(parts, "  "+key+optional+": "+strings.ReplaceAll(ts(properties[key].(map[string]any), schemas), "\n", "\n  ")+";")
			}
			result = "{\n" + strings.Join(parts, "\n") + "\n}"
		case nil:
			result = "unknown"
		default:
			panic(fmt.Sprintf("unsupported edge schema: %v", schema))
		}
	}
	if schema["nullable"] == true {
		result += " | null"
	}
	return result
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
