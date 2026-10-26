package scope_test

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// FuzzParseDeclaration feeds hostile .suiteward.yml bytes to ParseDeclaration. Whatever it accepts must satisfy the
// documented schema, checked against the YAML nodes directly: one document, a mapping with only the keys version
// (the integer 1), include, exclude, runner and execution, each key once, each list holding distinct strings that are
// patterns, the execution mapping holding only the keys profile, canonical and receipt with values from their fixed lists,
// and Execution and Reductions agreeing with those values.
// An extension beyond the brief: AddPatterns, which the scope notices use on the same bytes, must keep every
// pattern and stay valid.
func FuzzParseDeclaration(f *testing.F) {
	for _, seed := range []string{
		"version: 1\n", "version: 1\ninclude: [src/**, Makefile]\nexclude: [src/gen/]\nrunner: [scripts/test.sh]\n",
		"# only a comment\n", "---\n", "...\n", "version: 1\ninclude: [a, a]\n",
		"version: !!str 1\n", "&a version: 1\ninclude: *a\n",
		"version: 1\ninclude: ['!x']\n", "version: 1\nrunner: [~]\n",
		"version: 1\nexecution:\n  profile: go\n  canonical: required\n  receipt: before-approval\n",
		"version: 1\nexecution: {profile: go}\n", "version: 1\nexecution: {profile: go, canonical: off, receipt: off}\n",
		"version: 1\nexecution: {canonical: required}\n", "version: 1\nexecution: {}\n",
		"version: 1\nexecution: {profile: go, extra: x}\n", "version: 1\nexecution: {profile: rust}\n",
		"version: 1\nexecution: {profile: go, canonical: before-approval}\n", "version: 1\nexecution: ~\n",
		"version: 1\nexecution:\n", "version: 1\nexecution: {profile: &p go, receipt: *p}\n",
		"version: 1\nexecution:\n  <<: {profile: go}\n", "version: 1\nexecution: {profile: 1}\n",
		"version: 1\nexecution: {profile: go, profile: go}\n", "version: 1\nexecution: {profile: go}\nexecution: {profile: go}\n",
		"version: 1\ninclude: [src/]\nexecution: {profile: go, receipt: required}\nrunner: [Makefile]\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		d, err := scope.ParseDeclaration(raw)
		if err != nil {
			if !errors.Is(err, scope.ErrInvalidDeclaration) {
				t.Fatalf("ParseDeclaration(%q) = %v, want ErrInvalidDeclaration", raw, err)
			}
			return
		}
		if !bytes.Equal(d.Raw(), raw) || !d.Present() {
			t.Fatalf("an accepted declaration does not keep its bytes: %q", raw)
		}
		execution := checkSchema(t, raw)
		for name, list := range map[string][]scope.Pattern{"include": d.Include(), "exclude": d.Exclude(), "runner": d.Runner()} {
			seen := map[string]bool{}
			for _, p := range list {
				if _, perr := scope.ParsePattern(p.String()); perr != nil || seen[p.String()] || p.IsZero() {
					t.Fatalf("%s of %q holds %q: %v (duplicate, zero or not a pattern)", name, raw, p.String(), perr)
				}
				seen[p.String()] = true
			}
		}
		checkExecution(t, raw, d, execution)
		checkAddPatterns(t, raw, d)
	})
}

