package installer

import (
	"regexp"
	"testing"
)

func TestGeneratedSecrets(t *testing.T) {
	hex := regexp.MustCompile(`^[0-9a-f]+$`)
	for name, tc := range map[string]struct {
		gen  func() (string, error)
		size int
	}{
		"password":  {NewPassword, 48},
		"mcp token": {NewMCPToken, 64},
	} {
		first, err := tc.gen()
		if err != nil {
			t.Fatal(err)
		}
		second, err := tc.gen()
		if err != nil {
			t.Fatal(err)
		}
		if len(first) != tc.size || !hex.MatchString(first) {
			t.Errorf("%s %q: want %d lowercase hex characters", name, first, tc.size)
		}
		if first == second {
			t.Errorf("%s repeated", name)
		}
	}
}

func TestDatabaseURL(t *testing.T) {
	got := DatabaseURL("suiteward_app", "abc123")
	want := "postgres://suiteward_app:abc123@suiteward-db:5432/suiteward?sslmode=disable"
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}
