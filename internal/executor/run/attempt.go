package run

import (
	"archive/tar"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"runtime"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/source"
)

// attempt is one leased job, with what the executor knows of it before it touches a commit: the manifest, the declaration that decides the
// scope, and the entries of the trees. goarch is the sandbox's: the container runs natively on the executor's machine.
type attempt struct {
	e         *Executor
	lease     *client.Lease
	toolchain string
	limits    source.Limits
	archives  archives
	files     manifestFiles
	entries   map[source.Side][]execution.TreeEntry
	governing artifact.Manifest
	decl      scope.Declaration
}

var goarch = runtime.GOARCH

func newAttempt(e *Executor, lease *client.Lease, toolchain string) (*attempt, error) {
	governing, err := governingManifest(lease.Manifest)
	if err != nil {
		return nil, err
	}
	decl, err := declarationOf(lease, governing)
	if err != nil {
		return nil, err
	}
	a := &attempt{
		e: e, lease: lease, toolchain: toolchain, archives: archives{lease}, files: manifestFiles{lease}, governing: governing, decl: decl,
		limits:  source.Limits{ArchiveBytes: lease.Limits.ArchiveBytes, FileBytes: lease.Limits.FileBytes, TmpfsMiB: lease.Limits.TmpfsMiB},
		entries: map[source.Side][]execution.TreeEntry{source.SideHead: treeEntries(lease.Head.Tree)},
	}
	if lease.Base != nil {
		a.entries[source.SideBase] = treeEntries(lease.Base.Tree)
	}
	return a, nil
}

func treeEntries(wire []client.TreeEntry) []execution.TreeEntry {
	entries := make([]execution.TreeEntry, len(wire))
	for i, w := range wire {
		entries[i] = execution.TreeEntry{Path: w.Path, Mode: execution.GitMode(w.Mode), BlobSHA1: w.SHA}
	}
	return entries
}

// governingManifest builds the manifest that the job names and checks it is the one that its digest says. The service built both, so a
// difference is its bug, and the executor does not guess which of the two is right.
func governingManifest(m client.Manifest) (artifact.Manifest, error) {
	entries := make([]artifact.Entry, len(m.Files))
	for i, f := range m.Files {
		d, err := artifact.ParseDigest(f.Digest)
		if err != nil {
			return artifact.Manifest{}, refuse(reasonExecutorError, "the manifest file %q: %w", f.Path, err)
		}
		entries[i] = artifact.Entry{Path: f.Path, Content: d}
	}
	manifest, err := artifact.NewManifest(entries)
	if err != nil {
		return artifact.Manifest{}, refuse(reasonExecutorError, "the manifest of the job is invalid: %w", err)
	}
	if manifest.Digest().String() != m.Digest {
		return artifact.Manifest{}, refuse(reasonExecutorError, "the manifest of the job does not have the digest that the job names")
	}
	return manifest, nil
}

// declarationOf is the declaration that the manifest names: the bytes that came with the job, checked against the digest that the manifest
// lists for .suiteward.yml. A manifest without one has the declaration of no declaration, which covers the protected defaults alone.
func declarationOf(lease *client.Lease, governing artifact.Manifest) (scope.Declaration, error) {
	var listed artifact.Digest
	for _, e := range governing.Entries() {
		if e.Path == scope.DeclarationPath {
			listed = e.Content
		}
	}
	raw := lease.Declaration
	switch {
	case len(raw) == 0 && listed.IsZero():
		return scope.NoDeclaration(), nil
	case len(raw) == 0 || listed.IsZero():
		return scope.Declaration{}, refuse(reasonExecutorError, "the job and its manifest disagree on whether there is a declaration")
	case artifact.Hash(raw) != listed:
		return scope.Declaration{}, refuse(reasonExecutorError, "the declaration of the job is not the bytes that its manifest lists")
	}
	d, err := scope.ParseDeclaration(raw)
	if err != nil {
		return scope.Declaration{}, refuse(reasonExecutorError, "the declaration of the job does not parse: %w", err)
	}
	return d, nil
}

