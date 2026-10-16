package commandtext_test

import (
	"net/url"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The explicit bootstrap (M1.7b rework): /suiteward propose and /suiteward skip <code>.

func TestParseProposeAndSkip(t *testing.T) {
	propose := commandtext.Parsed{Kind: commandtext.KindCommand, Command: commandtext.Command{Verb: commandtext.Propose}}
	skip := func(code string) commandtext.Parsed {
		return commandtext.Parsed{Kind: commandtext.KindCommand, Command: commandtext.Command{Verb: commandtext.Skip, Code: code}}
	}
	cases := []struct {
		name string
		body string
		want commandtext.Parsed
	}{
		{"propose", "/suiteward propose", propose},
		{"propose with spaces, tabs and CRLF", " \t/suiteward \t propose \t\r\nthanks\r\n", propose},
		{"propose on a later line", "Let's start.\n\n/suiteward propose", propose},
		{"propose after a closed fence", "```\n/suiteward propose extra\n```\n/suiteward propose", propose},
		{"propose only inside a fence", "```\n/suiteward propose\n```", notCommand},
		{"propose with a word after it", "/suiteward propose now", malformed(commandtext.ExtraText)},
		{"propose with a reference", "/suiteward propose P1-R1", malformed(commandtext.ExtraText)},
		{"propose with a code", "/suiteward propose 123456", malformed(commandtext.ExtraText)},
		{"propose is case-sensitive", "/suiteward Propose", malformed(commandtext.UnknownVerb)},
		{"proposed is another verb", "/suiteward proposed", malformed(commandtext.UnknownVerb)},

		{"skip", "/suiteward skip 123456", skip("123456")},
		{"skip with leading zeros", "/suiteward skip 000012", skip("000012")},
		{"skip with spaces, tabs and CRLF", "\t/suiteward  skip\t 654321 \r\n", skip("654321")},
		{"skip without a code", "/suiteward skip", malformed(commandtext.MissingCode)},
		{"skip with a short code", "/suiteward skip 12345", malformed(commandtext.BadCode)},
		{"skip with a long code", "/suiteward skip 1234567", malformed(commandtext.BadCode)},
		{"skip with a code that is not digits", "/suiteward skip 12345a", malformed(commandtext.BadCode)},
		{"skip takes no reference", "/suiteward skip P1-R1 123456", malformed(commandtext.BadCode)},
		{"skip with text after the code", "/suiteward skip 123456 now", malformed(commandtext.ExtraText)},
		{"skip with a second code", "/suiteward skip 123456 123456", malformed(commandtext.ExtraText)},
		{"skip is case-sensitive", "/suiteward Skip 123456", malformed(commandtext.UnknownVerb)},

		// approve and revoke are unchanged.
		{"approve without a code", "/suiteward approve P1-R1", malformed(commandtext.MissingCode)},
		{"revoke", "/suiteward revoke P2-R1 111111", command(commandtext.Revoke, 2, 1, "111111")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandtext.Parse(tc.body); got != tc.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tc.body, got, tc.want)
			}
		})
	}
}

const (
	repository = "octo-org/widgets"
	branch     = "feature/add-tests"
)