// checkSchema checks the accepted raw against the schema and returns the values of its execution mapping.
func checkSchema(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var root, extra yaml.Node
	if err := decoder.Decode(&root); err != nil {
		t.Fatalf("accepted %q but its first document does not decode: %v", raw, err)
	}
	if decoder.Decode(&extra) == nil {
		t.Fatalf("accepted %q with more than one document", raw)
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		t.Fatalf("accepted %q whose document is not a mapping", raw)
	}
	keys := map[string]*yaml.Node{}
	for i := 0; i < len(top.Content); i += 2 {
		key := top.Content[i]
		if _, dup := keys[key.Value]; dup || key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" || !slices.Contains([]string{"version", "include", "exclude", "runner", "execution"}, key.Value) {
			t.Fatalf("accepted %q with the key %q (duplicate or not allowed)", raw, key.Value)
		}
		keys[key.Value] = top.Content[i+1]
	}
	if v := keys["version"]; v == nil || v.Kind != yaml.ScalarNode || v.ShortTag() != "!!int" || v.Value != "1" && !intValueIsOne(v) {
		t.Fatalf("accepted %q whose version is not the integer 1", raw)
	}
	for _, name := range []string{"include", "exclude", "runner"} {
		list, ok := keys[name]
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for _, item := range list.Content {
			if list.Kind != yaml.SequenceNode || item.Kind != yaml.ScalarNode || item.ShortTag() != "!!str" || seen[item.Value] {
				t.Fatalf("accepted %q whose %s is not a sequence of distinct strings", raw, name)
			}
			seen[item.Value] = true
		}
	}
	if execution, ok := keys["execution"]; ok {
		return executionValues(t, raw, execution)
	}
	return nil
}

// executionValues checks the execution node against the schema, independently of ParseDeclaration, and returns its values.
func executionValues(t *testing.T, raw []byte, node *yaml.Node) map[string]string {
	t.Helper()
	fixed := map[string][]string{
		"profile":   {"go"},
		"canonical": {"off", "advisory", "required"},
		"receipt":   {"off", "advisory", "before-approval", "required"},
	}
	if node.Kind != yaml.MappingNode {
		t.Fatalf("accepted %q whose execution is not a mapping", raw)
	}
	values := map[string]string{}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		_, dup := values[key.Value]
		if dup || key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" || value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" || !slices.Contains(fixed[key.Value], value.Value) {
			t.Fatalf("accepted %q with the execution entry %q: %q (duplicate, unknown key or value, or not strings)", raw, key.Value, value.Value)
		}
		values[key.Value] = value.Value
	}
	return values
}

// checkExecution compares Execution with the values of the execution mapping (no profile runs nothing), and checks that
// Reductions does not call an added or an unchanged policy a reduction and reports the removal of one level by level.
func checkExecution(t *testing.T, raw []byte, d scope.Declaration, values map[string]string) {
	t.Helper()
	got, ok := d.Execution()
	want, wantOK := scope.Execution{}, false
	if profile := values["profile"]; profile != "" {
		want, wantOK = scope.Execution{Profile: profile, Canonical: cmp.Or(values["canonical"], "advisory"), Receipt: cmp.Or(values["receipt"], "advisory")}, true
	}
	if got != want || ok != wantOK {
		t.Fatalf("Execution() of %q = %+v, %v, want %+v, %v", raw, got, ok, want, wantOK)
	}
	s, none := scope.NewScope(d), scope.NewScope(scope.NoDeclaration())
	if reductions := scope.Reductions(s, s); len(reductions) != 0 {
		t.Fatalf("Reductions of %q against itself = %q", raw, reductions)
	}
	lowered := func(reductions []string) (n int) {
		for _, entry := range reductions {
			if strings.HasPrefix(entry, "execution ") {
				n++
			}
		}
		return n
	}
	wantLowered := 0
	for _, level := range []string{want.Canonical, want.Receipt} {
		if wantOK && level != "off" {
			wantLowered++
		}
	}
	if n := lowered(scope.Reductions(none, s)); n != 0 {
		t.Fatalf("adding the policy of %q is reported as %d reductions", raw, n)
	}
	if n := lowered(scope.Reductions(s, none)); n != wantLowered {
		t.Fatalf("removing the policy of %q is reported as %d reductions, want %d", raw, n, wantLowered)
	}
}

func intValueIsOne(n *yaml.Node) bool {
	var v int
	return n.Decode(&v) == nil && v == 1
}

