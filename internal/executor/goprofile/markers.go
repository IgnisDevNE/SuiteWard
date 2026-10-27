package goprofile

import (
	"cmp"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Marker is a line of a non-test Go file that looks at its test environment. A marker is a line to read, never a
// verdict. Kind is one of testing-import, testing-testing, flag-lookup-test, exit-in-init, test-file-read, env-read,
// forged-event.
type Marker struct {
	Kind, Path string
	Line       int
}

// Markers flags test-aware code in the non-test Go files of changed (path to source), sorted by path and line, with no
// marker twice:
//   - testing-import: the file imports testing, whatever it calls it;
//   - testing-testing: a call of testing.Testing;
//   - flag-lookup-test: a call of flag.Lookup with a literal that starts with "test.";
//   - exit-in-init: a call of os.Exit or syscall.Exit inside an init function or a package-level var declaration, and a
//     call of syscall.Exit anywhere;
//   - test-file-read: a string literal that names a _test.go file;
//   - env-read: a call of os.Getenv or os.LookupEnv with the literal "TESTING" or "CI";
//   - forged-event: a string literal that holds a SYN byte (\x16), the byte that go test frames the events of a test
//     binary with. A package whose init prints such frames and exits with status 0 reports any result, and the exit
//     status contradicts nothing: this marker and exit-in-init are the only signs of it.
//
// The line of a marker is the line of the file, whatever //line directives it holds.
//
// The packages are found through the file's own imports, so a renamed import is seen, and a dot import is not. A file
// that does not parse is read as far as it parses: its build failure shows in the run, and a marker is only a hint. The
// caller keeps the list within the wire's bound.
func Markers(changed map[string][]byte) []Marker {
	var out []Marker
	for _, file := range slices.Sorted(maps.Keys(changed)) {
		if strings.HasSuffix(file, ".go") && !strings.HasSuffix(file, "_test.go") {
			out = append(out, fileMarkers(file, changed[file])...)
		}
	}
	return out
}

// finder collects the markers of one file.
type finder struct {
	fset  *token.FileSet
	file  string
	names map[string]string
	found []Marker
}

func (m *finder) add(kind string, pos token.Pos) {
	m.found = append(m.found, Marker{Kind: kind, Path: m.file, Line: m.fset.PositionFor(pos, false).Line})
}

func fileMarkers(file string, src []byte) []Marker {
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, file, src, parser.SkipObjectResolution) // a partial tree is walked as it is
	m := &finder{fset: fset, file: file, names: importNames(f)}
	for _, spec := range f.Imports {
		if unquote(spec.Path) == "testing" {
			m.add("testing-import", spec.Pos())
		}
	}
	for _, decl := range f.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
			m.exitsIn(gen)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			if n.Recv == nil && n.Name.Name == "init" && n.Body != nil {
				m.exitsIn(n.Body)
			}
		case *ast.CallExpr:
			m.call(n)
		case *ast.BasicLit:
			m.literal(n)
		}
		return true
	})
	slices.SortFunc(m.found, func(a, b Marker) int {
		return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(a.Kind, b.Kind))
	})
	return slices.Compact(m.found)
}

// exitsIn flags every os.Exit and syscall.Exit inside node, which is an init function's body or a package-level var.
func (m *finder) exitsIn(node ast.Node) {
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && (callee(call, m.names) == "os.Exit" || callee(call, m.names) == "syscall.Exit") {
			m.add("exit-in-init", call.Pos())
		}
		return true
	})
}

func (m *finder) call(call *ast.CallExpr) {
	switch callee(call, m.names) {
	case "syscall.Exit":
		m.add("exit-in-init", call.Pos())
	case "testing.Testing":
		m.add("testing-testing", call.Pos())
	case "flag.Lookup":
		if strings.HasPrefix(firstString(call), "test.") {
			m.add("flag-lookup-test", call.Pos())
		}
	case "os.Getenv", "os.LookupEnv":
		if v := firstString(call); v == "TESTING" || v == "CI" {
			m.add("env-read", call.Pos())
		}
	}
}

// literal flags a string literal that names a _test.go file, or holds a frame byte.
func (m *finder) literal(lit *ast.BasicLit) {
	if lit.Kind != token.STRING {
		return
	}
	value := unquote(lit)
	if strings.Contains(value, "_test.go") {
		m.add("test-file-read", lit.Pos())
	}
	if strings.Contains(value, "\x16") {
		m.add("forged-event", lit.Pos())
	}
}

func unquote(lit *ast.BasicLit) string {
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}

// importNames maps the name a file gives to each package it imports to the package's import path.
func importNames(f *ast.File) map[string]string {
	names := map[string]string{}
	for _, spec := range f.Imports {
		p := unquote(spec.Path)
		if spec.Name != nil {
			names[spec.Name.Name] = p
		} else {
			names[p[strings.LastIndex(p, "/")+1:]] = p
		}
	}
	return names
}

// callee is the import path and the name of the function that call calls through an imported package ("os.Exit"), or
// empty for any other call. The packages of the markers have one-element paths, so the path is also their name.
func callee(call *ast.CallExpr, names map[string]string) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || names[pkg.Name] == "" {
		return ""
	}
	return names[pkg.Name] + "." + sel.Sel.Name
}

// firstString is the value of the first argument of call when it is a string literal, or empty.
func firstString(call *ast.CallExpr) string {
	if len(call.Args) == 0 {
		return ""
	}
	if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
		return unquote(lit)
	}
	return ""
}
