package commandtext_test

import (
	"errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// FuzzParse feeds a hostile comment body to Parse. A command it reads must have the documented shape, and text
// that a code fence holds must never be read as a command.
func FuzzParse(f *testing.F) {
	for _, seed := range []string{
		"/suiteward approve P1-R2 123456", "```\n/suiteward approve P1-R2 123456\n```", "~~~\n/suiteward propose\n",
		"/SUITEWARD Approve P999999999-R1 000000", "/suiteward approve P01-R2 123456", "/suiteward skip 123456",
		"/suiteward approve P1-R2 12345\u0663", "text\n \t/suiteward\trevoke P1-R1 111111 \t\r\n",
		"\u017fuiteward approve P1-R1 123456", "````\n```\n/suiteward propose\n````",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		parsed := commandtext.Parse(body)
		checkParsed(t, body, parsed)
		if parsed.Kind != commandtext.KindNotCommand {
			if again := commandtext.Parse(body + "\n/suiteward propose"); again != parsed {
				t.Fatalf("a line after the command changed %+v to %+v for %q", parsed, again, body)
			}
		}
		for _, mark := range []byte{'`', '~'} {
			fence := strings.Repeat(string(mark), max(3, longestRun(body, mark)+1))
			for _, wrapped := range []string{fence + "\n" + body, fence + "\n" + body + "\n" + fence} {
				if got := commandtext.Parse(wrapped); got.Kind != commandtext.KindNotCommand {
					t.Fatalf("Parse read %+v inside a fence of %q around %q", got, mark, body)
				}
			}
		}
	})
}

// longestRun is the length of the longest run of c in s.
func longestRun(s string, c byte) int {
	longest, run := 0, 0
	for i := range len(s) {
		if s[i] != c {
			run = 0
			continue
		}
		run++
		longest = max(longest, run)
	}
	return longest
}

func isCode(s string) bool {
	return len(s) == 6 && strings.Trim(s, "0123456789") == ""
}

func checkParsed(t *testing.T, body string, p commandtext.Parsed) {
	t.Helper()
	switch p.Kind {
	case commandtext.KindNotCommand:
		if p.Command != (commandtext.Command{}) || p.Reason != 0 {
			t.Fatalf("not a command, yet %+v for %q", p, body)
		}
	case commandtext.KindMalformed:
		if p.Command != (commandtext.Command{}) || p.Reason < commandtext.UnknownVerb || p.Reason > commandtext.ExtraText {
			t.Fatalf("malformed with %+v for %q", p, body)
		}
	case commandtext.KindCommand:
		checkCommand(t, body, p.Command)
		if p.Reason != 0 {
			t.Fatalf("a command with a reason: %+v for %q", p, body)
		}
	default:
		t.Fatalf("unknown kind in %+v for %q", p, body)
	}
}

func checkCommand(t *testing.T, body string, c commandtext.Command) {
	t.Helper()
	switch c.Verb {
	case commandtext.Approve, commandtext.Revoke:
		ref := commandtext.Reference(c.Proposal, c.Revision) // panics outside 1..999999999
		proposal, revision, ok := commandtext.ParseReference(ref)
		if !ok || proposal != c.Proposal || revision != c.Revision || !isCode(c.Code) || !strings.Contains(body, ref) || !strings.Contains(body, c.Code) {
			t.Fatalf("command %+v does not hold its reference and 6-digit code, or they are not in %q", c, body)
		}
	case commandtext.Skip:
		if c.Proposal != 0 || c.Revision != 0 || !isCode(c.Code) || !strings.Contains(body, c.Code) {
			t.Fatalf("skip %+v is not only a 6-digit code from %q", c, body)
		}
	case commandtext.Propose, commandtext.Suggest:
		if c.Proposal != 0 || c.Revision != 0 || c.Code != "" {
			t.Fatalf("verb %d carries a reference or a code: %+v from %q", c.Verb, c, body)
		}
	default:
		t.Fatalf("unknown verb in %+v from %q", c, body)
	}
}

