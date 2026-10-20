package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The scope notice (docs/contracts/runtime.md, "Scope discovery (M1.9)"): after V1 the checks job tells the owner which
// test-like paths a pull request adds outside the protected scope. It is evidence, so the check never changes.

const (
	cjBase    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" // the base commit of the pull request under test
	cjDecl    = "version: 1\ninclude: [/tests/**]\n"       // the declaration of bootFiles, the canonical of the default world
	cjUtilTst = "pkg/util_test.go"
)

func scopeDigest(paths ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(paths, "\n")))
	return hex.EncodeToString(sum[:])
}

// withFiles is files plus an empty-looking file for each extra path.
func withFiles(files map[string]string, extra ...string) map[string]string {
	grown := maps.Clone(files)
	for _, path := range extra {
		grown[path] = "x\n"
	}
	return grown
}

func (w *cjJobWorld) scopeMessages(prefix string) []governance.OutboxMessage {
	var found []governance.OutboxMessage
	for _, m := range w.suites.messages {
		if strings.HasPrefix(m.Key, prefix) {
			found = append(found, m)
		}
	}
	return found
}

func (w *cjJobWorld) scopeKeys(prefix string) []string {
	var keys []string
	for _, m := range w.scopeMessages(prefix) {
		keys = append(keys, m.Key)
	}
	return keys
}

// scopePull opens pull 5 at head, whose tree is headFiles, over the base commit cjBase, whose tree is baseFiles, and checks once.
func (w *cjJobWorld) scopePull(headFiles, baseFiles map[string]string) {
	w.t.Helper()
	w.bases = map[int]string{5: cjBase}
	w.tree(cjHead2, headFiles)
	w.tree(cjBase, baseFiles)
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.mustCheck()
}

func (w *cjJobWorld) wantNoScopeMessages() {
	w.t.Helper()
	if keys := w.scopeKeys("scope-"); len(keys) != 0 {
		w.t.Fatalf("scope messages %v, want none", keys)
	}
}

