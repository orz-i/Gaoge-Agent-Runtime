package http

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPIExposesOnlyTargetRuntimeResources(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "agent-runtime", "v1", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read OpenAPI: %v", err)
	}
	var document struct {
		Paths map[string]map[string]interface{} `yaml:"paths"`
	}
	if err = yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse OpenAPI: %v", err)
	}
	operations := make([]string, 0)
	for path, methods := range document.Paths {
		for method := range methods {
			operations = append(operations, method+" "+path)
		}
	}
	sort.Strings(operations)
	expected := []string{
		"get /runs/{runID}",
		"get /runs/{runID}/workbench",
		"post /plan-runs",
		"post /plan-runs/{runID}/approval",
		"post /runs/{runID}/cancel",
		"post /team-runs",
		"post /text-runs",
		"post /workflow-runs",
		"post /workflow-runs/{runID}/wait",
	}
	if !reflect.DeepEqual(operations, expected) {
		t.Fatalf("OpenAPI operations = %#v, want %#v", operations, expected)
	}
	if containsBytes(raw, []byte("executionMode")) || containsBytes(raw, []byte("auto, direct, plan")) {
		t.Fatal("OpenAPI revived removed Text execution modes")
	}
}

func containsBytes(value []byte, target []byte) bool {
	return len(target) > 0 && stringContains(string(value), string(target))
}

func stringContains(value string, target string) bool {
	for index := 0; index+len(target) <= len(value); index++ {
		if value[index:index+len(target)] == target {
			return true
		}
	}
	return false
}