// FuzzParseReference checks that a reference that is accepted is exactly what Reference writes.
func FuzzParseReference(f *testing.F) {
	for _, seed := range []string{"P1-R1", "P999999999-R999999999", "P1000000000-R1", "P01-R1", "P1-R0", "p1-r1", "P1-R1 ", "P+1-R1", "P1-R-1", "P\u0661-R1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, ref string) {
		proposal, revision, ok := commandtext.ParseReference(ref)
		if !ok {
			if proposal != 0 || revision != 0 {
				t.Fatalf("ParseReference(%q) = %d, %d, false", ref, proposal, revision)
			}
			return
		}
		if proposal < 1 || revision < 1 || proposal > 999999999 || revision > 999999999 || commandtext.Reference(proposal, revision) != ref {
			t.Fatalf("ParseReference(%q) = %d, %d is not what Reference writes", ref, proposal, revision)
		}
	})
}

// FuzzValidLogin checks the login grammar against a byte-wise statement of it: 1 to 39 ASCII letters, digits, hyphens and
// underscores, the first of them a letter or digit.
func FuzzValidLogin(f *testing.F) {
	for _, seed := range []string{"octocat", "octocat_acme", "-bad", strings.Repeat("a", 39), strings.Repeat("a", 40), "a\n", "\uff41", "a b", "", "_a"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, login string) {
		want := len(login) >= 1 && len(login) <= 39 && login[0] != '-' && login[0] != '_'
		for i := 0; want && i < len(login); i++ {
			c := login[i]
			want = c == '-' || c == '_' || '0' <= c && c <= '9' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z'
		}
		if commandtext.ValidLogin(login) != want {
			t.Fatalf("ValidLogin(%q) = %v, want %v", login, !want, want)
		}
	})
}

// invisible restates, independently of production, the runes no comment may show raw: not graphic, or graphic to Unicode and drawing nothing.
func invisible(r rune) bool {
	return !strconv.IsGraphic(r) || r == 0x034f || r == 0x115f || r == 0x1160 || r == 0x17b4 || r == 0x17b5 || 0x180b <= r && r <= 0x180f ||
		r == 0x2800 || r == 0x3164 || 0xfe00 <= r && r <= 0xfe0f || r == 0xffa0 || 0xe0100 <= r && r <= 0xe01ef
}

// FuzzCanShow checks CanShow against a rune-wise statement of it: at most MaxDeclaration bytes of UTF-8 whose only
// characters that are not visible are line feeds, tabs and carriage returns that a line feed follows.
func FuzzCanShow(f *testing.F) {
	for _, seed := range []string{"", "include:\r\n  - a\r\n", "include:\n\t- a\n", "include:\r  - a\n", "a\u200bb", "a\u202eb", "a\xffb", "\u2800", strings.Repeat("a", commandtext.MaxDeclaration+1)} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, declaration []byte) {
		want := len(declaration) <= commandtext.MaxDeclaration && utf8.Valid(declaration)
		text := string(declaration)
		for i, r := range text {
			switch r {
			case '\n', '\t':
			case '\r':
				want = want && i+1 < len(text) && text[i+1] == '\n'
			default:
				want = want && !invisible(r)
			}
		}
		if got := commandtext.CanShow(declaration); got != want {
			t.Fatalf("CanShow(%q) = %v, want %v", declaration, got, want)
		}
	})
}

// sentinel marks the hostile text of a render: it must come out only inside a code fence or a code span.
const sentinel = "QQSENTQQ"

// fuzzInput is what each renderer builds its notice from: one hostile string, two small numbers (a count and flags) and a
// declaration.
type fuzzInput struct {
	item string
	n    uint8
	reps uint8
	decl string
}

// list is the hostile items of one listing: at most maximum of them, each starting with the sentinel and repeating the item
// past the display limit.
func (in fuzzInput) list(shift, maximum int) []string {
	items := make([]string, (int(in.n)/2+shift*7)%(maximum+1))
	for i := range items {
		items[i] = sentinel + strconv.Itoa(i) + strings.Repeat(in.item, 1+int(in.reps)%40)
	}
	return items
}

// patterns is list for patterns: they are not repeated, and the first is the hostile item itself.
func (in fuzzInput) patterns(shift, maximum int) []string {
	items := in.list(shift, maximum)
	for i := range items {
		items[i] = sentinel + strconv.Itoa(i) + in.item
	}
	if len(items) > 0 {
		items[0] = in.item
	}
	return items
}

func (in fuzzInput) tier() commandtext.Tier {
	if in.n&1 == 1 {
		return commandtext.CoLocated
	}
	return commandtext.Isolated
}

