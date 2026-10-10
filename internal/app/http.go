package app

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
)

// store is the part of *postgres.Store the HTTP surface reads.
type store interface {
	SchemaVersion(ctx context.Context) (int64, error)
	Counts(ctx context.Context) (postgres.Counts, error)
}

// api serves /healthz, /readyz and /status.
type api struct {
	store    store
	logger   *slog.Logger
	stopping atomic.Bool // set when shutdown begins: readiness turns 503 first
}

func newRouter(a *api) http.Handler {
	notImplemented := func(w http.ResponseWriter, _ *http.Request) { http.Error(w, errUnimplemented.Error(), http.StatusNotImplemented) }
	r := chi.NewRouter()
	r.Get("/healthz", notImplemented)
	r.Get("/readyz", notImplemented)
	r.Get("/status", notImplemented)
	return r
}