// inspect checks the archive of a side against the entries of its commit's tree, and gives the files of the commit with their digests.
func (a *attempt) inspect(ctx context.Context, side source.Side) (source.Inspection, error) {
	rc, err := a.archives.Open(ctx, side)
	if err != nil {
		return source.Inspection{}, err
	}
	defer func() { _ = rc.Close() }()
	return source.Inspect(ctx, rc, a.entries[side], a.limits)
}

// isRoot is a file that the Go profile reads of a tree: the module and its checksums.
func isRoot(p string) bool { return p == "go.mod" || p == "go.sum" }

func isTestFile(p string) bool { return strings.HasSuffix(p, "_test.go") }

// read gives visit the files of tree that want selects, one at a time, with their bytes. The source package has no function that gives
// bytes, so the files are composed, which checks each against the tree and the plan while it streams, into a pipe that is read back as a tar:
// no more than one file is in memory at once besides what visit keeps. A failure of the composition is returned, and what visit saw before
// it is then not to be used.
func (a *attempt) read(ctx context.Context, tree []execution.TreeFile, want func(execution.TreeFile) bool, visit func(path string, data []byte) error) error {
	subset := slices.DeleteFunc(slices.Clone(tree), func(f execution.TreeFile) bool { return !want(f) })
	if len(subset) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := source.Compose(ctx, pw, subset, a.archives, a.entries, a.files, a.limits)
		_ = pw.CloseWithError(err)
		done <- err
	}()
	readErr := readTar(pr, visit)
	_ = pr.CloseWithError(readErr)
	cancel()
	if composeErr := <-done; composeErr != nil && !errors.Is(composeErr, io.ErrClosedPipe) {
		return composeErr
	}
	return readErr
}

// readTar visits the regular files of a tar that source.Compose wrote, by their path in the tree.
func readTar(r io.Reader, visit func(path string, data []byte) error) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		p, ok := strings.CutPrefix(h.Name, source.TreeDir+"/")
		if !ok {
			return fmt.Errorf("the composed tar holds %q outside %s", h.Name, source.TreeDir)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		if err := visit(p, data); err != nil {
			return err
		}
	}
}

// collect is a visit that keeps every file it is given.
func collect(into map[string][]byte) func(string, []byte) error {
	return func(p string, data []byte) error { into[p] = data; return nil }
}

// treeOf is the files of a manifest as the files of a tree that comes from one source.
func treeOf(m artifact.Manifest, src execution.Source) []execution.TreeFile {
	var tree []execution.TreeFile
	for _, e := range m.Entries() {
		tree = append(tree, execution.TreeFile{Path: e.Path, Content: e.Content, Source: src})
	}
	return tree
}

// withModules adds, for each go.mod of the tree, an empty file: DeclaredTests reads the places of the modules, which decide the nested ones,
// and never their text.
func withModules(files map[string][]byte, tree []execution.TreeFile) map[string][]byte {
	out := make(map[string][]byte, len(files))
	maps.Copy(out, files)
	for _, f := range tree {
		if path.Base(f.Path) == "go.mod" {
			if _, ok := out[f.Path]; !ok {
				out[f.Path] = nil
			}
		}
	}
	return out
}

