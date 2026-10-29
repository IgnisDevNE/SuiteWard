package commandtext_test

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
)

// The link to the pull request page (docs/contracts/runtime.md, "The pull request page", "The comments of a head"): a renderer writes only this link, a loopback
// address as the settings allow, the path /pulls/<owner>/<repo>/<pull> and the number of the pull request, and refuses every other, as it does for the report and the
// receipt link.

const pullURL = "http://127.0.0.1:8080/pulls/acme/widgets/7"

func TestValidPullLinkAcceptsTheLoopbackPageOfThePullRequestAndNothingElse(t *testing.T) {
	for _, tc := range []struct {
		link string
		pull int
		want bool
	}{
		{pullURL, 7, true},
		{"http://localhost:8080/pulls/acme/widgets/7", 7, true},
		{"http://127.0.0.1:1/pulls/a.b_c-d/w%20x/7", 7, true},
		{"http://127.0.0.1:65535/pulls/acme/widgets/2147483647", 2147483647, true},
		{pullURL, 8, false},
		{receiptURL, 7, false}, // the old route redirects: no comment links it any more
		{reportURL, 7, false},
		{"http://127.0.0.1:8080/pulls/acme/widgets/7/", 7, false},
		{"http://127.0.0.1:8080/pulls/acme/widgets/7/x", 7, false},
		{"http://127.0.0.1:8080/pulls/acme/widgets/7?x=1", 7, false},
		{"http://127.0.0.1:8080/pulls/acme/widgets/7#top", 7, false},
		{"http://127.0.0.1:8080/pulls/acme/widgets/07", 7, false},
		{"http://127.0.0.1:8080/pulls/acme/widgets/0", 0, false},
		{"http://127.0.0.1:8080/pulls/acme/widgets/-7", -7, false},
		{"http://127.0.0.1:8080/pulls/../widgets/7", 7, false},
		{"http://127.0.0.1:8080/pulls/acme/./7", 7, false},
		{"http://127.0.0.1:8080/pulls/acme/wid\u202egets/7", 7, false},
		{"http://127.0.0.1:8080/pulls/acme/<!--/7", 7, false},
		{"http://[::1]:8080/pulls/acme/widgets/7", 7, false}, // the settings allow 127.0.0.1 and localhost only
		{"https://127.0.0.1:8080/pulls/acme/widgets/7", 7, false},
		{"http://example.com:8080/pulls/acme/widgets/7", 7, false},
		{"http://127.0.0.1:8080/pulls/acme/widgets/", 7, false},
		{"http://127.0.0.1:8080/pulls//widgets/7", 7, false},
		{"", 7, false},
	} {
		if got := commandtext.ValidPullLink(tc.link, tc.pull); got != tc.want {
			t.Errorf("ValidPullLink(%q, %d) = %v, want %v", tc.link, tc.pull, got, tc.want)
		}
	}
}
