package installer

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The host step refuses to run without --sha256sums, so no text of the installer may tell an administrator to run it without the flag.
func TestNoTextTellsTheAdministratorToRunTheHostStepWithoutTheSHA256SUMS(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	command := regexp.MustCompile("`sudo [^`]*install host[^`]*`")
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			for _, m := range command.FindAllString(line, -1) {
				if !strings.Contains(m, "--sha256sums") {
					t.Errorf("%s:%d tells the administrator to run %s, which the host step refuses without --sha256sums", e.Name(), i+1, m)
				}
			}
		}
	}
}