// receiptModules is the go.mod files of the head's tree that may make a nested module in a receipt, which are the protected ones:
//   - a go.mod at a path where the merge base has one;
//   - a go.mod that the revision's manifest lists. The revision's manifest is the head's files under the approved patterns, and the job
//     holds the canonical version that the revision expects, not that manifest: a head go.mod counts when the expected canonical's scope
//     covers its path (the patterns that the canonical approves; a pattern that only the head adds is not approved yet, and covers nothing);
//   - a go.mod that the expected canonical itself lists (a.governing, which its own scope normally covers too).
//
// Any other go.mod of the head is the candidate's, in a directory that it may have chosen, and must not turn a test of the candidate that
// go would run into an excluded one. A nested module that the base has and the head removed is no module of the head: its tests run, and
// the base side, which still has it, cannot build their package.
func (a *attempt) receiptModules(headTree []execution.TreeFile, base artifact.Manifest) ([]execution.TreeFile, error) {
	known := map[string]bool{}
	for _, m := range []artifact.Manifest{base, a.governing} {
		for _, e := range m.Entries() {
			if path.Base(e.Path) == "go.mod" {
				known[e.Path] = true
			}
		}
	}
	approved := scope.NewScope(a.decl)
	var modules []execution.TreeFile
	for _, f := range headTree {
		if path.Base(f.Path) != "go.mod" {
			continue
		}
		covered, err := approved.Covers(f.Path)
		if err != nil {
			return nil, err
		}
		if covered || known[f.Path] {
			modules = append(modules, f)
		}
	}
	return modules, nil
}

// canonical runs the declared tests of the governing manifest on the head with the whole manifest laid over it.
func (a *attempt) canonical(ctx context.Context) (completion, error) {
	head, err := a.inspect(ctx, source.SideHead)
	if err != nil {
		return completion{}, err
	}
	tree, err := execution.CanonicalTree(head.Files, a.governing, scope.NewScope(a.decl))
	if err != nil {
		return completion{}, err
	}
	// The expected tests are those that the manifest's own test files declare.
	files := map[string][]byte{}
	err = a.read(ctx, tree, func(f execution.TreeFile) bool {
		return isRoot(f.Path) || (f.Source == execution.FromManifest && isTestFile(f.Path))
	}, collect(files))
	if err != nil {
		return completion{}, err
	}
	// Only a go.mod that the governing manifest lists makes a nested module: a go.mod that the head adds in a protected test directory is the
	// candidate's, and must not turn the manifest's approved tests there into excluded ones. The root go.mod is read above, whoever has it.
	modules := slices.DeleteFunc(slices.Clone(tree), func(f execution.TreeFile) bool { return f.Source != execution.FromManifest })
	declared := goprofile.DeclaredTests(withModules(files, modules), goarch)
	done := completion{tests: declaredRows(declared), submodules: head.Submodules}
	if err := done.check(); err != nil {
		return completion{}, err
	}
	if len(declared.Tests) == 0 {
		return done, nil
	}
	s := side{which: source.SideHead, tree: tree}
	if err := a.prepare(&s, files["go.mod"], files["go.sum"]); err != nil {
		return completion{}, err
	}
	runs, err := a.runSide(ctx, &s, declared.Tests)
	if err != nil {
		return completion{}, err
	}
	fillRuns(done.tests, runs, nil)
	return done, nil
}

// declaredRows lists the declared tests, the runnable ones with no observation yet and the others with their reason, in the order of the wire.
func declaredRows(declared goprofile.Declared) []testRow {
	rows := []testRow{}
	for _, id := range declared.Tests {
		rows = append(rows, testRow{Package: id.Package, Name: id.Name, File: id.File, Kind: "declared"})
	}
	for _, x := range declared.Excluded {
		rows = append(rows, testRow{Package: x.Package, Name: x.Name, File: x.File, Kind: "declared", Excluded: x.Reason})
	}
	sortRows(rows)
	return rows
}

func sortRows(rows []testRow) {
	slices.SortFunc(rows, func(a, b testRow) int { return cmp.Or(cmp.Compare(a.Package, b.Package), cmp.Compare(a.Name, b.Name)) })
}

// fillRuns writes the observations of each side into the rows of the tests that ran. A canonical run has no base.
func fillRuns(rows []testRow, head, base map[goprofile.TestID][]execution.Observation) {
	names := func(runs []execution.Observation) []string {
		out := make([]string, len(runs))
		for i, o := range runs {
			out[i] = o.String()
		}
		return out
	}
	for i := range rows {
		id := goprofile.TestID{Package: rows[i].Package, Name: rows[i].Name, File: rows[i].File}
		if runs, ok := head[id]; ok {
			rows[i].Head = names(runs)
		}
		if runs, ok := base[id]; ok {
			rows[i].Base = names(runs)
		}
	}
}

