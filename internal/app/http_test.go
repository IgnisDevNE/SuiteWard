package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
)

type fakeStore struct {
	version    int64
	versionErr error
	counts     postgres.Counts
	countsErr  error
}

func (f fakeStore) SchemaVersion(context.Context) (int64, error)    { return f.version, f.versionErr }
func (f fakeStore) Counts(context.Context) (postgres.Counts, error) { return f.counts, f.countsErr }

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func call(t *testing.T, a *api, method, path string) (int, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	newRouter(a).ServeHTTP(recorder, httptest.NewRequest(method, path, nil))
	return recorder.Code, strings.TrimSpace(recorder.Body.String())
}

func TestHealthzAnswersWhileTheProcessServes(t *testing.T) {
	// Liveness does not consult the store: even a broken one is alive.
	a := &api{store: fakeStore{versionErr: errors.New("down")}, logger: quietLogger()}
	if code, body := call(t, a, http.MethodGet, "/healthz"); code != http.StatusOK || body != `{"status":"ok"}` {
		t.Fatalf("GET /healthz = %d %s; want 200 {\"status\":\"ok\"}", code, body)
	}
}

func TestReadyzReportsReadinessWithoutLeakingCauses(t *testing.T) {
	const secretCause = "dial tcp db.internal:5432: password=hunter2"
	tests := []struct {
		name     string
		store    fakeStore
		stopping bool
		code     int
		body     string
	}{
		{"ready", fakeStore{version: 10}, false, 200, `{"schemaVersion":10,"status":"ready"}`},
		{"shutting down", fakeStore{version: 10}, true, 503, `{"reason":"shutting down","status":"unavailable"}`},
		{"database unreachable", fakeStore{versionErr: errors.New(secretCause)}, false, 503, `{"reason":"database unavailable","status":"unavailable"}`},
		{"schema mismatch", fakeStore{versionErr: fmt.Errorf("%w: schema version 11", postgres.ErrSchemaNotReady)}, false, 503, `{"reason":"schema not ready","status":"unavailable"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := &api{store: test.store, logger: quietLogger()}
			a.stopping.Store(test.stopping)
			code, body := call(t, a, http.MethodGet, "/readyz")
			if code != test.code {
				t.Fatalf("GET /readyz = %d; want %d", code, test.code)
			}
			if !jsonEqual(t, body, test.body) {
				t.Fatalf("GET /readyz body = %s; want %s", body, test.body)
			}
			if strings.Contains(body, "hunter2") {
				t.Fatalf("GET /readyz leaked the cause: %s", body)
			}
		})
	}
}

func TestStatusReportsEveryStateEvenAtZero(t *testing.T) {
	a := &api{logger: quietLogger(), store: fakeStore{version: 10, counts: postgres.Counts{
		Jobs:   map[string]int64{"completed": 3, "discarded": 1, "cancelled": 9, "pending": 8},
		Outbox: map[string]int64{"delivered": 2},
	}}}
	code, body := call(t, a, http.MethodGet, "/status")
	want := `{"version":"` + Version + `","schemaVersion":10,
		"jobs":{"available":0,"running":0,"retryable":0,"scheduled":0,"completed":3,"discarded":1},
		"outbox":{"pending":0,"delivered":2,"failed":0}}`
	if code != http.StatusOK || !jsonEqual(t, body, want) {
		t.Fatalf("GET /status = %d %s; want 200 %s", code, body, want)
	}
}

func TestStatusIsUnavailableWhenTheStoreFails(t *testing.T) {
	const secretCause = "dial tcp db.internal:5432: password=hunter2"
	for name, store := range map[string]fakeStore{
		"schema version": {versionErr: errors.New(secretCause)},
		"counts":         {version: 10, countsErr: errors.New(secretCause)},
	} {
		t.Run(name, func(t *testing.T) {
			code, body := call(t, &api{store: store, logger: quietLogger()}, http.MethodGet, "/status")
			if code != http.StatusServiceUnavailable || strings.Contains(body, "hunter2") || !strings.Contains(body, "database unavailable") {
				t.Fatalf("GET /status = %d %s; want 503 database unavailable without the cause", code, body)
			}
		})
	}
}

func TestOnlyGetOnTheThreePaths(t *testing.T) {
	a := &api{store: fakeStore{version: 10}, logger: quietLogger()}
	for _, path := range []string{"/healthz", "/readyz", "/status"} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			if code, _ := call(t, a, method, path); code != http.StatusMethodNotAllowed {
				t.Errorf("%s %s = %d; want 405", method, path, code)
			}
		}
	}
	for _, path := range []string{"/", "/metrics", "/status/extra", "/healthz/"} {
		if code, _ := call(t, a, http.MethodGet, path); code != http.StatusNotFound {
			t.Errorf("GET %s = %d; want 404", path, code)
		}
	}
}

type brokenWriter struct{ header http.Header }

func (w brokenWriter) Header() http.Header     { return w.header }
func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("client went away") }
func (brokenWriter) WriteHeader(int)           {}

func TestAFailedResponseWriteIsLogged(t *testing.T) {
	var logs bytes.Buffer
	a := &api{store: fakeStore{version: 10}, logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	newRouter(a).ServeHTTP(brokenWriter{header: http.Header{}}, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if !strings.Contains(logs.String(), "client went away") {
		t.Fatalf("the failed write was not logged: %q", logs.String())
	}
}

// jsonEqual compares two JSON documents regardless of key order and spacing.
func jsonEqual(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("not JSON: %s", got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("not JSON: %s", want)
	}
	return reflect.DeepEqual(g, w)
}