func TestAcknowledgeProposeAndSkip(t *testing.T) {
	base := commandtext.Ack{Source: source, Owner: owner}
	with := func(a commandtext.Ack) commandtext.Ack {
		a.Source, a.Owner = base.Source, base.Owner
		return a
	}
	cases := []struct {
		name    string
		ack     commandtext.Ack
		want    []string
		notWant []string
	}{
		{
			"propose refused: canonical present",
			with(commandtext.Ack{Outcome: commandtext.ProposeRefused, Refusal: commandtext.CanonicalPresent}),
			[]string{"`/suiteward propose` was not processed: this repository already has a canonical contract", "Nothing changed."},
			nil,
		},
		{
			"propose refused: fork",
			with(commandtext.Ack{Outcome: commandtext.ProposeRefused, Refusal: commandtext.Fork}),
			[]string{"`/suiteward propose` was not processed: this pull request comes from a fork", "not supported yet", "Nothing changed.", "branch of this repository"},
			nil,
		},
		{
			"propose refused: empty inventory",
			with(commandtext.Ack{Outcome: commandtext.ProposeRefused, Refusal: commandtext.EmptyInventory}),
			[]string{"`/suiteward propose` was not processed: no file matches the include rules", "Nothing changed.", "comment `/suiteward propose` again"},
			nil,
		},
		{
			"propose refused: invalid source",
			with(commandtext.Ack{Outcome: commandtext.ProposeRefused, Refusal: commandtext.InvalidSource}),
			[]string{"`/suiteward propose` was not processed: SuiteWard could not read the files to protect as a contract", "declaration is invalid", "a link", "too large", "Nothing changed.", "comment `/suiteward propose` again"},
			nil,
		},
		{
			"propose refused: not open",
			with(commandtext.Ack{Outcome: commandtext.ProposeRefused, Refusal: commandtext.NotOpen}),
			[]string{"`/suiteward propose` was not processed: this pull request is no longer open", "Nothing changed.", "Comment `/suiteward propose` on an open pull request into the default branch"},
			nil,
		},
		{
			"propose refused: base is not the target",
			with(commandtext.Ack{Outcome: commandtext.ProposeRefused, Refusal: commandtext.BaseNotTarget}),
			[]string{"`/suiteward propose` was not processed: this pull request does not target the default branch", "its merge could never become the first contract", "Nothing changed.", "into the default branch"},
			nil,
		},
		{
			"already proposed",
			with(commandtext.Ack{Outcome: commandtext.AlreadyProposed, Proposal: 4, Revision: 2}),
			[]string{"already proposed as P4-R2", "Nothing changed.", "bootstrap summary"},
			nil,
		},
		{
			"propose failed",
			with(commandtext.Ack{Outcome: commandtext.ProposeFailed}),
			[]string{"could not process your `/suiteward propose` command", "proposed nothing", "comment `/suiteward propose` again"},
			nil,
		},
		{
			"skipped",
			with(commandtext.Ack{Outcome: commandtext.Skipped}),
			[]string{"Skip recorded for this pull request by @octo-owner.", "may merge before the first contract", "nothing was assessed", "not a contract assessment", "`/suiteward propose`"},
			nil,
		},
		{
			"skip refused: canonical present",
			with(commandtext.Ack{Outcome: commandtext.SkipRefused, Refusal: commandtext.CanonicalPresent}),
			[]string{"`/suiteward skip` was not processed: this repository already has a canonical contract", "Nothing changed."},
			nil,
		},
		{
			"skip refused: fork",
			with(commandtext.Ack{Outcome: commandtext.SkipRefused, Refusal: commandtext.Fork}),
			[]string{"`/suiteward skip` was not processed: this pull request comes from a fork", "Nothing changed."},
			nil,
		},
		{
			"skip refused: carrier",
			with(commandtext.Ack{Outcome: commandtext.SkipRefused, Refusal: commandtext.Carrier}),
			[]string{"`/suiteward skip` was not processed: this pull request carries the first contract proposal", "Nothing changed.", "`/suiteward propose` on another pull request"},
			nil,
		},
		{
			// A skip has no reference, so the code outcomes and NoSuite do not name one either.
			"invalid code without a reference",
			with(commandtext.Ack{Outcome: commandtext.InvalidCode}),
			[]string{"The confirmation code was not accepted; nothing changed.", "current code"},
			[]string{" for P", "for  "},
		},
		{
			"replayed code without a reference",
			with(commandtext.Ack{Outcome: commandtext.ReplayedCode}),
			[]string{"The confirmation code was already used; each code works only once, so nothing changed.", "new code"},
			[]string{" for P"},
		},
		{
			"no suite without a reference",
			with(commandtext.Ack{Outcome: commandtext.NoSuite}),
			[]string{"Your command was not processed: this repository's SuiteWard bootstrap is still pending, so nothing changed.", "comment `/suiteward propose`"},
			[]string{"P0", "R0", "bootstrap summary"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := render(t, func() string { return commandtext.Acknowledge(tc.ack) })
			if body == "" {
				t.Fatal("empty body")
			}
			for _, w := range tc.want {
				if !strings.Contains(body, w) {
					t.Errorf("body lacks %q:\n%s", w, body)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(body, w) {
					t.Errorf("body contains %q:\n%s", w, body)
				}
			}
			if link := "[your command](#issuecomment-2345678901)"; !strings.Contains(body, link) {
				t.Errorf("body lacks %q:\n%s", link, body)
			}
			if strings.Contains(body, "http") || strings.Contains(body, "/suiteward approve") || strings.Contains(body, "/suiteward revoke") {
				t.Errorf("body holds a URL or offers an approval:\n%s", body)
			}
			checkBody(t, body)
		})
	}
}

func declarationMissing(patterns ...string) commandtext.DeclarationSuggestion {
	return commandtext.DeclarationSuggestion{Source: source, Repo: repository, Branch: branch, Patterns: patterns}
}

var linkPattern = regexp.MustCompile(`\]\((https://github\.com/[^)\s]*)\)`)

// editorLink returns the one link of a declaration-missing body, parsed.
func editorLink(t *testing.T, body string) (raw string, u *url.URL) {
	t.Helper()
	found := linkPattern.FindAllStringSubmatch(body, -1)
	if len(found) != 1 {
		t.Fatalf("body has %d editor links, want 1:\n%s", len(found), body)
	}
	u, err := url.Parse(found[0][1])
	if err != nil {
		t.Fatalf("the link %q does not parse: %v", found[0][1], err)
	}
	return found[0][1], u
}

// linkedDeclaration is the .suiteward.yml the link of body fills in, after checking where the link points.
func linkedDeclaration(t *testing.T, body, wantBranchPath string) string {
	t.Helper()
	raw, u := editorLink(t, body)
	if u.Host != "github.com" || u.Path != "/"+repository+"/new/"+wantBranchPath {
		t.Errorf("the link points at %s%s, want github.com/%s/new/%s", u.Host, u.Path, repository, wantBranchPath)
	}
	query := u.Query()
	if len(query) != 2 || !strings.HasPrefix(u.RawQuery, "filename=.suiteward.yml&value=") {
		t.Errorf("the link query is %q, want exactly filename and value", u.RawQuery)
	}
	if strings.ContainsAny(raw, " +\n\"<>`") {
		t.Errorf("the link %q holds a character that is not percent-encoded", raw)
	}
	return query.Get("value")
}

func fencedYAML(declaration string) string { return "```\n" + declaration + "```" }

func TestSuggestDeclarationSuggestsTheDeclarationAndLinksGitHubsEditor(t *testing.T) {
	suggestion := []string{"tests/**", "internal/app/*_test.*"}
	body := render(t, func() string { return commandtext.SuggestDeclaration(declarationMissing(suggestion...)) })
	declaration := "version: 1\ninclude:\n  - \"tests/**\"\n  - \"internal/app/*_test.*\"\n"
	for _, w := range []string{
		"`/suiteward propose` was not processed: SuiteWard found no `.suiteward.yml`", "default branch", "head of this pull request",
		"proposed nothing", "Nothing changed.", fencedYAML(declaration), "comment `/suiteward propose` again", "never commits",
		"[your command](#issuecomment-2345678901)",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("body lacks %q:\n%s", w, body)
		}
	}
	if got := linkedDeclaration(t, body, "feature/add-tests"); got != declaration {
		t.Errorf("the link fills in %q, want %q", got, declaration)
	}
	if strings.Contains(body, "/suiteward approve") || strings.Contains(body, "left out") {
		t.Errorf("body offers an approval or says patterns were left out:\n%s", body)
	}
	checkBody(t, body)
}

