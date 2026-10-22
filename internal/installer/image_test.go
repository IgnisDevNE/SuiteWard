package installer

import (
	"errors"
	"strings"
	"testing"
)

func TestParseImageIsOneRuleForEveryCommand(t *testing.T) {
	hex := strings.Repeat("a", 64)
	for _, test := range []struct {
		name, asked, want string
		ok                bool
	}{
		{"a bare digest is the release image", "sha256:" + hex, releaseImageRepo + "@sha256:" + hex, true},
		{"the release image by digest", releaseImageRepo + "@sha256:" + hex, releaseImageRepo + "@sha256:" + hex, true},
		{"a local image by digest", "localhost/suiteward@sha256:" + hex, "localhost/suiteward@sha256:" + hex, true},
		{"a tag and a digest: the digest decides", releaseImageRepo + ":v1@sha256:" + hex, releaseImageRepo + ":v1@sha256:" + hex, true},
		{"a tag", releaseImageRepo + ":v0.3.0", "", false},
		{"no tag and no digest", releaseImageRepo, "", false},
		{"nothing", "", "", false},
		{"a short digest", "sha256:abc", "", false},
		{"an upper-case digest", "sha256:" + strings.ToUpper(hex), "", false},
		{"another algorithm", "sha512:" + hex, "", false},
		{"text after the digest", "sha256:" + hex + "x", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseImage(test.asked)
			if test.ok && (err != nil || got != test.want) {
				t.Fatalf("parseImage(%q) = %q, %v; want %q", test.asked, got, err, test.want)
			}
			if !test.ok && !errors.Is(err, ErrNotDigestImage) {
				t.Fatalf("parseImage(%q) = %q, %v; want ErrNotDigestImage", test.asked, got, err)
			}
		})
	}
}

// The usage text shows --image sha256:<digest>: the co-located install takes it, as the isolated install and the upgrade do.
func TestInstallColocatedTakesABareDigest(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.install("--runtime", "podman", "--connection", "pm", "--image", testDigest); err != nil {
		t.Fatalf("a bare digest was refused: %v", err)
	}
	if len(h.f.find("pull "+testImage)) == 0 {
		t.Errorf("the release image was not pulled by digest: %v", h.f.lines())
	}
	if got := h.settings().Image; got != testImage {
		t.Errorf("the settings pin %q, want %q", got, testImage)
	}
}

// A bare digest equal to the installed one names the same release: it is a re-run, not an upgrade.
func TestInstallColocatedRerunWithTheBareInstalledDigestIsNotAnUpgrade(t *testing.T) {
	h := newHarness(t, "linux")
	h.existingStack(colocatedSettings(), testImage, nil)
	h.have(KindNetwork, NetworkName)
	h.have(KindVolume, VolumeData, VolumeDBData)
	h.have(kindSecret, SecretDBAuth, SecretDatabaseOwnerURL, SecretDatabaseURL, SecretMCPAuth)
	h.first("secret inspect --showsecret --format {{.SecretData}} "+SecretDatabaseURL, DatabaseURL(appRole, "x")+"\n")
	h.roleExists = true
	h.busy[18081], h.busy[18091] = true, true
	if err := h.install("--runtime", "podman", "--connection", "pm", "--image", testDigest, "--http-port", "18081", "--mcp-port", "18091"); err != nil {
		t.Fatalf("the installed release, named by its bare digest, is a plain re-run: %v", err)
	}
}

func TestInstallIsolatedRefusesAnImageOfAnotherRepository(t *testing.T) {
	f := newIsoFake(t.TempDir())
	_, err := runIsolated(t, f, map[string]string{"image": "localhost/suiteward@" + testDigest}, "")
	if !errors.Is(err, ErrNotDigestImage) && (err == nil || !strings.Contains(err.Error(), releaseImageRepo)) {
		t.Fatalf("the isolated tier runs the release image only: %v", err)
	}
	if f.has("secret create") || f.has("start") {
		t.Fatalf("nothing may change: %v", f.lines())
	}
}
