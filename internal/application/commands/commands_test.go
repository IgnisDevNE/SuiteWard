package commands_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commands/commandstest"
)

// A new disposition must be placed in exactly one of the counted and ignored sets of the conformance suite, so that it cannot
// drift between AffectsCheck and the store's own list.
func TestEveryDispositionIsPlacedInExactlyOneConformanceSet(t *testing.T) {
	placed := map[commands.Disposition]int{}
	for _, d := range slices.Concat(commandstest.CountedDispositions, commandstest.IgnoredDispositions) {
		placed[d]++
	}
	all := commands.Dispositions()
	if len(all) != len(placed) {
		t.Fatalf("Dispositions() has %d codes and the counted and ignored sets %d; want the same codes", len(all), len(placed))
	}
	for _, d := range all {
		if placed[d] != 1 {
			t.Fatalf("disposition %q is in %d of the counted and ignored sets; want exactly one", d, placed[d])
		}
		counted := slices.Contains(commandstest.CountedDispositions, d)
		if d.AffectsCheck() != counted {
			t.Fatalf("AffectsCheck(%q) = %v, but the store counts it: %v", d, d.AffectsCheck(), counted)
		}
	}
}

// The list that Dispositions returns is kept by hand next to the constants, so a constant that is declared and not listed would pass
// every test above and be refused by Entry.Validate. The constants are read from the source, which is the only place that names them all.
func TestEveryDeclaredDispositionIsInTheList(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "commands.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[commands.Disposition]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			if kind, ok := value.Type.(*ast.Ident); !ok || kind.Name != "Disposition" {
				continue
			}
			for _, literal := range value.Values {
				text, err := strconv.Unquote(literal.(*ast.BasicLit).Value)
				if err != nil {
					t.Fatal(err)
				}
				declared[commands.Disposition(text)] = true
			}
		}
	}
	listed := commands.Dispositions()
	if len(declared) == 0 || len(declared) != len(listed) {
		t.Fatalf("commands.go declares %d dispositions and Dispositions() lists %d; want the same codes", len(declared), len(listed))
	}
	for _, d := range listed {
		if !declared[d] {
			t.Fatalf("Dispositions() lists %q, which commands.go does not declare", d)
		}
	}
}
