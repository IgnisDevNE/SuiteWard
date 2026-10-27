package app

import (
	"strings"
	"testing"
)

// The archive token is one of the tokens the service mints, so the self-check mints it too and names it when GitHub refuses it.
func TestSelfCheckGitHubMintsTheArchiveToken(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.denyPermission = "contents" // the synchronization token and the archive token both name it

	results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

	got := byName(t, results)["github.permissions"]
	if got.OK || !strings.Contains(got.Detail, "the archive token") {
		t.Errorf("github.permissions = %+v; want it to fail and name the archive token among the tokens GitHub refused", got)
	}
	noSecret(t, results)
}