// The suggestion must be a declaration SuiteWard itself accepts, whatever the patterns hold.
func TestSuggestDeclarationParsesAsADeclaration(t *testing.T) {
	patterns := []string{"*_test.*", "#hash/**", "a: b/**", "[ab]/*.go", "{x}/**", "é/**", "it's/**", "- x/**", "tests/`x`/**", "spec/**"}
	body := render(t, func() string { return commandtext.SuggestDeclaration(declarationMissing(patterns...)) })
	declaration, err := scope.ParseDeclaration([]byte(linkedDeclaration(t, body, "feature/add-tests")))
	if err != nil {
		t.Fatalf("the linked declaration is invalid: %v", err)
	}
	var got []string
	for _, p := range declaration.Include() {
		got = append(got, p.String())
	}
	if !slices.Equal(got, patterns) {
		t.Errorf("the declaration includes %q, want %q", got, patterns)
	}
	if !strings.Contains(body, "```\nversion: 1\n") {
		t.Errorf("the declaration is not in a code block:\n%s", body)
	}
}

func TestSuggestDeclarationFencesTheDeclarationAgainstBackticks(t *testing.T) {
	body := render(t, func() string { return commandtext.SuggestDeclaration(declarationMissing("a/```/b/**")) })
	if !strings.Contains(body, "````\nversion: 1\ninclude:\n  - \"a/```/b/**\"\n````") {
		t.Errorf("a pattern with backticks can close the code block:\n%s", body)
	}
}

