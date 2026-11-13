package scope_test

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// FuzzParseDeclaration feeds hostile .suiteward.yml bytes to ParseDeclaration. Whatever it accepts must satisfy the
// documented schema, checked against the YAML nodes directly: one document, a mapping with only the keys version
// (the integer 1), include, exclude, runner and execution, each key once, each list holding distinct strings that are
// patterns, the execution mapping holding only the keys profile, profiles, canonical and receipt (profile and profiles never
// together) with values from their fixed lists or entries that satisfy the list form (canonical and receipt only beside a
// running entry), and Execution, Parts, ListForm and Reductions agreeing with those values.
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
		"version: 1\nexecution: {canonical: required}\n", "version: 1\nexecution: {receipt: off}\n",
		"version: 1\nexecution: {canonical: required, profile: go}\n", "version: 1\nexecution: {}\n",
		"version: 1\nexecution: {profile: go, extra: x}\n", "version: 1\nexecution: {profile: rust}\n",
		"version: 1\nexecution: {profile: go, canonical: before-approval}\n", "version: 1\nexecution: ~\n",
		"version: 1\nexecution:\n", "version: 1\nexecution: {profile: &p go, receipt: *p}\n",
		"version: 1\nexecution:\n  <<: {profile: go}\n", "version: 1\nexecution: {profile: 1}\n",
		"version: 1\nexecution: {profile: go, profile: go}\n", "version: 1\nexecution: {profile: go}\nexecution: {profile: go}\n",
		"version: 1\ninclude: [src/]\nexecution: {profile: go, receipt: required}\nrunner: [Makefile]\n",
		"version: 1\nexecution:\n  profiles:\n    - profile: go\n    - profile: go\n      folder: core\n      run: false\n  canonical: required\n",
		"version: 1\nexecution: {profiles: [{profile: go, folder: tools/gen}, {profile: go, folder: \"+lib\"}, {profile: go}]}\n",
		"version: 1\nexecution: {profiles: [{profile: go, run: false}]}\n", "version: 1\nexecution: {profiles: [{profile: go, run: false}], canonical: off}\n",
		"version: 1\nexecution: {profile: go, profiles: [{profile: go}]}\n", "version: 1\nexecution: {profiles: [{profile: go}], profiles: [{profile: go}]}\n",
		"version: 1\nexecution: {profiles: [{profile: go, folder: core}, {profile: go, folder: core}]}\n",
		"version: 1\nexecution: {profiles: [{profile: go}, {profile: go}]}\n", "version: 1\nexecution: {profiles: [{profile: go, folder: .}]}\n",
		"version: 1\nexecution: {profiles: [{profile: go, folder: \"a\\u200Bb\"}]}\n", "version: 1\nexecution: {profiles: [{profile: go, folder: a//b}]}\n",
		"version: 1\nexecution: {profiles: [{profile: go, run: yes}]}\n", "version: 1\nexecution: {profiles: [{profile: go, run: !!bool yes}]}\n",
		"version: 1\nexecution: {profiles: [{profile: cpp}]}\n", "version: 1\nexecution: {profiles: [{profile: go, extra: x}]}\n",
		"version: 1\nexecution: {profiles: []}\n", "version: 1\nexecution: {profiles: ~}\n", "version: 1\nexecution: {profiles: [~]}\n",
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
		oracle := checkSchema(t, raw)
		for name, list := range map[string][]scope.Pattern{"include": d.Include(), "exclude": d.Exclude(), "runner": d.Runner()} {
			seen := map[string]bool{}
			for _, p := range list {
				if _, perr := scope.ParsePattern(p.String()); perr != nil || seen[p.String()] || p.IsZero() {
					t.Fatalf("%s of %q holds %q: %v (duplicate, zero or not a pattern)", name, raw, p.String(), perr)
				}
				seen[p.String()] = true
			}
		}
		checkExecution(t, raw, d, oracle)
		checkAddPatterns(t, raw, d)
	})
}

// checkSchema checks the accepted raw against the schema and returns what its execution mapping must read as.
func checkSchema(t *testing.T, raw []byte) executionOracle {
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
	return executionOracle{}
}

// executionOracle is what the execution mapping of an accepted declaration must read as, read from the YAML nodes alone.
type executionOracle struct {
	values  map[string]string // profile, canonical and receipt, as written
	list    bool              // the list form
	running []scope.Part      // the entries that run, root first, then by folder, then by profile
}

// executionValues checks the execution node against the schema, independently of ParseDeclaration, and returns what it must read as.
func executionValues(t *testing.T, raw []byte, node *yaml.Node) executionOracle {
	t.Helper()
	fixed := map[string][]string{
		"profile":   {"go"},
		"canonical": {"off", "advisory", "required"},
		"receipt":   {"off", "advisory", "before-approval", "required"},
	}
	if node.Kind != yaml.MappingNode {
		t.Fatalf("accepted %q whose execution is not a mapping", raw)
	}
	oracle := executionOracle{values: map[string]string{}}
	seen := map[string]bool{}
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if seen[key.Value] || key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			t.Fatalf("accepted %q with the execution key %q (duplicate or not a string)", raw, key.Value)
		}
		seen[key.Value] = true
		if key.Value == "profiles" {
			oracle.list, oracle.running = true, profilesRunning(t, raw, value)
			continue
		}
		if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" || !slices.Contains(fixed[key.Value], value.Value) {
			t.Fatalf("accepted %q with the execution entry %q: %q (unknown key or value, or not strings)", raw, key.Value, value.Value)
		}
		oracle.values[key.Value] = value.Value
	}
	if _, scalar := oracle.values["profile"]; scalar {
		if oracle.list {
			t.Fatalf("accepted %q with profile and profiles together", raw)
		}
		oracle.running = []scope.Part{{Profile: "go", Folder: "."}}
	}
	if len(oracle.running) == 0 && (oracle.values["canonical"] != "" || oracle.values["receipt"] != "") {
		t.Fatalf("accepted %q whose execution has levels but no running entry", raw)
	}
	return oracle
}