func TestChecksWriteOneScopeNoticeForANewTestFileOutsideTheScope(t *testing.T) {
	w := newCJWorld(t, true)

	w.scopePull(withFiles(bootFiles("v1"), cjUtilTst), bootFiles("v1"))

	wantKey := "scope-notice:1001:5:" + scopeDigest(cjUtilTst)
	if keys := w.scopeKeys("scope-"); !slices.Equal(keys, []string{wantKey}) {
		t.Fatalf("scope messages %v, want exactly %s\nlogs: %s", keys, wantKey, w.logs)
	}
	message := w.scopeMessages("scope-")[0]
	if message.Kind != githubCommentKind {
		t.Fatalf("kind %q, want %q", message.Kind, githubCommentKind)
	}
	declaration, err := scope.AddPatterns([]byte(cjDecl), []string{"*_test.go"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(message.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"repositoryId": float64(bootRepoID), "repo": bootRepo, "pull": float64(5), "head": cjHead2, "branch": "feature",
		"paths": []any{cjUtilTst}, "pathsTotal": float64(1), "declaration": string(declaration)}
	if !reflect.DeepEqual(payload, want) {
		t.Fatalf("payload %v, want %v", payload, want)
	}
	// Evidence only: the check is the unchanged success it was.
	w.want(5, checks.Success, "unchanged", 1)
}

// A head test-like path that is no valid path makes the check an invalid-source failure; the scope notice must treat that as a fact about the
// commit: no failed step, nothing written, and no new attempt on the next runs.
func TestChecksDecideOnceForAHeadTestPathThatIsNoValidPath(t *testing.T) {
	for _, path := range []string{"te:sts/test_a.py", `te\sts/test_a.py`} {
		t.Run(path, func(t *testing.T) {
			w := newCJWorld(t, true)

			w.scopePull(withFiles(bootFiles("v1"), path), bootFiles("v1"))

			w.want(5, checks.Failure, "invalid-source", 1)
			w.wantNoScopeMessages()
			head := w.reader.reads[cjHead2]
			w.mustCheck()
			w.mustCheck()
			if w.reader.reads[cjHead2] != head {
				t.Fatalf("the head was read %d times, want %d: the decision was not remembered\nlogs: %s", w.reader.reads[cjHead2], head, w.logs)
			}
		})
	}
}

func TestChecksScopeNoticeLeavesTheCheckAsItWas(t *testing.T) {
	with := newCJWorld(t, true)
	with.scopePull(withFiles(bootFiles("v1"), cjUtilTst), bootFiles("v1"))
	without := newCJWorld(t, true)
	without.scopePull(bootFiles("v1"), bootFiles("v1"))

	if len(with.scopeMessages("scope-notice:")) != 1 || len(without.scopeMessages("scope-")) != 0 {
		t.Fatalf("scope messages %v with the file and %v without it, want one and none", with.scopeKeys("scope-"), without.scopeKeys("scope-"))
	}
	recordWith, _ := with.record(5)
	recordWithout, _ := without.record(5)
	if !reflect.DeepEqual(recordWith, recordWithout) {
		t.Fatalf("check record %+v with the notice, want %+v as without it", recordWith, recordWithout)
	}
	if len(with.ledger.messages) != len(without.ledger.messages) {
		t.Fatalf("%d check messages with the notice, want %d", len(with.ledger.messages), len(without.ledger.messages))
	}
	for i := range with.ledger.messages {
		if string(with.ledger.messages[i].Payload) != string(without.ledger.messages[i].Payload) {
			t.Fatalf("check payload %s with the notice, want %s", with.ledger.messages[i].Payload, without.ledger.messages[i].Payload)
		}
	}
}

func TestChecksReportARunnerFileOutsideTheScope(t *testing.T) {
	w := newCJWorld(t, true)

	w.scopePull(withFiles(bootFiles("v1"), "conftest.py"), bootFiles("v1"))

	wantKey := "scope-notice:1001:5:" + scopeDigest("conftest.py")
	if keys := w.scopeKeys("scope-"); !slices.Equal(keys, []string{wantKey}) {
		t.Fatalf("scope messages %v, want exactly %s\nlogs: %s", keys, wantKey, w.logs)
	}
	var payload struct {
		Declaration string `json:"declaration"`
	}
	if err := json.Unmarshal(w.scopeMessages("scope-")[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	declaration, err := scope.ParseDeclaration([]byte(payload.Declaration))
	if err != nil {
		t.Fatalf("declaration %q: %v", payload.Declaration, err)
	}
	var runner, include []string
	for _, p := range declaration.Runner() {
		runner = append(runner, p.String())
	}
	for _, p := range declaration.Include() {
		include = append(include, p.String())
	}
	if !slices.Equal(runner, []string{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini", "pytest.toml"}) || !slices.Equal(include, []string{"/tests/**"}) {
		t.Fatalf("declaration %q, want the canonical include plus the pytest runner group", payload.Declaration)
	}
}

func TestChecksWriteNoScopeNoticeWhenThereIsNothingToReport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		world     func(t *testing.T) *cjJobWorld
		head      map[string]string
		baseFiles map[string]string
		baseReads int // the reads of the base tree the case allows
	}{
		{"every test-like path is protected", func(t *testing.T) *cjJobWorld { return newCJWorld(t, true) }, bootFiles("v1"), bootFiles("v1"), 0},
		{"the paths are all present in the base", func(t *testing.T) *cjJobWorld { return newCJWorld(t, true) }, withFiles(bootFiles("v1"), cjUtilTst, "conftest.py"),
			withFiles(bootFiles("v1"), cjUtilTst, "conftest.py"), 1},
		{"the only candidate is excluded by the governing scope", func(t *testing.T) *cjJobWorld {
			return newCJWorldOf(t, withDeclaration(bootFiles("v1"), "version: 1\ninclude: [/tests/**]\nexclude: [gen_test.go]\n"))
		}, withDeclaration(withFiles(bootFiles("v1"), "pkg/gen_test.go"), "version: 1\ninclude: [/tests/**]\nexclude: [gen_test.go]\n"),
			withDeclaration(bootFiles("v1"), "version: 1\ninclude: [/tests/**]\nexclude: [gen_test.go]\n"), 0},
		{"the head's own valid declaration already covers the path", func(t *testing.T) *cjJobWorld { return newCJWorld(t, true) },
			withDeclaration(withFiles(bootFiles("v1"), cjUtilTst), "version: 1\ninclude: [/tests/**, /pkg/**]\n"), bootFiles("v1"), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := tc.world(t)

			w.scopePull(tc.head, tc.baseFiles)

			w.wantNoScopeMessages()
			if reads := w.reader.reads[cjBase]; reads != tc.baseReads {
				t.Fatalf("the base tree was read %d times, want %d", reads, tc.baseReads)
			}
		})
	}
}

func withDeclaration(files map[string]string, declaration string) map[string]string {
	grown := maps.Clone(files)
	grown[".suiteward.yml"] = declaration
	return grown
}

func TestChecksWriteNoScopeNoticeForAForkAClosedPullRequestOrASuiteWithoutACanonical(t *testing.T) {
	t.Run("a fork", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.headRepo = map[int]string{5: "evil/widgets"}

		w.scopePull(withFiles(bootFiles("v1"), cjUtilTst), bootFiles("v1"))

		w.wantNoScopeMessages()
		if w.reader.reads[cjBase] != 0 || w.reader.reads[cjHead2] != 0 {
			t.Fatalf("a fork's trees were read: head %d, base %d", w.reader.reads[cjHead2], w.reader.reads[cjBase])
		}
	})
	t.Run("a closed pull request", func(t *testing.T) {
		w := newCJWorld(t, true)
		w.bases = map[int]string{5: cjBase}
		w.tree(cjHead2, withFiles(bootFiles("v1"), cjUtilTst))
		w.tree(cjBase, bootFiles("v1"))
		w.sync(map[int]string{4: bootHead, 5: cjHead2})
		w.sync(map[int]string{4: bootHead}) // pull 5 is no longer open

		w.mustCheck()

		w.wantNoScopeMessages()
	})
	t.Run("a Suite without a canonical", func(t *testing.T) {
		w := newCJWorld(t, false)
		w.bases = map[int]string{5: cjBase}
		w.tree(cjHead2, withFiles(bootFiles("v1"), cjUtilTst))
		w.tree(cjBase, bootFiles("v1"))
		w.sync(map[int]string{4: bootHead, 5: cjHead2})

		w.mustCheck()

		w.wantNoScopeMessages()
	})
}

func TestChecksWriteOneScopeNoticePerSetOfAddedPaths(t *testing.T) {
	w := newCJWorld(t, true)
	w.scopePull(withFiles(bootFiles("v1"), cjUtilTst), bootFiles("v1"))
	first := "scope-notice:1001:5:" + scopeDigest(cjUtilTst)

	// A later head with the same added path, and one more implementation change.
	w.tree(cjHead3, withFiles(bootFiles("v1"), cjUtilTst, "src/more.go"))
	w.sync(map[int]string{4: bootHead, 5: cjHead3})
	w.mustCheck()

	if keys := w.scopeKeys("scope-notice:"); !slices.Equal(keys, []string{first}) {
		t.Fatalf("notices %v after a push that adds nothing new, want only %s", keys, first)
	}
	if reads := w.reader.reads[cjBase]; reads != 1 {
		t.Fatalf("the base tree was read %d times, want once: the notice of that set was already written, so the next head needs no base read", reads)
	}

	// Another path makes another set.
	w.tree(cjHead4, withFiles(bootFiles("v1"), cjUtilTst, "pkg/other_test.go"))
	w.sync(map[int]string{4: bootHead, 5: cjHead4})
	w.mustCheck()

	second := "scope-notice:1001:5:" + scopeDigest("pkg/other_test.go", cjUtilTst)
	if keys := w.scopeKeys("scope-notice:"); !slices.Equal(keys, []string{first, second}) {
		t.Fatalf("notices %v, want %s then %s", keys, first, second)
	}
}

func TestChecksReadTheBaseTreeOnceForEveryHeadAndNeverForATickThatChangesNothing(t *testing.T) {
	w := newCJWorld(t, true)
	w.scopePull(withFiles(bootFiles("v1"), cjUtilTst), bootFiles("v1"))
	head, base := w.reader.reads[cjHead2], w.reader.reads[cjBase]
	if base != 1 {
		t.Fatalf("the base tree was read %d times for the first head, want once", base)
	}

	w.mustCheck()
	w.mustCheck()

	if w.reader.reads[cjHead2] != head || w.reader.reads[cjBase] != base {
		t.Fatalf("reads of the head and the base went from %d and %d to %d and %d on ticks that change nothing", head, base, w.reader.reads[cjHead2], w.reader.reads[cjBase])
	}

	// An unrelated Suite write moves the revision and makes this pull request assess again, which must not read the base again.
	w.tree(cjHead3, bootFiles("v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2, 6: cjHead3})
	w.mustCheck()
	if w.reader.reads[cjBase] != base {
		t.Fatalf("the base tree was read %d times after another pull request moved the Suite, want still %d", w.reader.reads[cjBase], base)
	}
}

// A base commit that cannot be read is a fact about the commit, not a failed read: the decision is made once, not every run.
func TestChecksDecideOnceForABaseCommitThatCanNeverBeRead(t *testing.T) {
	w := newCJWorld(t, true)
	w.reader.repo(bootRepo).treeErr[cjBase] = fmt.Errorf("%w: not a commit SHA", github.ErrInvalidArgument)

	w.scopePull(withFiles(bootFiles("v1"), cjUtilTst), bootFiles("v1"))

	w.wantNoScopeMessages()
	head, base := w.reader.reads[cjHead2], w.reader.reads[cjBase]
	w.mustCheck()
	w.mustCheck()
	if w.reader.reads[cjHead2] != head || w.reader.reads[cjBase] != base {
		t.Fatalf("reads of the head and the base went from %d and %d to %d and %d: the decision was not remembered", head, base, w.reader.reads[cjHead2], w.reader.reads[cjBase])
	}
}

func TestChecksOfferThePatternsAloneForADeclarationTooLargeToShow(t *testing.T) {
	var declaration strings.Builder
	declaration.WriteString("version: 1\ninclude:\n  - /tests/**\n")
	for i := range 700 {
		fmt.Fprintf(&declaration, "  - /dir%03d/**\n", i)
	}
	files := withDeclaration(bootFiles("v1"), declaration.String())
	w := newCJWorldOf(t, files)

	w.scopePull(withFiles(files, cjUtilTst), files)

	stored, text := w.postedScopeNotice()
	if _, found := stored["declaration"]; found || !reflect.DeepEqual(stored["addInclude"], []any{"*_test.go"}) || !strings.Contains(text, "```\n*_test.go\n```") {
		t.Fatalf("payload %v and text\n%s\nwant the pattern alone", stored, text)
	}
	w.want(5, checks.Success, "unchanged", 1)
}

// discoverHead is what the suggest command will call: the uncovered paths of a head with no base filter, and the declaration for them.
func TestDiscoverHeadReportsTheUncoveredPathsAndTheDeclarationForThem(t *testing.T) {
	w := newCJWorld(t, true)
	head := &bootTree{files: withFiles(bootFiles("v1"), cjUtilTst, "conftest.py", "package.json", "tests/b_test.go")}

	found, err := discoverHead(t.Context(), w.suites, nopContents{}, w.project(), head)

	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"conftest.py", cjUtilTst}; !slices.Equal(found.Paths, want) {
		t.Fatalf("paths %v, want %v (tests/b_test.go is protected)", found.Paths, want)
	}
	if !slices.Equal(found.Mixed, []string{"package.json"}) {
		t.Fatalf("mixed %v, want package.json", found.Mixed)
	}
	suggestion, declaration, err := found.declaration(found.Paths)
	if err != nil {
		t.Fatal(err)
	}
	want, err := scope.AddPatterns([]byte(cjDecl), []string{"*_test.go"}, []string{".pytest.ini", ".pytest.toml", "conftest.py", "pytest.ini", "pytest.toml"})
	if err != nil {
		t.Fatal(err)
	}
	if string(declaration) != string(want) || !slices.Equal(suggestion.Include, []string{"*_test.go"}) {
		t.Fatalf("declaration %q for include %v, want %q", declaration, suggestion.Include, want)
	}
}

// logged returns the fields of the first log line whose message contains msg.
func (w *cjJobWorld) logged(msg string) map[string]any {
	w.t.Helper()
	for _, line := range strings.Split(w.logs.String(), "\n") {
		var fields map[string]any
		if json.Unmarshal([]byte(line), &fields) == nil {
			if text, _ := fields["msg"].(string); strings.Contains(text, msg) {
				return fields
			}
		}
	}
	w.t.Fatalf("no log line says %q\nlogs: %s", msg, w.logs)
	return nil
}

// A skipped notice says why. The reason is fixed text: the branch is the repository's, and the renderer's own error quotes it.
func TestChecksLogTheReasonWhenTheNoticeCannotBeRendered(t *testing.T) {
	w := newCJWorld(t, true)
	w.refs = map[int]string{5: "a..b"} // not a git ref name

	w.scopePull(withFiles(bootFiles("v1"), cjUtilTst), bootFiles("v1"))

	w.wantNoScopeMessages()
	line := w.logged("scope notice skipped: it cannot be rendered")
	if reason, _ := line["reason"].(string); reason == "" || strings.Contains(reason, "a..b") {
		t.Fatalf("log line %v, want a reason that does not quote the branch", line)
	}
	if strings.Contains(w.logs.String(), "a..b") {
		t.Fatalf("the logs quote the branch: %s", w.logs)
	}
}

// More than 20 patterns are needed: the declaration is cut at the limit, and the notice says so instead of showing a partial one.
func TestChecksSayThatTheDeclarationOfAScopeNoticeWasCut(t *testing.T) {
	runners := []string{"jest.config.js", "vitest.config.js", ".mocharc.js", "karma.conf.js", "playwright.config.ts", "cypress.config.ts", ".coveragerc", "phpunit.xml", ".rspec",
		"test_helper.rb", "dart_test.yaml", "codecov.yml", "conftest.py"} // 25 runner patterns
	w := newCJWorld(t, true)

	w.scopePull(withFiles(bootFiles("v1"), runners...), bootFiles("v1"))

	messages := w.scopeMessages("scope-notice:")
	if len(messages) != 1 {
		t.Fatalf("notices %v, want one\nlogs: %s", w.scopeKeys("scope-notice:"), w.logs)
	}
	var payload map[string]any
	if err := json.Unmarshal(messages[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["cut"] != true {
		t.Fatalf("payload %v, want cut: the 25 runner patterns do not fit the limit of 20", payload)
	}
}

func TestChecksLeaveCutOutOfAScopeNoticeThatWasNotCut(t *testing.T) {
	w := newCJWorld(t, true)

	w.scopePull(withFiles(bootFiles("v1"), cjUtilTst), bootFiles("v1"))

	var payload map[string]any
	if err := json.Unmarshal(w.scopeMessages("scope-notice:")[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["cut"]; present {
		t.Fatalf("payload %v carries cut", payload)
	}
}

// The runner names the canonical lists take no room from the limit of 20: 16 test files bring 16 include patterns and the 5 pytest names, 21 in all.
func TestChecksDoNotSayCutForRunnerNamesTheCanonicalAlreadyLists(t *testing.T) {
	canonical := "version: 1\ninclude: [/tests/**]\nrunner: [.pytest.ini, .pytest.toml, pytest.ini, pytest.toml, conftest.py]\n"
	files := []string{"a_test.go", "a.test.js", "a.spec.js", "a.cy.js", "a_spec.rb", "a_test.rb", "a_test.exs", "a_test.dart", "ATest.java", "ATests.java", "ATest.kt", "ATests.cs", "ATest.php", "test_a.py", "a_test.py", "tests.py"}
	base := withDeclaration(bootFiles("v1"), canonical)
	w := newCJWorldOf(t, base)

	w.scopePull(withFiles(base, files...), base)

	messages := w.scopeMessages("scope-notice:")
	if len(messages) != 1 {
		t.Fatalf("notices %v, want one\nlogs: %s", w.scopeKeys("scope-notice:"), w.logs)
	}
	var payload map[string]any
	if err := json.Unmarshal(messages[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, cut := payload["cut"]; cut {
		t.Fatalf("payload %v, want no cut: the 16 missing patterns fit the limit", payload)
	}
}