func TestSuggestDeclarationWithoutPatternsGivesAMinimalFileToFillIn(t *testing.T) {
	for _, suggestion := range [][]string{nil, {}} {
		body := render(t, func() string { return commandtext.SuggestDeclaration(declarationMissing(suggestion...)) })
		declaration := "version: 1\ninclude:\n  - \"path/to/tests/**\"\n"
		if !strings.Contains(body, fencedYAML(declaration)) || !strings.Contains(body, "no test-like path") || !strings.Contains(body, "Replace") {
			t.Errorf("body does not give the minimal file to fill in:\n%s", body)
		}
		if got := linkedDeclaration(t, body, "feature/add-tests"); got != declaration {
			t.Errorf("the link fills in %q, want %q", got, declaration)
		}
		if _, err := scope.ParseDeclaration([]byte(declaration)); err != nil {
			t.Errorf("the minimal file is not a valid declaration: %v", err)
		}
	}
}

func TestSuggestDeclarationEscapesTheBranchInThePath(t *testing.T) {
	cases := map[string]string{
		"main":            "main",
		"feature/x":       "feature/x",
		"fix/a#b%d":       "fix/a%23b%25d",
		"feat/(x)":        "feat/%28x%29",
		"user/ünï-€":      "user/%C3%BCn%C3%AF-%E2%82%AC",
		"a&b=c+d/e;f,g@h": "a&b=c+d/e%3Bf%2Cg@h",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			a := declarationMissing("tests/**")
			a.Branch = name
			body := render(t, func() string { return commandtext.SuggestDeclaration(a) })
			raw, u := editorLink(t, body)
			if u.Path != "/"+repository+"/new/"+name {
				t.Errorf("the link path decodes to %q, want %q", u.Path, "/"+repository+"/new/"+name)
			}
			if !strings.Contains(raw, "/new/"+want+"?filename=.suiteward.yml&value=") {
				t.Errorf("the link %q does not carry the branch as %q before the query", raw, want)
			}
		})
	}
}

func TestSuggestDeclarationLeavesOutPatternsThatCannotBeSuggestedSafely(t *testing.T) {
	bad := []string{"a\nevil: true", "has\"quote", "back\\slash", "\xff/**", strings.Repeat("a", 101), "tab\there", "\u202ebidi/**"}
	body := render(t, func() string {
		return commandtext.SuggestDeclaration(declarationMissing(append([]string{"tests/**"}, bad...)...))
	})
	for _, leak := range []string{"evil", "quote", "slash", "bidi", "aaaaaaaaaa", "\xff", "\u202e", "\t"} {
		if strings.Contains(body, leak) {
			t.Errorf("body holds a pattern it should have left out (%q):\n%s", leak, body)
		}
	}
	if !strings.Contains(body, "Left out of this suggestion because they are too long or contain unusual characters: 7.") {
		t.Errorf("body does not count the patterns it left out:\n%s", body)
	}
	declaration := "version: 1\ninclude:\n  - \"tests/**\"\n"
	if got := linkedDeclaration(t, body, "feature/add-tests"); got != declaration {
		t.Errorf("the link fills in %q, want %q", got, declaration)
	}
	// Patterns 100 bytes long are kept: only longer ones are left out.
	exact := strings.Repeat("a", 98) + "/*"
	body = render(t, func() string { return commandtext.SuggestDeclaration(declarationMissing(exact)) })
	if !strings.Contains(body, "\""+exact+"\"") || strings.Contains(body, "Left out") {
		t.Errorf("a pattern of exactly 100 bytes was dropped:\n%s", body)
	}
	// When nothing is left, the minimal file is suggested.
	body = render(t, func() string { return commandtext.SuggestDeclaration(declarationMissing(bad...)) })
	if !strings.Contains(body, "path/to/tests/**") || !strings.Contains(body, "unusual characters: 7.") {
		t.Errorf("body does not fall back to the minimal file:\n%s", body)
	}
}