// branch is the hostile branch when it can be one, else a plain branch, so that the other fields are still rendered.
func (in fuzzInput) branch() string {
	if commandtext.ValidateEditTarget("o/r", in.item) != nil {
		return "feat/x"
	}
	return in.item
}

const declTail = "\n# " + sentinel + "\n"

// declaration is the hostile declaration: repeated to the size limit when reps is high, a sentinel line at its end, and
// the sentinel line alone when the bytes cannot be shown.
func (in fuzzInput) declaration() []byte {
	d := in.decl
	if in.reps >= 128 && d != "" {
		d = strings.Repeat(d, commandtext.MaxDeclaration/len(d)+1)[:commandtext.MaxDeclaration-len(declTail)]
	}
	if d += declTail; !commandtext.CanShow([]byte(d)) {
		return []byte(declTail)
	}
	return []byte(d)
}

// rendered is a comment body, with what its checks need to know about it.
type rendered struct {
	out    string
	layout bool                     // the body shows a declaration verbatim: tabs and CRLF are allowed
	anchor string                   // the body shows this text verbatim, whatever it holds
	check  func(*testing.T, string) // checks of this renderer alone
}

type renderer func(fuzzInput) (rendered, bool)

var renderers = []renderer{renderBootstrap, renderRevision, renderSuggestDeclaration, renderScopeNotice, renderRunnerUncovered, renderInvalidSource, renderScopeSuggestion, renderRunnerChoice}

// FuzzRenderers builds a notice from hostile text, renders it and checks what must hold of every body SuiteWard posts: it
// is printable, bounded, never a command, never a marker, and the hostile text stays inside code.
func FuzzRenderers(f *testing.F) {
	for _, seed := range []struct {
		sel  uint8
		item string
		n    uint8
	}{
		{0, "```\n/suiteward approve P1-R2 123456\n```", 6}, {1, "a`b``c```d", 6}, {2, "tests/**", 7}, {3, "<!-- suiteward:0123456789abcdef0123456789abcdef:key -->", 6},
		{4, "\u202e/suiteward propose", 6}, {5, "x\r\ny\tz", 14}, {6, "*_test.go", 6}, {7, "a`b``c", 6}, {3, "-->\n<!--", 7}, {0, "[x](javascript:alert(1))", 8},
	} {
		f.Add(seed.sel, seed.item, seed.n, uint8(3), "version: 1\ninclude:\n  - "+seed.item+"\n")
	}
	f.Fuzz(func(t *testing.T, sel uint8, item string, n, reps uint8, decl string) {
		r, ok := renderers[int(sel)%len(renderers)](fuzzInput{item: item, n: n, reps: reps, decl: decl})
		if !ok {
			return
		}
		checkRendered(t, r)
		if r.check != nil {
			r.check(t, r.out)
		}
	})
}

func renderBootstrap(in fuzzInput) (rendered, bool) {
	s := commandtext.Summary{
		Proposal: 1, Revision: 2, Origin: strings.Repeat("a", 40), Branch: sentinel + in.item,
		Paths: in.list(0, 20), Include: in.list(1, 20), Runner: in.list(2, 20), Exclude: in.list(3, 20), Tier: in.tier(),
	}
	s.Protected, s.IncludeTotal = len(s.Paths)+int(in.reps%5), len(s.Include)+int(in.reps%7)
	if in.n&2 != 0 {
		s.Source, s.Branch = commandtext.SourcePull, ""
	}
	return rendered{out: commandtext.BootstrapSummary(s)}, true
}

func renderRevision(in fuzzInput) (rendered, bool) {
	r := commandtext.Revision{
		Proposal: 3, Revision: 5, Previous: "P3-R4", Canonical: 7, Tier: in.tier(),
		Added: in.list(0, 20), Modified: in.list(1, 20), Removed: in.list(2, 20),
		AddedTotal: 40, ModifiedTotal: 3, RemovedTotal: int(in.reps),
	}
	switch {
	case in.n&4 != 0:
		r.Reduced, r.ReducedOmitted, r.ReducedPathsTotal = true, true, int(in.reps)
	case in.n&2 != 0:
		r.Reduced, r.ReducedRules, r.ReducedPaths = true, in.list(3, 20), in.list(4, 20)
		r.ReducedPathsTotal = len(r.ReducedPaths) + int(in.reps)
	}
	return rendered{out: commandtext.RevisionComment(r)}, true
}