func checkAddPatterns(t *testing.T, raw []byte, d scope.Declaration) {
	t.Helper()
	same, err := scope.AddPatterns(raw, nil, nil)
	if err != nil || !bytes.Equal(same, raw) {
		t.Fatalf("AddPatterns(%q, nothing) = %q, %v; want the bytes unchanged", raw, same, err)
	}
	added, err := scope.AddPatterns(raw, []string{"fuzz/added/"}, []string{"fuzz-runner"})
	if err != nil {
		t.Fatalf("AddPatterns(%q) = %v", raw, err)
	}
	after, err := scope.ParseDeclaration(added)
	if err != nil {
		t.Fatalf("AddPatterns(%q) wrote an invalid declaration %q: %v", raw, added, err)
	}
	for name, pair := range map[string][2][]scope.Pattern{"include": {d.Include(), after.Include()}, "exclude": {d.Exclude(), after.Exclude()}, "runner": {d.Runner(), after.Runner()}} {
		before, now := patternStrings(pair[0]), patternStrings(pair[1])
		if len(now) < len(before) || !slices.Equal(now[:len(before)], before) {
			t.Fatalf("AddPatterns changed the %s list of %q: %q became %q", name, raw, before, now)
		}
	}
}

// FuzzPatternMatch compiles a hostile pattern and matches it against a hostile path. Errors keep their categories,
// matching is deterministic and reaches everything beneath a matched path, and MayMatchBelow agrees with Match for
// every directory above a matched path.
func FuzzPatternMatch(f *testing.F) {
	for _, seed := range [][2]string{
		{"docs/", "docs/a.md"}, {"tests/**", "tests/a/b/c.go"}, {"a/**/b", "a/x/y/b"}, {"**/fixtures", "x/y/fixtures/a.go"},
		{"*.go", "a.go"}, {"/vendor", "pkg/vendor/a.go"}, {"**/**/**/**/a", "a/a/a/a/a/a"},
		{"a[b", "a"}, {"a**", "a"}, {"*a*a*a*a*a*a*b", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, pattern, path string) {
		p, err := scope.ParsePattern(pattern)
		if err != nil {
			if !errors.Is(err, scope.ErrInvalidPattern) || !p.IsZero() {
				t.Fatalf("ParsePattern(%q) = %+v, %v; want a zero pattern and ErrInvalidPattern", pattern, p, err)
			}
			return
		}
		if p.String() != pattern || p.IsZero() {
			t.Fatalf("ParsePattern(%q) returned %q", pattern, p.String())
		}
		checkMatch(t, p, pattern, path)
	})
}

func checkMatch(t *testing.T, p scope.Pattern, pattern, path string) {
	t.Helper()
	matched, err := p.Match(path)
	again, errAgain := p.Match(path)
	if matched != again || fmt.Sprint(err) != fmt.Sprint(errAgain) {
		t.Fatalf("Match(%q, %q) is not deterministic", pattern, path)
	}
	if !artifact.ValidPath(path) {
		if !errors.Is(err, artifact.ErrInvalidPath) || matched {
			t.Fatalf("Match(%q, %q) = %v, %v; want false and ErrInvalidPath", pattern, path, matched, err)
		}
		if _, berr := p.MayMatchBelow(path); !errors.Is(berr, artifact.ErrInvalidPath) {
			t.Fatalf("MayMatchBelow(%q, %q) = %v; want ErrInvalidPath", pattern, path, berr)
		}
		return
	}
	if err != nil {
		t.Fatalf("Match(%q, %q) failed on a valid path: %v", pattern, path, err)
	}
	if !matched {
		return
	}
	if below, err := p.Match(path + "/x"); err != nil || !below {
		t.Fatalf("%q matches %q but not what lies beneath it: %v, %v", pattern, path, below, err)
	}
	for i := range len(path) {
		if path[i] != '/' {
			continue
		}
		if may, err := p.MayMatchBelow(path[:i]); err != nil || !may {
			t.Fatalf("%q matches %q but MayMatchBelow(%q) = %v, %v", pattern, path, path[:i], may, err)
		}
	}
}
