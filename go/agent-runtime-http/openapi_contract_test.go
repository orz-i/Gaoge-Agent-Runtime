package http

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"gopkg.in/yaml.v3"
)

type openAPIMethod struct {
	OperationID string `yaml:"operationId"`
}

type openAPIPaths map[string]map[string]openAPIMethod

func TestOpenAPIExposesTargetRuntimeAndHarnessResources(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "agent-runtime", "v1", "openapi.yaml"))
	if err != nil {
		t.Fatalf("read OpenAPI: %v", err)
	}
	var document struct {
		Paths openAPIPaths `yaml:"paths"`
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
		"get /harness/turns/{turnID}",
		"get /harness/turns/{turnID}/feed",
		"get /runs/{runID}",
		"get /runs/{runID}/feed",
		"get /runs/{runID}/workbench",
		"post /agent-runs",
		"post /harness/turns/{turnID}/approval",
		"post /plan-runs",
		"post /plan-runs/{runID}/approval",
		"post /runs/{runID}/cancel",
		"post /team-runs",
		"post /workflow-runs",
		"post /workflow-runs/{runID}/wait",
	}
	if !reflect.DeepEqual(operations, expected) {
		t.Fatalf("OpenAPI operations = %#v, want %#v", operations, expected)
	}
	assertCapabilityFragments(t, document.Paths)
	for _, marker := range []string{
		"runfeed.cursor_expired",
		"X-Run-Feed-Head",
		"harness.feed_cursor_expired",
		"X-Harness-Feed-Head",
		"HarnessTurnSnapshot",
	} {
		if !containsBytes(raw, []byte(marker)) {
			t.Fatalf("OpenAPI missing formal recovery/Harness marker %q", marker)
		}
	}
	if containsBytes(raw, []byte("executionMode")) || containsBytes(raw, []byte("auto, direct, plan")) {
		t.Fatal("OpenAPI revived removed Agent execution modes")
	}
}

type capabilityFragment struct {
	Capability string `json:"capability"`
	Operations []struct {
		Method      string `json:"method"`
		Path        string `json:"path"`
		OperationID string `json:"operationId"`
	} `json:"operations"`
}

func assertCapabilityFragments(t *testing.T, paths openAPIPaths) {
	t.Helper()
	fragmentFS := os.DirFS(filepath.Join("..", "..", "contracts", "agent-runtime", "v1", "capabilities"))
	fragmentNames := []string{"core.json", "agent.json", "planexecute.json", "workflow.json", "team.json", "harness.json"}
	actual := make([]string, 0)
	seenCapabilities := map[string]struct{}{}
	for _, fragmentName := range fragmentNames {
		fragment := readCapabilityFragment(t, fragmentFS, fragmentName)
		registerCapabilityFragment(t, seenCapabilities, fragment)
		actual = append(actual, validateCapabilityOperations(t, paths, fragment)...)
	}
	sort.Strings(actual)
	if !reflect.DeepEqual(actual, openAPIOperations(paths)) {
		t.Fatalf("capability fragments = %#v, OpenAPI = %#v", actual, openAPIOperations(paths))
	}
}

func readCapabilityFragment(t *testing.T, fragmentFS fs.FS, name string) capabilityFragment {
	t.Helper()
	raw, err := fs.ReadFile(fragmentFS, name)
	if err != nil {
		t.Fatalf("read capability fragment %s: %v", name, err)
	}
	var fragment capabilityFragment
	if err = json.Unmarshal(raw, &fragment); err != nil || fragment.Capability == "" || len(fragment.Operations) == 0 {
		t.Fatalf("invalid capability fragment %s: %v", name, err)
	}
	return fragment
}

func registerCapabilityFragment(t *testing.T, seen map[string]struct{}, fragment capabilityFragment) {
	t.Helper()
	if _, duplicate := seen[fragment.Capability]; duplicate {
		t.Fatalf("duplicate capability fragment %s", fragment.Capability)
	}
	seen[fragment.Capability] = struct{}{}
}

func validateCapabilityOperations(t *testing.T, paths openAPIPaths, fragment capabilityFragment) []string {
	t.Helper()
	result := make([]string, 0, len(fragment.Operations))
	for _, operation := range fragment.Operations {
		pathMethods, ok := paths[operation.Path]
		if !ok || pathMethods[operation.Method].OperationID != operation.OperationID {
			t.Fatalf("fragment operation missing from OpenAPI: %#v", operation)
		}
		result = append(result, operation.Method+" "+operation.Path)
	}
	return result
}

func openAPIOperations(paths openAPIPaths) []string {
	result := make([]string, 0)
	for path, methods := range paths {
		for method := range methods {
			result = append(result, method+" "+path)
		}
	}
	sort.Strings(result)
	return result
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