// The largest declaration-missing acknowledgment: 20 patterns of 100 bytes made of what encodes worst, the longest
// branch and the longest repository name.
func TestSuggestDeclarationStaysInsideAComment(t *testing.T) {
	for name, unit := range map[string]string{"plain": "a", "3-byte": "\u20ac", "backtick": "`", "space": " ", "percent": "%"} {
		t.Run(name, func(t *testing.T) {
			pattern := func(i int) string {
				p := strings.Repeat(unit, 99/len(unit))
				return p + strings.Repeat("x", 100-len(p)-1) + string(rune('a'+i))
			}
			var suggestion []string
			for i := range 20 {
				suggestion = append(suggestion, pattern(i))
			}
			a := declarationMissing(suggestion...)
			a.Repo = "a" + strings.Repeat("b", 38) + "/" + strings.Repeat("c", 100)
			a.Branch = strings.Repeat("\u20ac", 85)
			body := render(t, func() string { return commandtext.SuggestDeclaration(a) })
			if body == "" {
				t.Fatal("empty body")
			}
			raw := linkPattern.FindStringSubmatch(body)
			if raw == nil {
				t.Fatalf("no link in body:\n%s", body)
			}
			if len(raw[1]) > 8000 { // browsers and GitHub refuse far longer URLs; this stays well inside them
				t.Errorf("the link has %d bytes", len(raw[1]))
			}
			if len(body) > maxComment/4 {
				t.Errorf("the body has %d bytes", len(body))
			}
		})
	}
}

func TestAcknowledgeProposeAndSkipPanicsOnImpossibleState(t *testing.T) {
	refused := func(outcome commandtext.Outcome, refusal commandtext.Refusal) commandtext.Ack {
		return commandtext.Ack{Outcome: outcome, Refusal: refusal, Source: source, Owner: owner}
	}
	cases := map[string]commandtext.Ack{
		"propose refused without a reason":               refused(commandtext.ProposeRefused, 0),
		"propose refused with an unknown reason":         refused(commandtext.ProposeRefused, 99),
		"propose refused for a carrier":                  refused(commandtext.ProposeRefused, commandtext.Carrier),
		"skip refused without a reason":                  refused(commandtext.SkipRefused, 0),
		"skip refused for an empty inventory":            refused(commandtext.SkipRefused, commandtext.EmptyInventory),
		"skip refused for an invalid source":             refused(commandtext.SkipRefused, commandtext.InvalidSource),
		"skip refused for a pull request not open":       refused(commandtext.SkipRefused, commandtext.NotOpen),
		"skip refused for a base that is not the target": refused(commandtext.SkipRefused, commandtext.BaseNotTarget),
		"a refusal on a skip":                            refused(commandtext.Skipped, commandtext.Fork),
		"a refusal on an approval":                       {Outcome: commandtext.Approved, Refusal: commandtext.Fork, Proposal: 1, Revision: 1, Source: source, Owner: owner},
		"already proposed without a reference":           {Outcome: commandtext.AlreadyProposed, Source: source, Owner: owner},
		"already proposed with half a reference":         {Outcome: commandtext.AlreadyProposed, Proposal: 1, Source: source, Owner: owner},
		"a reference on a propose refusal":               {Outcome: commandtext.ProposeRefused, Refusal: commandtext.Fork, Proposal: 1, Revision: 1, Source: source, Owner: owner},
		"a reference on a propose failure":               {Outcome: commandtext.ProposeFailed, Proposal: 1, Revision: 1, Source: source, Owner: owner},
		"a reference on a skip":                          {Outcome: commandtext.Skipped, Proposal: 1, Revision: 1, Source: source, Owner: owner},
		"eligibility on a skip":                          {Outcome: commandtext.Skipped, Source: source, Owner: owner, Eligible: true},
		"a version on a skip":                            {Outcome: commandtext.Skipped, Version: 1, Source: source, Owner: owner},
		"no source command on a skip":                    {Outcome: commandtext.Skipped, Owner: owner},
		"no source command on a propose failure":         {Outcome: commandtext.ProposeFailed, Owner: owner},
		"no owner on a skip":                             {Outcome: commandtext.Skipped, Source: source},
		"an invalid code with half a reference":          {Outcome: commandtext.InvalidCode, Proposal: 1, Source: source, Owner: owner},
		"a replayed code with a reference out of range":  {Outcome: commandtext.ReplayedCode, Proposal: 1, Revision: 1000000000, Source: source, Owner: owner},
		"no suite with half a reference":                 {Outcome: commandtext.NoSuite, Revision: 1, Source: source, Owner: owner},
		"approved without a reference":                   {Outcome: commandtext.Approved, Source: source, Owner: owner},
	}
	for name, ack := range cases {
		t.Run(name, func(t *testing.T) {
			mustPanic(t, func() { commandtext.Acknowledge(ack) })
		})
	}
}

