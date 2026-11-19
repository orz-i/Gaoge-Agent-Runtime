package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const acceptanceBuggyCode = "package sample\nfunc Clamp(value, low, high int) int { return low }\n"
const acceptanceFixedCode = "package sample\nfunc Clamp(value, low, high int) int { if value < low { return low }; if value > high { return high }; return value }\n"
const acceptancePublicTests = `package sample
import "testing"
func TestClampPublic(t *testing.T) {
 for _, x := range []struct{v,l,h,w int}{{5,0,10,5},{-2,0,10,0},{20,0,10,10}} {
  if got := Clamp(x.v,x.l,x.h); got != x.w { t.Errorf("Clamp(%d,%d,%d)=%d want %d",x.v,x.l,x.h,got,x.w) }
 }
}
`
const acceptanceHeldOutTests = `package sample
import "testing"
func TestClampHeldOut(t *testing.T) {
 for low := -9; low < 3; low++ { for high := low; high < low+7; high++ { for v := low-2; v <= high+2; v++ {
  want := v; if want < low { want = low }; if want > high { want = high }
  if got := Clamp(v,low,high); got != want { t.Fatalf("held-out mismatch") }
 } } }
}
`

// This is not a general code sandbox. The model may edit only a finite, pure
// integer function: no imports, calls, globals, loops, allocation or directives.
// The AST is normalized without comments before any Go subprocess is started.
func normalizeAcceptanceCode(source string) ([]byte, error) {
	if len(source) > 8192 {
		return nil, errors.New("code fixture too large")
	}
	set := token.NewFileSet()
	file, err := parser.ParseFile(set, "clamp.go", source, 0)
	if err != nil || file.Name.Name != "sample" || len(file.Decls) != 1 || len(file.Imports) != 0 {
		return nil, errors.New("invalid pure fixture source")
	}
	function, ok := file.Decls[0].(*ast.FuncDecl)
	if !ok || function.Recv != nil || function.Name.Name != "Clamp" || function.Body == nil || function.Type.TypeParams != nil ||
		len(function.Type.Params.List) != 1 || len(function.Type.Params.List[0].Names) != 3 || function.Type.Results == nil || len(function.Type.Results.List) != 1 {
		return nil, errors.New("invalid pure fixture signature")
	}
	parameters := function.Type.Params.List[0]
	if parameters.Names[0].Name != "value" || parameters.Names[1].Name != "low" || parameters.Names[2].Name != "high" {
		return nil, errors.New("invalid pure fixture parameters")
	}
	parameterType, parameterOK := parameters.Type.(*ast.Ident)
	returnType, returnOK := function.Type.Results.List[0].Type.(*ast.Ident)
	if !parameterOK || !returnOK || parameterType.Name != "int" || returnType.Name != "int" || len(function.Type.Results.List[0].Names) != 0 {
		return nil, errors.New("invalid pure fixture types")
	}
	valid, nodes := true, 0
	ast.Inspect(function.Body, func(node ast.Node) bool {
		if node == nil || !valid {
			return false
		}
		nodes++
		if nodes > 256 {
			valid = false
			return false
		}
		switch value := node.(type) {
		case *ast.BlockStmt, *ast.ReturnStmt, *ast.IfStmt, *ast.ParenExpr, *ast.BinaryExpr:
		case *ast.UnaryExpr:
			valid = value.Op == token.ADD || value.Op == token.SUB || value.Op == token.XOR || value.Op == token.NOT
		case *ast.Ident:
			valid = value.Name == "value" || value.Name == "low" || value.Name == "high" || value.Name == "true" || value.Name == "false"
		case *ast.BasicLit:
			_, parseErr := strconv.ParseInt(value.Value, 0, 64)
			valid = value.Kind == token.INT && parseErr == nil
		default:
			valid = false
		}
		return valid
	})
	if !valid {
		return nil, errors.New("source exceeds the pure fixture grammar")
	}
	var output bytes.Buffer
	if err := format.Node(&output, set, file); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (task *acceptanceTask) prepareCode() ([]tools.Registration, error) {
	for name, content := range map[string]string{"go.mod": "module acceptance.local/sample\n\ngo 1.26\n", "clamp.go": acceptanceBuggyCode, "clamp_test.go": acceptancePublicTests} {
		if err := os.WriteFile(task.artifactPath(name), []byte(content), 0600); err != nil {
			return nil, err
		}
	}
	task.goal = "Fix Clamp(value, low, high int) so it returns value inside the inclusive interval and otherwise the nearest bound, assuming low <= high. Read the source and public tests, run tests, edit clamp.go and verify again. write_code accepts the complete package sample source with exactly that function, the same grouped parameters, and only pure integer expressions/if/return; no imports, calls, globals or loops. Do not alter tests."
	return []tools.Registration{
		acceptanceRegistration("read_code", "Read clamp.go", `{"type":"object","additionalProperties":false}`, func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			content, err := os.ReadFile(task.artifactPath("clamp.go"))
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			return acceptanceReceipt(request, map[string]string{"source": string(content)})
		}),
		acceptanceRegistration("read_tests", "Read the trusted public unit tests", `{"type":"object","additionalProperties":false}`, func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			return acceptanceReceipt(request, map[string]string{"tests": acceptancePublicTests})
		}),
		acceptanceRegistration("write_code", "Replace only the bounded pure Clamp function", `{"type":"object","properties":{"source":{"type":"string","maxLength":8192}},"required":["source"],"additionalProperties":false}`, func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			var input struct{ Source string }
			if json.Unmarshal(request.Call.Arguments, &input) != nil {
				return acceptanceRefusal(request, "code.arguments", "Provide the complete pure fixture source")
			}
			content, err := normalizeAcceptanceCode(input.Source)
			if err != nil {
				return acceptanceRefusal(request, "code.grammar", "Only the described pure Clamp function is allowed; no other declarations, calls, imports or loops")
			}
			if err := os.WriteFile(task.artifactPath("clamp.go"), content, 0600); err != nil {
				return tools.ExecutionResult{}, err
			}
			return acceptanceReceipt(request, map[string]bool{"saved": true})
		}),
		acceptanceRegistration("run_tests", "Run only the fixed Go unit-test command in the owned fixture workspace", `{"type":"object","additionalProperties":false}`, func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			passed, err := task.testCode(ctx, false)
			if err != nil {
				return tools.ExecutionResult{}, err
			}
			task.publicTestsRan = true
			return acceptanceReceipt(request, map[string]bool{"testsPassed": passed})
		}),
	}, nil
}

