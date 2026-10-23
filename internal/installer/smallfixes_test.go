package installer

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// An image the engine already has is not pulled, so an offline re-run works, as the co-located install and the upgrade do.
func TestInstallIsolatedPullsOnlyTheImagesTheEngineLacks(t *testing.T) {
	g := newIsoGitHub(t) // the fake engine has every image
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if g.f.has("podman pull") {
		t.Errorf("an image that is here was pulled: %v", g.f.lines())
	}

	g = newIsoGitHub(t)
	g.f.noImage = true
	if err := g.run(); err != nil {
		t.Fatal(err)
	}
	if !g.f.has("podman pull "+releaseImageRepo+"@"+testDigest) || !g.f.has("podman pull docker.io/library/postgres") {
		t.Errorf("a missing image was not pulled: %v", g.f.lines())
	}
}

// The isolated upgrade reads /status through the same seam as the other commands, so a test or a caller can replace it.
func TestUpgradeIsolatedReadsStatusThroughTheEnvSeam(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	fastHealthWait(t)
	var asked []string
	env := u.env()
	env.HTTPGet = func(_ context.Context, address string) ([]byte, error) {
		asked = append(asked, address)
		return []byte(`{"version":"v9.9.9","schemaVersion":20}`), nil
	}
	args := []string{"--upgrade", "--tier", "isolated", "--settings", settingsFile(u.home), "--backup-dir", u.backups, "--image", testDigest, "--yes"}
	if err := Install(context.Background(), args, env); err != nil {
		t.Fatal(err)
	}
	// The wait for a stable service after the upgrade reads /readyz through the seam as well.
	if readyz := slices.DeleteFunc(slices.Clone(asked), func(a string) bool { return !strings.HasSuffix(a, "/readyz") }); len(readyz) != stablePolls {
		t.Fatalf("the seam was asked %v, want /readyz %d times", asked, stablePolls)
	}
	asked = slices.DeleteFunc(asked, func(a string) bool { return strings.HasSuffix(a, "/readyz") })
	if len(asked) != 2 || !strings.HasSuffix(asked[0], "/status") || asked[0] != asked[1] { // the probe before the offer, then the backup
		t.Fatalf("the seam was asked %v, want /status twice: the probe, then the backup", asked)
	}
	files, _ := filepath.Glob(filepath.Join(u.backups, "*", backupStatus))
	if len(files) != 1 || !strings.Contains(readFile(t, files[0]), "v9.9.9") {
		t.Errorf("the backup's status.json does not hold the seam's answer: %v", files)
	}
}

// ---- the Windows user is named by its SID ----

const testSID = "S-1-5-21-1111-2222-3333-1001"

func windowsDeps(sid func() (string, error), run func(args []string)) Deps {
	return Deps{
		GOOS:        "windows",
		Getenv:      func(k string) string { return map[string]string{"USERNAME": "tester"}[k] },
		Interactive: func(context.Context, string, []string) error { return nil },
		UserSID:     sid,
		Exec: func(_ context.Context, _ string, args []string, _ io.Reader) ([]byte, error) {
			run(args)
			return nil, nil
		},
	}
}

// A domain or Entra ID account may not resolve by its name in icacls: the grant names the SID of the current user.
func TestRestrictPathGrantsTheSIDOfTheCurrentUserOnWindows(t *testing.T) {
	for dir, want := range map[bool]string{false: "*" + testSID + ":F", true: "*" + testSID + ":(OI)(CI)F"} {
		var got []string
		d := windowsDeps(func() (string, error) { return testSID, nil }, func(args []string) { got = args })
		if err := restrictPath(context.Background(), d, filepath.Join(t.TempDir(), "f"), dir); err != nil {
			t.Fatal(err)
		}
		if len(got) != 4 || got[1] != "/inheritance:r" || got[2] != "/grant:r" || got[3] != want {
			t.Errorf("dir=%v: icacls args %q, want the grant %q", dir, got, want)
		}
		if strings.Contains(strings.Join(got, " "), "tester") {
			t.Errorf("the user name is still in the grant: %q", got)
		}
	}
}

func TestRestrictPathRefusesWhenTheSIDIsUnknownOnWindows(t *testing.T) {
	for name, sid := range map[string]func() (string, error){
		"nil":    nil,
		"error":  func() (string, error) { return "", io.ErrUnexpectedEOF },
		"empty":  func() (string, error) { return "", nil },
		"a name": func() (string, error) { return "tester", nil },
	} {
		ran := false
		d := windowsDeps(sid, func([]string) { ran = true })
		if err := restrictPath(context.Background(), d, filepath.Join(t.TempDir(), "f"), false); err == nil || ran {
			t.Errorf("%s: restrictPath = %v, icacls ran %v; want a refusal before icacls", name, err, ran)
		}
	}
}