func TestSuggestDeclarationPanicsOnImpossibleState(t *testing.T) {
	branch := func(b string) func(*commandtext.DeclarationSuggestion) {
		return func(s *commandtext.DeclarationSuggestion) { s.Branch = b }
	}
	repo := func(r string) func(*commandtext.DeclarationSuggestion) {
		return func(s *commandtext.DeclarationSuggestion) { s.Repo = r }
	}
	cases := map[string]func(*commandtext.DeclarationSuggestion){
		"no source command":              func(s *commandtext.DeclarationSuggestion) { s.Source = 0 },
		"a negative source command":      func(s *commandtext.DeclarationSuggestion) { s.Source = -1 },
		"no repository":                  repo(""),
		"a repository without a name":    repo("octo-org/"),
		"a repository without an owner":  repo("/widgets"),
		"a repository of three parts":    repo("octo/org/widgets"),
		"a repository with a space":      repo("octo-org/wid gets"),
		"a repository with a query":      repo("octo-org/widgets?x=1"),
		"a repository with markup":       repo("octo-org/w)[x](y"),
		"a repository named dot":         repo("octo-org/."),
		"a repository named dot dot":     repo("octo-org/.."),
		"an owner of 40 characters":      repo("a" + strings.Repeat("b", 39) + "/widgets"),
		"a repository name of 101 chars": repo("octo-org/" + strings.Repeat("w", 101)),
		"no branch":                      branch(""),
		"a branch with a leading slash":  branch("/feature"),
		"a branch with a trailing slash": branch("feature/"),
		"a branch with an empty segment": branch("a//b"),
		"a branch with a dot segment":    branch("a/../b"),
		"a branch with a hidden segment": branch("a/.b"),
		"a branch with two dots":         branch("a..b"),
		"a branch with a newline":        branch("a\nb"),
		"a branch with a space":          branch("a b"),
		"a branch with a colon":          branch("a:b"),
		"a branch with a question mark":  branch("a?b"),
		"a branch with a backslash":      branch("a\\b"),
		"a branch with a star":           branch("a*b"),
		"a branch that is not UTF-8":     branch("a\xffb"),
		"a branch of 256 bytes":          branch(strings.Repeat("b", 256)),
		"more than 20 patterns":          func(s *commandtext.DeclarationSuggestion) { s.Patterns = slices.Repeat([]string{"tests/**"}, 21) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := declarationMissing("tests/**")
			mutate(&s)
			mustPanic(t, func() { commandtext.SuggestDeclaration(s) })
		})
	}
	// 255 bytes and 20 patterns are the largest allowed.
	s := declarationMissing(slices.Repeat([]string{"tests/**"}, 20)...)
	s.Branch = strings.Repeat("b", 255)
	if body := render(t, func() string { return commandtext.SuggestDeclaration(s) }); body == "" {
		t.Error("the largest suggestion was refused")
	}
}

