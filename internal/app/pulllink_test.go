package app

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The link to the pull request page (docs/contracts/runtime.md, "The pull request page", "Route"): <REPORT_BASE_URL>/pulls/<owner>/<repo>/<pull>, with the owner and the
// name path-escaped, written like the links of the report and the receipt, and only when the instance has a base URL. The renderers accept it, and no other.
func TestThePullRequestPageLinkIsTheBaseAndTheEscapedOwnerAndNameAndThePullNumber(t *testing.T) {
	for name, tc := range map[string]struct {
		base, repo string
		pull       int
		want       string
	}{
		"a plain repository":   {"http://127.0.0.1:8080", "acme/widgets", 7, "http://127.0.0.1:8080/pulls/acme/widgets/7"},
		"localhost":            {"http://localhost:47081", "IgnisDevNE/SuiteWard", 62, "http://localhost:47081/pulls/IgnisDevNE/SuiteWard/62"},
		"dots and dashes":      {"http://127.0.0.1:8080", "a.b-c/d_e.f", 1, "http://127.0.0.1:8080/pulls/a.b-c/d_e.f/1"},
		"an escaped character": {"http://127.0.0.1:8080", "acme/wid gets", 7, "http://127.0.0.1:8080/pulls/acme/wid%20gets/7"},
		"no base address":      {"", "acme/widgets", 7, ""},
	} {
		got := pullPageURL(tc.base, tc.repo, tc.pull)
		if got != tc.want {
			t.Errorf("%s: pullPageURL(%q, %q, %d) = %q, want %q", name, tc.base, tc.repo, tc.pull, got, tc.want)
		}
		if tc.want != "" && !commandtext.ValidPullLink(got, tc.pull) {
			t.Errorf("%s: the renderer refuses the link %q that the builder writes", name, got)
		}
	}
}