func (task *acceptanceTask) testCode(ctx context.Context, compileOnly bool) (bool, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := os.MkdirAll(task.artifactPath("tmp"), 0700); err != nil {
		return false, err
	}
	args := []string{"test", "-count=1"}
	if compileOnly {
		args = append(args, "-run", "^$")
	}
	args = append(args, "./...")
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = task.directory
	// Do not pass Provider keys, GOFLAGS, proxies or other Host secrets to Go.
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + task.directory,
		"GOCACHE=" + filepath.Join(cache, "go-build"), "GOMODCACHE=" + task.artifactPath("module-cache"),
		"TMPDIR=" + task.artifactPath("tmp"), "GOWORK=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0"}
	output, err := command.CombinedOutput()
	if len(output) > 8192 {
		output = output[:8192]
	}
	task.lastTestOutput = string(output)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var failed *exec.ExitError
	if errors.As(err, &failed) {
		return false, nil // a known failed test is a Tool result, not unknown execution
	}
	return err == nil, err
}

func (task *acceptanceTask) verifyCode(ctx context.Context, checks map[string]bool) ([]byte, error) {
	content, err := os.ReadFile(task.artifactPath("clamp.go"))
	if err != nil {
		return nil, err
	}
	checks["source_actually_changed"] = string(content) != acceptanceBuggyCode
	checks["public_tests_executed"] = task.publicTestsRan
	_, sourceErr := normalizeAcceptanceCode(string(content))
	checks["pure_source_validated"] = sourceErr == nil
	if !checks["pure_source_validated"] {
		return content, nil
	}
	// Only after the Agent completes: never expose held-out outcomes to its loop.
	if err := os.WriteFile(task.artifactPath("heldout_test.go"), []byte(acceptanceHeldOutTests), 0600); err != nil {
		return nil, err
	}
	checks["independent_held_out_tests_pass"], err = task.testCode(ctx, false)
	return content, err
}