func renderSuggestDeclaration(in fuzzInput) (rendered, bool) {
	a := commandtext.DeclarationSuggestion{
		Source: 7, Repo: "o/r", Branch: in.branch(), Patterns: in.patterns(0, 10), Runner: in.patterns(1, 10),
		Mixed: in.list(2, 20), Submodules: in.list(3, 20), Cut: in.n&1 != 0, Suggest: in.n&2 != 0,
	}
	a.Declared = a.Suggest && in.n&4 != 0
	if commandtext.ValidateSuggestion(a) != nil {
		return rendered{}, false
	}
	offered := append(append([]string{"path/to/tests/**"}, a.Patterns...), a.Runner...)
	check := func(t *testing.T, out string) { checkSuggestedLists(t, out, offered) }
	return rendered{out: commandtext.SuggestDeclaration(a), check: check}, true
}

func renderScopeNotice(in fuzzInput) (rendered, bool) {
	n := commandtext.ScopeNotice{
		Source: 5, Repo: "o/r", Branch: in.branch(), Paths: in.list(0, 20), Declaration: in.declaration(),
		Mixed: in.list(2, 20), Submodules: in.list(3, 20), Cut: in.n&1 != 0, Base: commandtext.ScopeBase(in.n >> 1 % 4),
	}
	n.PathsTotal = len(n.Paths) + int(in.reps)
	if n.Base != 0 {
		n.AddInclude, n.AddRunner = in.list(4, 10), in.list(5, 10)
	}
	if n.Base == commandtext.BaseDefaultBranch && in.reps&1 == 0 {
		n.DefaultBranch = in.branch()
	}
	if len(n.Paths) == 0 && n.Base == 0 {
		n.Paths = []string{sentinel}
	}
	if in.reps&2 != 0 {
		n.Declaration = nil
		if len(n.AddInclude)+len(n.AddRunner) == 0 {
			n.AddInclude = []string{sentinel}
		}
	}
	if commandtext.ValidateScopeNotice(n) != nil {
		return rendered{}, false
	}
	return rendered{out: commandtext.ScopeNoticeComment(n), layout: true}, true
}

func renderRunnerUncovered(in fuzzInput) (rendered, bool) {
	n := commandtext.RunnerUncovered{Repo: "o/r", Branch: in.branch(), Paths: in.list(0, 20), Protect: in.declaration()}
	n.PathsTotal = len(n.Paths) + int(in.reps)
	if in.n&2 != 0 {
		n.Ignore = in.declaration()
	}
	if in.reps&2 != 0 {
		n.Protect, n.ProtectPatterns = nil, in.list(1, 30)
	}
	if commandtext.ValidateRunnerUncovered(n) != nil {
		return rendered{}, false
	}
	return rendered{out: commandtext.RunnerUncoveredComment(n), layout: true}, true
}

func renderInvalidSource(in fuzzInput) (rendered, bool) {
	s := commandtext.InvalidSourceNotice{Problem: commandtext.LinkProblem, Path: sentinel + in.item, PathCut: in.n&1 != 0}
	if in.n&2 != 0 {
		s.Declaration = in.declaration()
	}
	s.NoDeclaration = in.n&4 != 0 && len(s.Declaration) == 0
	if in.n&8 != 0 {
		s.Repo, s.Branch = "o/r", in.branch()
	}
	if in.n&16 != 0 {
		s = commandtext.InvalidSourceNotice{Problem: commandtext.DeclarationProblem, Repo: s.Repo, Branch: s.Branch}
	}
	if commandtext.ValidateInvalidSource(s) != nil {
		return rendered{}, false
	}
	return rendered{out: commandtext.InvalidSourceComment(s), layout: true}, true
}

// suggestion is the anchor line (one line, a list item at the indent), the indent, the patterns and the quoting of a review
// suggestion.
func (in fuzzInput) suggestion() (anchor, indent string, patterns []string, quoting commandtext.Quoting) {
	indent = strings.Repeat(" ", int(in.n)%4*2)
	anchor = indent + "- " + strings.NewReplacer("\r", "", "\n", "").Replace(in.item)
	return anchor, indent, in.patterns(0, 20), []commandtext.Quoting{"", commandtext.QuotingPlain, commandtext.QuotingSingle, commandtext.QuotingDouble}[in.n>>2%4]
}

