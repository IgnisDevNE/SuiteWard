package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
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
	Instance(ctx context.Context) (string, error)
}

// executorJobCounter is the part of the execution store /status reads.
type executorJobCounter interface {
	Counts(ctx context.Context) (map[execution.State]int, error)
}

// executorStatus is what /status reports of execution: whether an executor token is configured, how many jobs are in each state, and when
// an executor was last seen (docs/contracts/runtime.md, "HTTP surface").
type executorStatus struct {
	enabled  bool
	jobs     executorJobCounter
	liveness *execution.Liveness
}

// executorJobStates are the states /status counts, each even at zero.
var executorJobStates = []execution.State{execution.StatePending, execution.StateLeased, execution.StateComplete, execution.StateUnavailable, execution.StateCancelled}

// report is the execution block of /status: counts and a time only, never a repository, a path or a test name.
func (e *executorStatus) report(ctx context.Context) (map[string]any, error) {
	counts, err := e.jobs.Counts(ctx)
	if err != nil {
		return nil, fmt.Errorf("count the execution jobs: %w", err)
	}
	jobs := make(map[string]int, len(executorJobStates))
	for _, state := range executorJobStates {
		jobs[string(state)] = counts[state]
	}
	return map[string]any{"enabled": e.enabled, "jobs": jobs, "lastSeenAt": e.liveness.LastSeen().UTC().Format(time.RFC3339)}, nil
}

// api serves /healthz, /readyz, /status, the change report pages, the pull request pages and the redirect of the receipt route. Only fixed reasons reach a
// response; the cause of a failure is logged.
type api struct {
	store          store
	logger         *slog.Logger
	tier           string // the configured installation tier
	owners         ownerReader
	github         *githubStatus   // nil when the GitHub settings are not set
	executorStatus *executorStatus // the execution block of /status: serve always sets it
	executor       http.Handler    // the executor API under /executor/v1; nil without EXECUTOR_TOKEN: not served
	reports        reportSource    // rebuilds the change report of a revision for the report page
	receipts       receiptSource   // builds the page of a pull request
	stopping       atomic.Bool     // set when shutdown begins: readiness turns 503 first
}

func newRouter(a *api) http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", a.healthz)
	r.Get("/readyz", a.readyz)
	r.Get("/status", a.status)
	r.Mount("/executor/v1", a.executorHandler())
	r.Route("/reports", func(r chi.Router) {
		r.Use(reportGuard)
		r.Get("/{owner}/{repo}/{revision}", a.report)
	})
	r.Route("/pulls", func(r chi.Router) {
		r.Use(reportGuard)
		r.Get("/{owner}/{repo}/{pull}", a.pull)
	})
	r.Route("/receipts", func(r chi.Router) { // the route of the receipt page of M2.1, which the revision comments posted before M2.1b link
		r.Use(reportGuard)
		r.Get("/{owner}/{repo}/{pull}", a.receiptMoved)
	})
	return r
}

// executorHandler is the executor API, or, without EXECUTOR_TOKEN, a handler that serves nothing: 404 with an empty body, as for a path that
// was never served.
func (a *api) executorHandler() http.Handler {
	if a.executor != nil {
		return a.executor
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
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
	instance, err := a.store.Instance(ctx)
	if err != nil {
		a.storeFailed(w, r, err)
		return
	}
	enrollment, enrolled, err := a.owners.Current(ctx)
	if err != nil {
		a.storeFailed(w, r, err)
		return
	}
	tier, note := effectiveTier(a.tier, enrollment, enrolled)
	body := map[string]any{
		"version": Version, "schemaVersion": version, "tier": tier, "instance": shortInstance(instance),
		"jobs": perState(jobStates, counts.Jobs), "outbox": perState(outboxStates, counts.Outbox),
	}
	if note != "" {
		body["tierNote"] = note
	}
	if Source != "" {
		body["source"] = Source
	}
	block, err := a.executorStatus.report(ctx)
	if err != nil {
		a.storeFailed(w, r, err)
		return
	}
	body["execution"] = block
	if enrolled {
		body["owner"] = map[string]any{"login": enrollment.Owner.Login, "accountId": enrollment.Owner.AccountID, "enrolledAt": enrollment.EnrolledAt.UTC().Format(time.RFC3339)}
	}
	if a.github != nil {
		if window, known := a.github.budget.Window(); known {
			body["budget"] = map[string]any{"remaining": window.Remaining, "reset": window.Reset.UTC().Format(time.RFC3339)}
		}
		if names := unconfiguredRepositories(a.github.repos, enrollment); len(names) > 0 {
			body["unconfiguredRepositories"] = names
		}
		protections, err := storedProtection(ctx, a.owners, a.github.ids, a.github.repos)
		if err != nil {
			a.storeFailed(w, r, err)
			return
		}
		if body["github"], err = a.github.report(ctx, protections); err != nil {
			a.storeFailed(w, r, err)
			return
		}
	}
	a.respond(w, r, http.StatusOK, body)
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
