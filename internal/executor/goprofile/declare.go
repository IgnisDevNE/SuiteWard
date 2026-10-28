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
	reason   string // why it is not runnable: ReasonBuildConstraint or ReasonNestedModule
}

// key is what go test -run selects by: the package and the name, whatever the file.
type key struct{ pkg, name string }

// DeclaredTests lists the test functions of the _test.go files in files (paths relative to the root, with slashes), which
// also holds the go.mod files of the tree: a directory below the root with a go.mod of its own is another module. A
// function is a test when it is a top-level func TestXxx(t *testing.T): not TestMain, no benchmark, fuzz target or
// example, no method, no other signature. The sandbox's build context (linux, goarch, cgo off, the release tags of the
// toolchain, the default level tag of goarch, no other tag; goexperiment tags are not modelled) decides which tests it
// can run: those of the files it leaves out are Excluded with the reason build-constraint, and those under another
// module are Excluded with the reason nested-module (the root invocation cannot name their packages; this reason comes
// first). A name is listed once per package, and as excluded only when no runnable file declares it; of several files
// that declare it, the first by path stands for it. Directories that go does not build (testdata, vendor, and names
// that start with "." or "_") hold no test.
//
// A file is the candidate's, so none of them fails the call. A runnable file that go cannot read (a syntax error, a build
// constraint that does not parse, a NUL byte) gives no test and is listed in Unparsed: the build of its package fails
// when the tests run. A file that the context leaves out, or a nested module's, is skipped if it cannot be read, since
// go reads only the header of such a file.
func DeclaredTests(files map[string][]byte, goarch string) Declared {
	scanned := scan(files, goarch)
	declared := Declared{Unparsed: scanned.unparsed}
	for _, d := range ordered(pick(scanned.decls)) {
		if d.runnable {
			declared.Tests = append(declared.Tests, d.id)
		} else {
			declared.Excluded = append(declared.Excluded, ExcludedTest{TestID: d.id, Reason: d.reason})
		}
	}
	return declared
}

// ChangedTests lists the test functions that head adds or changes against canonical: a name that canonical does not
// declare in the package is added, one whose declaration text differs is changed, and one whose text is the same is
// neither. Both lists hold the tests of files that the build context leaves out too: they are not run, and the caller
// lists them as excluded by looking them up in DeclaredTests. A name that two files of a package declare (an internal
// and an external test package) is changed when either text differs, and its File is the first of them in the head.
// Files are read as DeclaredTests reads them, go.mod files included, and one that cannot be read gives no test (the
// caller finds it in the Unparsed of DeclaredTests).
func ChangedTests(head, canonical map[string][]byte, goarch string) (added, changed []TestID) {
	before := pick(scan(canonical, goarch).decls)
	for _, d := range ordered(pick(scan(head, goarch).decls)) {
		old, ok := before[key{d.id.Package, d.id.Name}]
		switch {
		case !ok:
			added = append(added, d.id)
		case old.text != d.text:
			changed = append(changed, d.id)
		}
	}
	return added, changed
}

// scanned is what the test files of a tree hold: their test functions, and the runnable files that could not be read.
type scanned struct {
	decls    []declaration
	unparsed []string
}

// toolTags are the level tags that the go tool sets for the default level of the architecture (GOAMD64=v1, GOARM64=v8.0):
// one for each level up to the default, which for these is the default itself. The goexperiment tags are not modelled,
// and another architecture has no tag here.
var toolTags = map[string][]string{
	"amd64": {"amd64.v1"},
	"arm64": {"arm64.v8.0"},
}

// scan reads the test files of files, in path order.
func scan(files map[string][]byte, goarch string) scanned {
	// A Context reads files through OpenFile and joins paths with JoinPath, so these in-memory paths need no disk and
	// no separator of the host. The release tags are the toolchain's (the executor is built with the sandbox's Go); the
	// build tags are none, and cgo is off, as the image has no C compiler.
	ctx := build.Context{
		GOOS:        "linux",
		GOARCH:      goarch,
		Compiler:    "gc",
		ReleaseTags: build.Default.ReleaseTags,
		ToolTags:    toolTags[goarch],
		JoinPath:    path.Join,
		OpenFile: func(name string) (io.ReadCloser, error) {
			src, ok := files[name]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return io.NopCloser(bytes.NewReader(src)), nil
		},
	}
	modules := moduleDirs(files)
	var out scanned
	for _, file := range slices.Sorted(maps.Keys(files)) {
		if !isBuiltTestFile(file) {
			continue
		}
		nested := inNestedModule(path.Dir(file), modules)
		selected, err := ctx.MatchFile(path.Dir(file), path.Base(file))
		if err != nil {
			// go reads this header too, and fails the package, unless it is another module's.
			if !nested {
				out.unparsed = append(out.unparsed, file)
			}
			continue
		}
		found, err := testFunctions(file, files[file])
		if err != nil {
			if selected && !nested {
				out.unparsed = append(out.unparsed, file)
			}
			continue
		}
		reason := ""
		switch {
		case nested:
			reason = ReasonNestedModule
		case !selected:
			reason = ReasonBuildConstraint
		}
		for _, d := range found {
			d.runnable, d.reason = reason == "", reason
			out.decls = append(out.decls, d)
		}
	}
	return out
}

// moduleDirs are the directories below the root that hold a go.mod of their own.
func moduleDirs(files map[string][]byte) map[string]bool {
	dirs := map[string]bool{}
	for file := range files {
		if dir := path.Dir(file); dir != "." && path.Base(file) == "go.mod" {
			dirs[dir] = true
		}
	}
	return dirs
}

// inNestedModule is whether dir, or a directory above it but below the root, is the root of another module.
func inNestedModule(dir string, modules map[string]bool) bool {
	for ; dir != "."; dir = path.Dir(dir) {
		if modules[dir] {
			return true
		}
	}
	return false
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