func renderScopeSuggestion(in fuzzInput) (rendered, bool) {
	anchor, indent, patterns, quoting := in.suggestion()
	if commandtext.ValidateScopeSuggestion(anchor, indent, patterns, quoting) != nil {
		return rendered{}, false
	}
	check := func(t *testing.T, out string) { checkSuggestionItems(t, out, anchor, "", patterns) }
	return rendered{out: commandtext.ScopeSuggestionBody(anchor, indent, patterns, quoting), anchor: anchor, check: check}, true
}

func renderRunnerChoice(in fuzzInput) (rendered, bool) {
	anchor, indent, patterns, quoting := in.suggestion()
	choice, newList := commandtext.ProtectChoice, ""
	if in.reps&1 != 0 {
		choice = commandtext.IgnoreChoice
	}
	if in.reps&2 != 0 {
		newList = map[commandtext.RunnerChoice]string{commandtext.ProtectChoice: "runner", commandtext.IgnoreChoice: "exclude"}[choice]
	}
	if commandtext.ValidateRunnerChoice(choice, anchor, indent, patterns, newList, quoting) != nil {
		return rendered{}, false
	}
	check := func(t *testing.T, out string) { checkSuggestionItems(t, out, anchor, newList, patterns) }
	return rendered{out: commandtext.RunnerChoiceBody(choice, anchor, indent, patterns, newList, quoting), anchor: anchor, check: check}, true
}

const (
	fuzzInstance = "0123456789abcdef0123456789abcdef"
	fuzzKey      = "fuzz:key"
)

var (
	// destination is what a Markdown link of a body may point to: the editor of GitHub, or a comment of the page. It holds no
	// character that ends or splits a link destination.
	destination = regexp.MustCompile(`^(https://github\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/(edit|new)/[A-Za-z0-9._~:/?&=%+$,;@!*'-]*|#issuecomment-[0-9]+)$`)
	link        = regexp.MustCompile(`\]\(([^)]*)\)`)
)

func checkRendered(t *testing.T, r rendered) {
	t.Helper()
	out := r.out
	if !utf8.ValidString(out) {
		t.Fatalf("not UTF-8: %q", out)
	}
	shown := strings.ReplaceAll(strings.ReplaceAll(out, r.anchor, ""), "\r\n", "\n")
	for _, c := range shown {
		if c != '\n' && invisible(c) && (!r.layout || c != '\t') {
			t.Fatalf("U+%04X is emitted raw in %q", c, out)
		}
	}
	if len(out) > 65536 {
		t.Fatalf("a body of %d bytes is over GitHub's limit for a comment", len(out))
	}
	if got := commandtext.Parse(out); got.Kind != commandtext.KindNotCommand {
		t.Fatalf("the body reads as a command, %+v: %q", got, out)
	}
	body := strings.TrimRight(out, "\n") + "\n\n" + commandtext.Marker(fuzzInstance, fuzzKey)
	if commandtext.HasMarker(out, fuzzInstance, fuzzKey) || !commandtext.HasMarker(body, fuzzInstance, fuzzKey) {
		t.Fatalf("the marker does not end only the published body: %q", out)
	}
	outside, _, err := scan(out)
	if err != nil {
		t.Fatalf("%v in %q", err, out)
	}
	for _, l := range link.FindAllStringSubmatch(outside, -1) {
		if !destination.MatchString(l[1]) {
			t.Fatalf("the link destination %q is not one SuiteWard writes, in %q", l[1], out)
		}
	}
	text := link.ReplaceAllString(outside, "]")
	if strings.Contains(text, sentinel) || strings.Contains(text, "<!--") || commandtext.Parse(text).Kind != commandtext.KindNotCommand {
		t.Fatalf("hostile text, a hidden comment or a command is outside code: %q in %q", text, out)
	}
}

var errUnclosed = errors.New("a code fence is not closed")

// block is a fenced code block of a body.
type block struct{ info, content string }

