package installer

import (
	"strings"
	"testing"
)

func TestReleaseImageNamesTheEmbeddedDigest(t *testing.T) {
	valid := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name   string
		digest string
		want   string
		ok     bool
	}{
		{"a development build has no digest", "", "", false},
		{"a valid digest", valid, "ghcr.io/ignisdevne/suiteward@" + valid, true},
		{"a short digest", "sha256:abc", "", false},
		{"an upper-case digest", "sha256:" + strings.Repeat("A", 64), "", false},
		{"a tag", "latest-stable", "", false},
		{"a digest with trailing text", valid + "\n", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := ImageDigest
			t.Cleanup(func() { ImageDigest = old })
			ImageDigest = tc.digest
			got, ok := ReleaseImage()
			if got != tc.want || ok != tc.ok {
				t.Fatalf("ReleaseImage() = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
