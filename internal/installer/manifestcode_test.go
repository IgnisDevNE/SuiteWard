package installer

import (
	"errors"
	"strings"
	"testing"
)

// The manifest code converts into the App's credentials, the key included, for an hour: an argument would stay in the shell
// history and the process list, so there is no flag.
func TestInstallHasNoManifestCodeFlag(t *testing.T) {
	h := newHarness(t, "linux")
	err := h.podman("--manifest-code", "thecode1")
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("--manifest-code = %v, want a usage error: the flag is gone", err)
	}
	h.mustNotMutate()
	if newInstallFlagSet().Lookup("manifest-code") != nil {
		t.Error("the install still defines --manifest-code")
	}
}

// A Resolver whose flags carry the code (a caller other than the command line) is refused as well, before anything is created.
func TestResolveGitHubRefusesAManifestCodeGivenAsAFlag(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
	env.R.flags["manifest-code"] = "thecode1"
	_, err := resolve(t, env)
	if err == nil || !strings.Contains(err.Error(), "argument") || !strings.Contains(err.Error(), "answers file") {
		t.Fatalf("error = %v, want a refusal that points to the answers file", err)
	}
	if g.sawRequest("POST /app-manifests") || strings.Contains(err.Error()+out.String(), "thecode1") {
		t.Errorf("the code was used or shown: requests %v\n%s", g.requests, out.String())
	}
}

func TestResolveGitHubTakesTheManifestCodeFromTheAnswersFile(t *testing.T) {
	g := newFakeGitHub(t)
	flags := manifestFlags()
	delete(flags, "manifest-code")
	env, out := flowEnv(t, g, flags, strings.NewReader(""), false)
	env.R.answers["manifest-code"] = "thecode1"
	if _, err := resolve(t, env); err != nil {
		t.Fatal(err)
	}
	if !g.sawRequest("POST /app-manifests") || strings.Contains(out.String(), "thecode1") {
		t.Errorf("the code was not converted, or it was printed: requests %v\n%s", g.requests, out.String())
	}
}
