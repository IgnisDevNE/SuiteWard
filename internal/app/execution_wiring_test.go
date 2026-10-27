package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"testing"
)

// TestTheServiceComposesTheChecksJobWithTheExecutionLedger scans serve.go: the checks job that the service runs is built with every dependency of execution. A
// job without its Receipt ledger panics the first time a receipt applies, and the run of the project is never decided.
func TestTheServiceComposesTheChecksJobWithTheExecutionLedger(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "serve.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var literals [][]string
	ast.Inspect(file, func(n ast.Node) bool {
		literal, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if composed, ok := literal.Type.(*ast.Ident); !ok || composed.Name != "checksJob" {
			return true
		}
		var keys []string
		for _, element := range literal.Elts {
			if pair, ok := element.(*ast.KeyValueExpr); ok {
				if key, ok := pair.Key.(*ast.Ident); ok {
					keys = append(keys, key.Name)
				}
			}
		}
		literals = append(literals, keys)
		return true
	})
	if len(literals) != 1 {
		t.Fatalf("serve.go builds %d checks jobs, want exactly one", len(literals))
	}
	for _, field := range []string{"executions", "receipts", "liveness", "executor", "executorConfigured"} {
		if !slices.Contains(literals[0], field) {
			t.Errorf("the checks job that serve.go builds sets no %s", field)
		}
	}
}
