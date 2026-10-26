package goprofile

import (
	"bytes"
	"cmp"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// declaration is one top-level test function of one file. runnable says that the sandbox's build context selects the
// file; text is the source of the function from `func` to its closing brace.
type declaration struct {
	id       TestID
	text     string
	runnable bool
}

// key is what go test -run selects by: the package and the name, whatever the file.
type key struct{ pkg, name string }

// DeclaredTests lists the test functions of the _test.go files in files (paths relative to the root, with slashes). A
// function is a test when it is a top-level func TestXxx(t *testing.T): not TestMain, no benchmark, fuzz target or
// example, no method, no other signature. The sandbox's build context (linux, goarch, cgo off, no tags) decides which
// tests it can run: those of the files it leaves out are Excluded. A name is listed once per package, and as excluded
// only when no runnable file declares it; of several files that declare it, the first by path stands for it.
// Directories that go does not build (testdata, vendor, and names that start with "." or "_") hold no test. A file
// that does not parse is an error, whether the context selects it or not.
func DeclaredTests(files map[string][]byte, goarch string) (Declared, error) {
	decls, err := scan(files, goarch)
	if err != nil {
		return Declared{}, err
	}
	var declared Declared
	for _, d := range ordered(pick(decls)) {
		if d.runnable {
			declared.Tests = append(declared.Tests, d.id)
		} else {
			declared.Excluded = append(declared.Excluded, ExcludedTest{TestID: d.id})
		}
	}
	return declared, nil
}

// ChangedTests lists the test functions that head adds or changes against canonical: a name that canonical does not
// declare in the package is added, one whose declaration text differs is changed, and one whose text is the same is
// neither. Both lists hold the tests of files that the build context leaves out too: they are not run, and the caller
// lists them as excluded by looking them up in DeclaredTests. A name that two files of a package declare (an internal
// and an external test package) is changed when either text differs, and its File is the first of them in the head.
// Files are read as DeclaredTests reads them.
func ChangedTests(head, canonical map[string][]byte, goarch string) (added, changed []TestID, err error) {
	headDecls, err := scan(head, goarch)
	if err != nil {
		return nil, nil, fmt.Errorf("head: %w", err)
	}
	canonicalDecls, err := scan(canonical, goarch)
	if err != nil {
		return nil, nil, fmt.Errorf("canonical: %w", err)
	}
	before := pick(canonicalDecls)
	for _, d := range ordered(pick(headDecls)) {
		old, ok := before[key{d.id.Package, d.id.Name}]
		switch {
		case !ok:
			added = append(added, d.id)
		case old.text != d.text:
			changed = append(changed, d.id)
		}
	}
	return added, changed, nil
}

// scan reads the test files of files, in path order.
func scan(files map[string][]byte, goarch string) ([]declaration, error) {
	// A Context reads files through OpenFile and joins paths with JoinPath, so these in-memory paths need no disk and
	// no separator of the host. The release tags are the toolchain's (the executor is built with the sandbox's Go); the
	// build and tool tags are none, and cgo is off, as the image has no C compiler.
	ctx := build.Context{
		GOOS:        "linux",
		GOARCH:      goarch,
		Compiler:    "gc",
		ReleaseTags: build.Default.ReleaseTags,
		JoinPath:    path.Join,
		OpenFile: func(name string) (io.ReadCloser, error) {
			src, ok := files[name]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return io.NopCloser(bytes.NewReader(src)), nil
		},
	}
	var decls []declaration
	for _, file := range slices.Sorted(maps.Keys(files)) {
		if !isBuiltTestFile(file) {
			continue
		}
		runnable, err := ctx.MatchFile(path.Dir(file), path.Base(file))
		if err != nil {
			return nil, fmt.Errorf("build constraints of %s: %w", file, err)
		}
		found, err := testFunctions(file, files[file])
		if err != nil {
			return nil, err
		}
		for _, d := range found {
			d.runnable = runnable
			decls = append(decls, d)
		}
	}
	return decls, nil
}

// isBuiltTestFile is whether file is a _test.go file of a directory that go builds: not under testdata or vendor, and
// under no directory that starts with "." or "_".
func isBuiltTestFile(file string) bool {
	if !strings.HasSuffix(file, "_test.go") {
		return false
	}
	dir := path.Dir(file)
	if dir == "." {
		return true
	}
	for _, element := range strings.Split(dir, "/") {
		if element == "testdata" || element == "vendor" || strings.HasPrefix(element, ".") || strings.HasPrefix(element, "_") {
			return false
		}
	}
	return true
}

func testFunctions(file string, src []byte) ([]declaration, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	testing := importName(f, "testing")
	var out []declaration
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || !isTest(fn, testing) {
			continue
		}
		start, end := fset.Position(fn.Pos()).Offset, fset.Position(fn.End()).Offset
		out = append(out, declaration{
			id:   TestID{Package: path.Dir(file), Name: fn.Name.Name, File: file},
			text: string(src[start:end]),
		})
	}
	return out, nil
}

// importName is the name that f gives to the package at importPath, "." for a dot import, or empty when it does not
// import it.
func importName(f *ast.File, importPath string) string {
	for _, spec := range f.Imports {
		if p, err := strconv.Unquote(spec.Path.Value); err != nil || p != importPath {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return path.Base(importPath)
	}
	return ""
}

// isTest is the go tool's rule for a test function, with the testing package named as the file names it (testing is
// empty when the file does not import it).
func isTest(fn *ast.FuncDecl, testing string) bool {
	if testing == "" || fn.Recv != nil || fn.Type.TypeParams != nil || !isTestName(fn.Name.Name) {
		return false
	}
	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		return false
	}
	params := fn.Type.Params.List
	if len(params) != 1 || len(params[0].Names) > 1 {
		return false
	}
	star, ok := params[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	switch x := star.X.(type) {
	case *ast.Ident:
		return testing == "." && x.Name == "T"
	case *ast.SelectorExpr:
		pkg, ok := x.X.(*ast.Ident)
		return ok && pkg.Name == testing && x.Sel.Name == "T"
	}
	return false
}

// isTestName: Test, or Test followed by anything but a lowercase letter, and not TestMain.
func isTestName(name string) bool {
	rest, ok := strings.CutPrefix(name, "Test")
	if !ok || name == "TestMain" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return rest == "" || !unicode.IsLower(r)
}

// pick gives each name of a package one declaration: a runnable one stands for the name over an excluded one, and of
// several of the same kind the first stands (decls are in path order), with the text of all of them so that a change of
// any is seen.
func pick(decls []declaration) map[key]declaration {
	picked := map[key]declaration{}
	for _, d := range decls {
		k := key{d.id.Package, d.id.Name}
		cur, ok := picked[k]
		switch {
		case !ok || (d.runnable && !cur.runnable):
			picked[k] = d
		case d.runnable == cur.runnable:
			// A NUL is no part of any Go source, so it cannot join two texts into one that another pair would make.
			cur.text += "\x00" + d.text
			picked[k] = cur
		}
	}
	return picked
}

func ordered(picked map[key]declaration) []declaration {
	return slices.SortedFunc(maps.Values(picked), func(a, b declaration) int {
		return cmp.Or(cmp.Compare(a.id.Package, b.id.Package), cmp.Compare(a.id.Name, b.id.Name))
	})
}
