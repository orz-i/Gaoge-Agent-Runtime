package http_test

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestHTTPDoesNotReviveDeletedCompositeFeatures(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		for _, imported := range file.Imports {
			module, unquoteErr := strconv.Unquote(imported.Path.Value)
			if unquoteErr != nil {
				t.Fatal(unquoteErr)
			}
			for _, feature := range []string{"team", "workflow", "groupchat"} {
				prefix := "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/" + feature
				if module == prefix || strings.HasPrefix(module, prefix+"/") {
					t.Errorf("%s imports concrete feature %s", name, module)
				}
			}
		}
	}
}