// profilesRunning checks the list form against the schema and returns its running entries in the order of the contract.
func profilesRunning(t *testing.T, raw []byte, list *yaml.Node) []scope.Part {
	t.Helper()
	if list.Kind != yaml.SequenceNode || len(list.Content) < 1 || len(list.Content) > 8 {
		t.Fatalf("accepted %q whose profiles are not 1 to 8 entries in a sequence", raw)
	}
	var running []scope.Part
	pairs := map[scope.Part]bool{}
	for _, item := range list.Content {
		if item.Kind != yaml.MappingNode {
			t.Fatalf("accepted %q with a profiles entry that is not a mapping", raw)
		}
		part, run, keys := scope.Part{Folder: "."}, true, map[string]bool{}
		for i := 0; i < len(item.Content); i += 2 {
			key, value := item.Content[i], item.Content[i+1]
			if keys[key.Value] || key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" || value.Kind != yaml.ScalarNode {
				t.Fatalf("accepted %q with the profiles entry key %q (duplicate, not a string, or its value is not a scalar)", raw, key.Value)
			}
			keys[key.Value] = true
			switch isStr := value.ShortTag() == "!!str"; key.Value {
			case "profile":
				if !isStr || !slices.Contains(scope.ProfileIDs(), value.Value) {
					t.Fatalf("accepted %q with the profile %q, which is not a string of the closed set", raw, value.Value)
				}
				part.Profile = value.Value
			case "folder":
				if !isStr || !cleanFolder(value.Value) {
					t.Fatalf("accepted %q with the folder %q, which is not a clean relative path", raw, value.Value)
				}
				part.Folder = value.Value
			case "run":
				if value.ShortTag() != "!!bool" || !slices.Contains([]string{"true", "True", "TRUE", "false", "False", "FALSE"}, value.Value) {
					t.Fatalf("accepted %q with run %q, which is not a YAML boolean", raw, value.Value)
				}
				run = strings.EqualFold(value.Value, "true")
			default:
				t.Fatalf("accepted %q with the unknown profiles entry key %q", raw, key.Value)
			}
		}
		if part.Profile == "" || pairs[part] {
			t.Fatalf("accepted %q with an entry without a profile or a pair (%s, %s) twice", raw, part.Profile, part.Folder)
		}
		pairs[part] = true
		if run {
			running = append(running, part)
		}
	}
	rootFirst := func(p scope.Part) string { // no folder is empty, so the root's key sorts first
		if p.Folder == "." {
			return ""
		}
		return p.Folder
	}
	slices.SortFunc(running, func(a, b scope.Part) int {
		return cmp.Or(strings.Compare(rootFirst(a), rootFirst(b)), strings.Compare(a.Profile, b.Profile))
	})
	return running
}

// cleanFolder is the rule for a folder of the contract: a clean relative path of at most 512 bytes, not the root's spelling, with
// no control or format character.
func cleanFolder(s string) bool {
	if s == "" || s == "." || len(s) > 512 || !utf8.ValidString(s) || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") {
		return false
	}
	for _, segment := range strings.Split(s, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) < 0
}

// checkExecution compares Execution, Parts and ListForm with what the execution mapping reads as (an empty mapping, or entries
// that all run no profile, is no policy), and checks that Reductions does not call an added or an unchanged policy a reduction
// and reports the removal of one level by level.
func checkExecution(t *testing.T, raw []byte, d scope.Declaration, oracle executionOracle) {
	t.Helper()
	got, ok := d.Execution()
	want, wantOK := scope.Execution{}, false
	if len(oracle.running) > 0 {
		var ids []string
		for _, part := range oracle.running {
			ids = append(ids, part.Profile)
		}
		slices.Sort(ids)
		want, wantOK = scope.Execution{Profile: strings.Join(slices.Compact(ids), "-"), Canonical: cmp.Or(oracle.values["canonical"], "advisory"), Receipt: cmp.Or(oracle.values["receipt"], "advisory")}, true
	}
	if got != want || ok != wantOK {
		t.Fatalf("Execution() of %q = %+v, %v, want %+v, %v", raw, got, ok, want, wantOK)
	}
	if parts := d.Parts(); !slices.Equal(parts, oracle.running) || len(oracle.running) == 0 && parts != nil {
		t.Fatalf("Parts() of %q = %v, want %v (nil when nothing runs)", raw, parts, oracle.running)
	}
	if d.ListForm() != oracle.list {
		t.Fatalf("ListForm() of %q = %v, want %v", raw, d.ListForm(), oracle.list)
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
