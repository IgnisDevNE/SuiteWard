package commandtext_test

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// Parse hides every line from one that holds an HTML comment opener to the next that holds a closer, whatever Markdown makes of the opener (a code
// span shows it as text). Repository text that holds an opener must therefore never reach a body as it is: with the opener in the branch and the
// closer in a path, the fences that follow would be read out of step, and the copyable approve command would read as a command.
func TestRepositoryTextNeverWritesAnHTMLCommentOpener(t *testing.T) {
	s := summary(2, []string{"y-->", "z<!--w"}, commandtext.Isolated)
	s.Branch = "x<!--"
	body := render(t, func() string { return commandtext.BootstrapSummary(s) })
	if strings.Contains(body, "<!--") {
		t.Errorf("the body holds an HTML comment opener:\n%s", body)
	}
	for _, want := range []string{`on branch ` + "`" + `"x\u003c!--"` + "`", `y-->`, `"z\u003c!--w"`, "```\n/suiteward approve P1-R1 <code>\n```"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if parsed := commandtext.Parse(body); parsed.Kind != commandtext.KindNotCommand {
		t.Errorf("the body reads as the command %+v:\n%s", parsed, body)
	}
}

func TestRevisionCommentNeverWritesAnHTMLCommentOpenerOfAPath(t *testing.T) {
	r := revision(commandtext.Isolated)
	r.Added, r.AddedTotal = []string{"tests/a<!--b_test.go"}, 1
	r.Reduced, r.ReducedPaths, r.ReducedPathsTotal = true, []string{"tests/<!--"}, 1
	r.ReducedRules = []string{"rule <!--**", "execution receipt: a<!--b"}

	body := revisionText(t, r)

	if strings.Contains(body, "<!--") {
		t.Errorf("the body holds an HTML comment opener:\n%s", body)
	}
	if !strings.Contains(body, `"tests/a\u003c!--b_test.go"`) {
		t.Errorf("the opener of the path is not shown escaped:\n%s", body)
	}
	checkBodyInert(t, body)
}

// A path that holds no opener is shown as it was.
func TestRepositoryTextWithoutAnHTMLCommentOpenerIsShownAsItIs(t *testing.T) {
	r := revision(commandtext.Isolated)
	r.Added, r.AddedTotal = []string{"tests/a!--b>_test.go", "tests/<!-b_test.go"}, 2
	if body := revisionText(t, r); !strings.Contains(body, "tests/a!--b>_test.go\ntests/<!-b_test.go\n") {
		t.Errorf("a path without an opener is not shown as it is:\n%s", body)
	}
}
