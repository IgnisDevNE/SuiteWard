package goprofile

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var imageRef = regexp.MustCompile(`^(?P<name>[^:@]+):(?P<tag>[^@]+)@(?P<digest>sha256:[a-f0-9]{64})$`)

// The sandbox's Go is the Go that builds the release: Image and the builder stage of deploy/Containerfile are one pin.
func TestImageIsTheContainerfileBuilder(t *testing.T) {
	raw, err := os.ReadFile("../../../deploy/Containerfile")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	version := regexp.MustCompile(`(?m)^ARG GO_VERSION=(\S+)\s*$`).FindStringSubmatch(text)
	builder := regexp.MustCompile(`(?m)^FROM\s+(?:--\S+\s+)*(\S+)\s+AS\s+build\s*$`).FindStringSubmatch(text)
	if version == nil || builder == nil {
		t.Fatal("the Containerfile has no GO_VERSION default or no build stage")
	}
	want := imageRef.FindStringSubmatch(strings.ReplaceAll(builder[1], "${GO_VERSION}", version[1]))
	got := imageRef.FindStringSubmatch(Image)
	if want == nil || got == nil {
		t.Fatalf("not name:tag@sha256:digest: builder %q, Image %q", builder[1], Image)
	}
	for i, part := range imageRef.SubexpNames()[1:] {
		if got[i+1] != want[i+1] {
			t.Errorf("Image %s = %q, the Containerfile builder's is %q", part, got[i+1], want[i+1])
		}
	}
}
