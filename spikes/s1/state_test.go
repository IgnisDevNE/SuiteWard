package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadStateValidatesPulls(t *testing.T) {
	load := func(content string) (*state, error) {
		p := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return loadState(p)
	}
	if _, err := load(`{"pulls":{"5":null}}`); err == nil || !strings.Contains(err.Error(), "pull 5") {
		t.Fatalf("a null pull entry must be rejected, got %v", err)
	}
	st, err := load(`{"pulls":{"5":{"head_sha":"a"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	if st.Pulls[5].Judged == nil {
		t.Fatal("a state file from before the judged field must load with an empty judged map")
	}
}