// receipt runs the tests that the revision adds or changes on the head and on the base side, and flags the non-test Go files that differ.
func (a *attempt) receipt(ctx context.Context) (completion, error) {
	// Both archives are checked before anything is composed: a side that is not its commit's tree is found before the other is read.
	head, err := a.inspect(ctx, source.SideHead)
	if err != nil {
		return completion{}, err
	}
	base, err := a.inspect(ctx, source.SideBase)
	if err != nil {
		return completion{}, err
	}
	sel := execution.Selection{Include: a.decl.Include(), Exclude: a.decl.Exclude()}
	evaluated, err := execution.EvaluatedFiles(head.Files, a.governing, sel)
	if err != nil {
		return completion{}, err
	}
	baseTree, err := execution.BaseSideTree(base.Files, head.Files, a.governing, sel)
	if err != nil {
		return completion{}, err
	}
	headTree := treeOf(head.Files, execution.FromHead)

	headRead, err := a.readHead(ctx, headTree, evaluated, differing(head.Files, base.Files))
	if err != nil {
		return completion{}, err
	}
	canonical, err := a.readCanonical(ctx, evaluated)
	if err != nil {
		return completion{}, err
	}
	baseRoot := map[string][]byte{}
	if err := a.read(ctx, baseTree, func(f execution.TreeFile) bool { return isRoot(f.Path) }, collect(baseRoot)); err != nil {
		return completion{}, err
	}

	modules, err := a.receiptModules(headTree, base.Files)
	if err != nil {
		return completion{}, err
	}
	tests := withModules(headRead.tests, modules)
	added, changed := goprofile.ChangedTests(tests, canonical, goarch)
	declared := goprofile.DeclaredTests(tests, goarch)
	rows, runnable, err := receiptRows(added, changed, declared)
	if err != nil {
		return completion{}, err
	}
	done := completion{
		tests: rows, markers: capMarkers(headRead.markers), unparsed: declared.Unparsed,
		submodules: union(head.Submodules, base.Submodules),
	}
	if err := done.check(); err != nil {
		return completion{}, err
	}
	if len(runnable) == 0 {
		return done, nil
	}
	headSide, baseSide := side{which: source.SideHead, tree: headTree}, side{which: source.SideBase, tree: baseTree}
	// Each side's toolchain and dependencies are checked before the first container runs.
	if err := a.prepare(&headSide, headRead.root["go.mod"], headRead.root["go.sum"]); err != nil {
		return completion{}, err
	}
	if err := a.prepare(&baseSide, baseRoot["go.mod"], baseRoot["go.sum"]); err != nil {
		return completion{}, err
	}
	headRuns, err := a.runSide(ctx, &headSide, runnable)
	if err != nil {
		return completion{}, err
	}
	baseRuns, err := a.runSide(ctx, &baseSide, runnable)
	if err != nil {
		return completion{}, err
	}
	fillRuns(done.tests, headRuns, baseRuns)
	return done, nil
}

// headRead is what the head side gives the analysis: its module files, the evaluated test files, and the markers of the non-test Go files.
type headRead struct {
	root    map[string][]byte
	tests   map[string][]byte
	markers []markerRow
}