// Nothing a comment carries reaches the body: not the skip code, and not the text after a malformed command.
func TestProposeAndSkipBodiesNeverEchoTheComment(t *testing.T) {
	skip := commandtext.Parse("Skipping CANARY-TEXT\n/suiteward skip 493817\nCANARY-TAIL")
	if skip.Kind != commandtext.KindCommand || skip.Command.Verb != commandtext.Skip {
		t.Fatalf("Parse = %+v, want a skip", skip)
	}
	broken := commandtext.Parse("CANARY-TEXT\n/suiteward propose CANARY-EXTRA")
	if broken.Kind != commandtext.KindMalformed {
		t.Fatalf("Parse = %+v, want malformed", broken)
	}
	ack := func(outcome commandtext.Outcome, refusal commandtext.Refusal) commandtext.Ack {
		return commandtext.Ack{Outcome: outcome, Refusal: refusal, Source: source, Owner: owner}
	}
	bodies := []string{
		commandtext.Acknowledge(ack(commandtext.Skipped, 0)),
		commandtext.Acknowledge(ack(commandtext.SkipRefused, commandtext.Carrier)),
		commandtext.Acknowledge(ack(commandtext.InvalidCode, 0)),
		commandtext.Acknowledge(ack(commandtext.ReplayedCode, 0)),
		commandtext.Acknowledge(ack(commandtext.ProposeFailed, 0)),
		commandtext.Acknowledge(commandtext.Ack{Outcome: commandtext.MalformedCommand, Malformed: broken.Reason, Source: source, Owner: owner}),
	}
	for i, body := range bodies {
		for _, leak := range []string{"CANARY", "493817", "Skipping"} {
			if strings.Contains(body, leak) {
				t.Errorf("body %d echoes %q:\n%s", i, leak, body)
			}
		}
	}
}

func TestBootstrapNoticeSaysHowToStartOrSkip(t *testing.T) {
	body := render(t, commandtext.BootstrapNotice)
	for _, w := range []string{
		"SuiteWard has no canonical contract yet.", "`/suiteward propose`", "pull request that should carry the first contract",
		"`/suiteward skip <code>`", "before the first contract", "authenticator app",
		"of the default branch when it has a `.suiteward.yml`, otherwise those of that pull request",
	} {
		if !strings.Contains(body, w) {
			t.Errorf("notice lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "/suiteward approve") || strings.Contains(body, "http") || len(body) > 2048 {
		t.Errorf("notice offers an approval, holds a URL or is long:\n%s", body)
	}
	checkBody(t, body)
}

func TestBootstrapCancelledNamesTheProposalAndSaysToProposeAgain(t *testing.T) {
	body := render(t, func() string { return commandtext.BootstrapCancelled(3, 2) })
	for _, w := range []string{"P3-R2", "protected files changed", "cancelled", "`/suiteward propose` again"} {
		if !strings.Contains(body, w) {
			t.Errorf("comment lacks %q:\n%s", w, body)
		}
	}
	if strings.Contains(body, "/suiteward approve") || strings.Contains(body, "http") {
		t.Errorf("comment offers an approval or holds a URL:\n%s", body)
	}
	checkBody(t, body)
	if got := render(t, func() string { return commandtext.BootstrapCancelled(999999999, 999999999) }); len(got) > 2048 {
		t.Errorf("the comment has %d bytes", len(got))
	}
}

func TestBootstrapCancelledPanicsOnAReferenceThatCannotBeStored(t *testing.T) {
	for _, ids := range [][2]int{{0, 1}, {1, 0}, {-1, 1}, {1, 1_000_000_000}} {
		mustPanic(t, func() { commandtext.BootstrapCancelled(ids[0], ids[1]) })
	}
}
