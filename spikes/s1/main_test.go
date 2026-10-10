package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Against a fake GitHub: first cycle creates an in_progress check run, the second is all 304 and sends nothing,
// an approval turns it into a PATCH to success, and a new head sha gets a new check run.
func TestCycleCheckLifecycle(t *testing.T) {
	head := "aaaa"
	var checkCalls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/access_tokens"):
			_, _ = w.Write([]byte(`{"token":"t","expires_at":"2999-01-01T00:00:00Z"}`))
		case strings.Contains(r.URL.Path, "/check-runs"):
			checkCalls = append(checkCalls, r.Method+" "+string(body))
			_, _ = w.Write([]byte(`{"id":99}`))
		default:
			etag := `"` + r.URL.Path + head + `"`
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", etag)
			if strings.HasSuffix(r.URL.Path, "/pulls") {
				_, _ = w.Write([]byte(`[{"number":5,"head":{"sha":"` + head + `"}}]`))
			} else {
				_, _ = w.Write([]byte(`{"tree":[{"path":"tests/a_test.go","type":"blob","sha":"s1"}]}`))
			}
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	a := &app{
		cfg:       config{repo: "o/r", protectedPrefix: "tests/", checkName: "n", stateFile: filepath.Join(t.TempDir(), "state.json")},
		log:       &logger{w: &out, start: time.Now()},
		st:        &state{Etags: map[string]string{}, Pulls: map[int]*pullState{}, Promotions: []json.RawMessage{}},
		published: map[int]string{}, started: time.Now(),
	}
	a.c = testClient(t, srv.URL, &out)
	a.c.etags = a.st.Etags
	cycle := func() {
		t.Helper()
		if err := a.cycle(context.Background()); err != nil {
			t.Fatal(err)
		}
	}

	cycle()
	if len(checkCalls) != 1 || !strings.HasPrefix(checkCalls[0], "POST ") || !strings.Contains(checkCalls[0], `"status":"in_progress"`) {
		t.Fatalf("first cycle: %q", checkCalls)
	}
	cycle()
	if len(checkCalls) != 1 {
		t.Fatalf("unchanged cycle published again: %q", checkCalls)
	}
	if got := a.c.calls; len(got) != 2 || got[0].Status != 304 || got[1].Status != 304 {
		t.Fatalf("second cycle calls %+v, want two 304s", got)
	}

	a.st.Pulls[5].ApprovedCommentID = 1234
	cycle()
	if len(checkCalls) != 2 || !strings.HasPrefix(checkCalls[1], "PATCH ") || !strings.Contains(checkCalls[1], `"conclusion":"success"`) {
		t.Fatalf("approval cycle: %q", checkCalls)
	}

	head = "bbbb"
	cycle()
	if len(checkCalls) != 3 || !strings.HasPrefix(checkCalls[2], "POST ") || !strings.Contains(checkCalls[2], `"head_sha":"bbbb"`) {
		t.Fatalf("new head cycle: %q", checkCalls)
	}
}
