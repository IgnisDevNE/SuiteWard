package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
)

// storeTimeout bounds what a probing request asks of the database.
const storeTimeout = 3 * time.Second

// States that /status reports, each even at zero (deploy/smoke.sh reads discarded and failed).
// River's pending and cancelled states are not part of the contract and are not reported.
var (
	jobStates    = []string{"available", "running", "retryable", "scheduled", "completed", "discarded"}
	outboxStates = []string{postgres.OutboxPending, postgres.OutboxDelivered, postgres.OutboxFailed}
)

// store is the part of *postgres.Store the HTTP surface reads.
type store interface {
	SchemaVersion(ctx context.Context) (int64, error)
	Counts(ctx context.Context) (postgres.Counts, error)
}

// api serves /healthz, /readyz and /status. Only fixed reasons reach a
// response; the cause of a failure is logged.
type api struct {
	store    store
	logger   *slog.Logger
	stopping atomic.Bool // set when shutdown begins: readiness turns 503 first
}

func newRouter(a *api) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", a.healthz)
	r.Get("/readyz", a.readyz)
	r.Get("/status", a.status)
	return r
}

func (a *api) healthz(w http.ResponseWriter, r *http.Request) {
	a.respond(w, r, http.StatusOK, map[string]any{"status": "ok"})
}

func (a *api) readyz(w http.ResponseWriter, r *http.Request) {
	if a.stopping.Load() {
		a.unavailable(w, r, "shutting down")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), storeTimeout)
	defer cancel()
	version, err := a.store.SchemaVersion(ctx)
	if err != nil {
		a.storeFailed(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, map[string]any{"status": "ready", "schemaVersion": version})
}

func (a *api) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), storeTimeout)
	defer cancel()
	version, err := a.store.SchemaVersion(ctx)
	if err != nil {
		a.storeFailed(w, r, err)
		return
	}
	counts, err := a.store.Counts(ctx)
	if err != nil {
		a.storeFailed(w, r, err)
		return
	}
	a.respond(w, r, http.StatusOK, map[string]any{
		"version": Version, "schemaVersion": version,
		"jobs": perState(jobStates, counts.Jobs), "outbox": perState(outboxStates, counts.Outbox),
	})
}

// perState lists every state of states with its count, zero when absent.
func perState(states []string, counts map[string]int64) map[string]int64 {
	listed := make(map[string]int64, len(states))
	for _, state := range states {
		listed[state] = counts[state]
	}
	return listed
}

func (a *api) storeFailed(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.WarnContext(r.Context(), "store check failed", "path", r.URL.Path, "error", err)
	if errors.Is(err, postgres.ErrSchemaNotReady) {
		a.unavailable(w, r, "schema not ready")
		return
	}
	a.unavailable(w, r, "database unavailable")
}

func (a *api) unavailable(w http.ResponseWriter, r *http.Request, reason string) {
	a.respond(w, r, http.StatusServiceUnavailable, map[string]any{"status": "unavailable", "reason": reason})
}

func (a *api) respond(w http.ResponseWriter, r *http.Request, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		a.logger.DebugContext(r.Context(), "write response", "path", r.URL.Path, "error", err)
	}
}