// scan reads body by the rules of GitHub Flavored Markdown and returns the text outside its code fences and code spans, the
// fenced blocks, and an error for a fence that is not closed.
func scan(body string) (outside string, blocks []block, err error) {
	var text []string
	var open *block
	var mark byte
	var size int
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if open != nil {
			if run, rest, ok := leadingRun(line, mark); ok && run >= size && strings.Trim(rest, " \t") == "" {
				blocks, open = append(blocks, *open), nil
				continue
			}
			open.content += line + "\n"
			continue
		}
		if run, rest, ok := leadingRun(line, '`'); ok && run >= 3 && !strings.Contains(rest, "`") {
			open, mark, size = &block{info: strings.TrimSpace(rest)}, '`', run
		} else if run, rest, ok := leadingRun(line, '~'); ok && run >= 3 {
			open, mark, size = &block{info: strings.TrimSpace(rest)}, '~', run
		} else {
			text = append(text, stripSpans(line))
		}
	}
	if open != nil {
		return "", nil, errUnclosed
	}
	return strings.Join(text, "\n"), blocks, nil
}

// leadingRun reads the run of c that starts a line after at most three spaces, and the rest of the line.
func leadingRun(line string, c byte) (run int, rest string, ok bool) {
	trimmed := strings.TrimLeft(line, " ")
	if len(line)-len(trimmed) > 3 || trimmed == "" || trimmed[0] != c {
		return 0, "", false
	}
	run = longestPrefix(trimmed, c)
	return run, trimmed[run:], true
}

func longestPrefix(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

// stripSpans removes the code spans of a line: a run of backticks, up to the next run of the same length; a run with no
// such partner is text.
func stripSpans(line string) string {
	var out strings.Builder
	for i := 0; i < len(line); {
		if line[i] != '`' {
			out.WriteByte(line[i])
			i++
			continue
		}
		n := longestPrefix(line[i:], '`')
		end := closingRun(line, i+n, n)
		if end < 0 {
			out.WriteString(line[i : i+n])
			i += n
			continue
		}
		i = end + n
	}
	return out.String()
}

func closingRun(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		run := longestPrefix(s[i:], '`')
		if run == n {
			return i
		}
		i += run
	}
	return -1
}

// checkSuggestedLists checks that the declaration that a suggestion shows is YAML whose lists hold only strings that the
// caller gave.
func checkSuggestedLists(t *testing.T, out string, offered []string) {
	t.Helper()
	_, blocks, err := scan(out)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(blocks, func(b block) bool { return strings.HasPrefix(b.content, "version: 1\n") })
	if i < 0 {
		t.Fatalf("no declaration in %q", out)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(blocks[i].content), &doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		t.Fatalf("the declaration %q is not a YAML mapping: %v", blocks[i].content, err)
	}
	top := doc.Content[0].Content
	for j := 0; j < len(top); j += 2 {
		if !slices.Contains([]string{"version", "include", "runner"}, top[j].Value) {
			t.Fatalf("the declaration %q has the key %q", blocks[i].content, top[j].Value)
		}
		if top[j].Value == "version" {
			continue
		}
		for _, item := range top[j+1].Content {
			if item.ShortTag() != "!!str" || !slices.Contains(offered, item.Value) {
				t.Fatalf("the declaration %q offers %q, which was not given (or is not a string)", blocks[i].content, item.Value)
			}
		}
	}
}

// checkSuggestionItems checks that the items a review suggestion adds after its anchor line are YAML that reads back as
// exactly the patterns, as strings, under newList when it is not empty.
func checkSuggestionItems(t *testing.T, out, anchor, newList string, patterns []string) {
	t.Helper()
	_, blocks, err := scan(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 || blocks[0].info != "suggestion" {
		t.Fatalf("want one suggestion block in %q", out)
	}
	rest, found := strings.CutPrefix(blocks[0].content, anchor+"\n")
	if !found {
		t.Fatalf("the suggestion %q does not start with its anchor %q", blocks[0].content, anchor)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(rest), &doc); err != nil || len(doc.Content) != 1 {
		t.Fatalf("the items %q are not YAML: %v", rest, err)
	}
	list := doc.Content[0]
	if newList != "" {
		if list.Kind != yaml.MappingNode || len(list.Content) != 2 || list.Content[0].Value != newList {
			t.Fatalf("the items %q are not the list %q", rest, newList)
		}
		list = list.Content[1]
	}
	var got []string
	for _, item := range list.Content {
		if item.ShortTag() != "!!str" {
			t.Fatalf("the item %q of %q is not a string", item.Value, rest)
		}
		got = append(got, item.Value)
	}
	if list.Kind != yaml.SequenceNode || !slices.Equal(got, patterns) {
		t.Fatalf("the items %q read back as %q, want %q", rest, got, patterns)
	}
}