// readHead reads, from the head's files, the module files, the evaluated test files, and the non-test Go files that differ from the base,
// which go through the markers one at a time, so that their bytes are not kept.
func (a *attempt) readHead(ctx context.Context, tree []execution.TreeFile, evaluated []string, differs map[string]bool) (headRead, error) {
	evaluatedTests := map[string]bool{}
	for _, p := range evaluated {
		if isTestFile(p) {
			evaluatedTests[p] = true
		}
	}
	read := headRead{root: map[string][]byte{}, tests: map[string][]byte{}}
	err := a.read(ctx, tree, func(f execution.TreeFile) bool {
		return isRoot(f.Path) || evaluatedTests[f.Path] || differs[f.Path]
	}, func(p string, data []byte) error {
		switch {
		case isRoot(p):
			read.root[p] = data
		case evaluatedTests[p]:
			read.tests[p] = data
		default:
			for _, m := range goprofile.Markers(map[string][]byte{p: data}) {
				read.markers = append(read.markers, markerRow{Kind: m.Kind, Path: m.Path, Line: m.Line})
			}
		}
		return nil
	})
	slices.SortStableFunc(read.markers, func(x, y markerRow) int { return cmp.Or(cmp.Compare(x.Path, y.Path), cmp.Compare(x.Line, y.Line)) })
	return read, err
}

// readCanonical reads the test files of the canonical version that the revision expects, in the directories of the evaluated test files:
// the tests that the revision adds or changes are those that differ from the canonical's declarations of the same package.
func (a *attempt) readCanonical(ctx context.Context, evaluated []string) (map[string][]byte, error) {
	dirs := map[string]bool{}
	for _, p := range evaluated {
		if isTestFile(p) {
			dirs[path.Dir(p)] = true
		}
	}
	var tree []execution.TreeFile
	for _, f := range treeOf(a.governing, execution.FromManifest) {
		if isTestFile(f.Path) && dirs[path.Dir(f.Path)] {
			tree = append(tree, f)
		}
	}
	files := map[string][]byte{}
	err := a.read(ctx, tree, func(execution.TreeFile) bool { return true }, collect(files))
	return files, err
}

// differing is the non-test Go files of the head that the base lacks or has with other bytes: the files whose lines the markers flag.
func differing(head, base artifact.Manifest) map[string]bool {
	baseDigests := map[string]artifact.Digest{}
	for _, e := range base.Entries() {
		baseDigests[e.Path] = e.Content
	}
	out := map[string]bool{}
	for _, e := range head.Entries() {
		if strings.HasSuffix(e.Path, ".go") && !isTestFile(e.Path) {
			if d, ok := baseDigests[e.Path]; !ok || d != e.Content {
				out[e.Path] = true
			}
		}
	}
	return out
}

// receiptRows names each test that the revision adds or changes: the runnable ones, which have no observation yet, and the ones that the
// sandbox cannot run, with their reason. A test of the changed lists that no declaration of the head knows is a broken invariant.
func receiptRows(added, changed []goprofile.TestID, declared goprofile.Declared) (rows []testRow, runnable []goprofile.TestID, err error) {
	type key struct{ pkg, name string }
	runs := map[key]goprofile.TestID{}
	for _, id := range declared.Tests {
		runs[key{id.Package, id.Name}] = id
	}
	excluded := map[key]string{}
	for _, x := range declared.Excluded {
		excluded[key{x.Package, x.Name}] = x.Reason
	}
	rows = []testRow{}
	for kind, ids := range map[string][]goprofile.TestID{"added": added, "changed": changed} {
		for _, id := range ids {
			k := key{id.Package, id.Name}
			row := testRow{Package: id.Package, Name: id.Name, File: id.File, Kind: kind}
			if reason, ok := excluded[k]; ok {
				row.Excluded = reason
			} else if run, ok := runs[k]; ok {
				row.File = run.File
				runnable = append(runnable, run)
			} else {
				return nil, nil, fmt.Errorf("the test %s of %s changed, and no declaration of the head has it", id.Name, id.Package)
			}
			rows = append(rows, row)
		}
	}
	sortRows(rows)
	slices.SortFunc(runnable, func(x, y goprofile.TestID) int {
		return cmp.Or(cmp.Compare(x.Package, y.Package), cmp.Compare(x.Name, y.Name))
	})
	return rows, runnable, nil
}

// union is the sorted paths of both lists, each once.
func union(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}
