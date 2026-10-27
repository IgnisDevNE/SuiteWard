package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// The executor API is the service's side of the executor wire (docs/contracts/runtime.md, "The executor API"): the five routes under
// /executor/v1 that an executor, which never holds a GitHub token, uses to lease a job, read its sources, keep its lease and report
// what it saw. This file composes the handler and holds what every route shares; the routes are in executorapi_*.go.

// The bounds of the wire (docs/contracts/runtime.md; the † numbers are A0's).
const (
	maxTreeEntries  = 50_000   // the tree entries of one commit
	maxDeclaration  = 1 << 20  // the declaration of a job
	maxLeaseAnswer  = 16 << 20 // a lease answer
	maxResultBody   = 4 << 20  // a result
	maxLeaseRequest = 1 << 10  // the body of a lease request, which has none
	maxDownloadTime = 10 * time.Minute
	leaseHeader     = "Suiteward-Lease"
)

// leaseReadTimeout bounds one lease request: the tree reads run inside the store's transaction and the listener closes a response after 30 s.
const leaseReadTimeout = 20 * time.Second

// treeEntry is one entry of a commit's tree as the lease answer lists it: the contract's {path, mode, sha}.
type treeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
}

// treeReader reads the tree entries of a commit through the GitHub budget: blobs and submodules, never directories.
type treeReader func(ctx context.Context, repo, commitSHA string) ([]treeEntry, error)

// archiveReader streams the tar.gz of a commit to dst, at most maxBytes (github.Reader.Archive).
type archiveReader func(ctx context.Context, repo, commitSHA string, dst io.Writer, maxBytes int64) (int64, error)

// executorContent is the content store the executor API reads: the manifest of a job, its declaration and its files.
type executorContent interface {
	governance.ContentReader
	Size(ctx context.Context, digest artifact.Digest) (int64, error)
}

// executorDeps is what the executor API is composed from.
type executorDeps struct {
	token    []byte // the bearer token
	store    execution.Store
	liveness *execution.Liveness
	times    config.ExecutorTimes
	content  executorContent
	owners   ownerReader
	trees    treeReader
	archives archiveReader
	wake     func(ctx context.Context) error // wakes github_checks after a result is committed
	stopping func() bool                     // the service is shutting down
	logger   *slog.Logger
	now      func() time.Time
}

// validate reports the dependency that is missing: the composition is the trust boundary of the API.
func (d executorDeps) validate() error {
	missing := ""
	switch {
	case len(d.token) == 0:
		missing = "token"
	case d.store == nil:
		missing = "execution store"
	case d.liveness == nil:
		missing = "liveness record"
	case d.times.JobTimeout <= 0 || d.times.GitHubWait <= 0:
		missing = "job timeout and GitHub wait"
	case d.content == nil:
		missing = "content store"
	case d.owners == nil:
		missing = "owner reader"
	case d.trees == nil:
		missing = "tree reader"
	case d.archives == nil:
		missing = "archive reader"
	case d.wake == nil:
		missing = "wake-up"
	case d.stopping == nil:
		missing = "shutdown flag"
	case d.logger == nil:
		missing = "logger"
	case d.now == nil:
		missing = "clock"
	}
	if missing != "" {
		return fmt.Errorf("compose the executor API: no %s", missing)
	}
	return nil
}

// executorAPI serves /executor/v1.
type executorAPI struct {
	executorDeps
	tokenHash [sha256.Size]byte
}

// newExecutorAPI composes the handler served under /executor/v1.
func newExecutorAPI(d executorDeps) (http.Handler, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	h := &executorAPI{executorDeps: d, tokenHash: sha256.Sum256(d.token)}
	r := chi.NewRouter()
	r.Use(h.guard)
	r.NotFound(h.notFound)
	r.MethodNotAllowed(h.notFound) // the fixed codes of the contract have no 405
	r.Post("/lease", h.lease)
	r.Route("/jobs/{id}", func(r chi.Router) {
		r.Get("/archive/{side}", h.archive)
		r.Get("/files/{digest}", h.file)
		r.Post("/heartbeat", h.heartbeat)
	})
	return r, nil
}

// guard is what every route has before its own work: the answer is never cached, the token is checked first and in constant time (of the digests,
// so that the length of the token is not timed either), and a service that is stopping answers nothing else.
func (h *executorAPI) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !h.authorized(r) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized) // no detail
			return
		}
		if h.stopping() {
			h.fail(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *executorAPI) authorized(r *http.Request) bool {
	scheme, credential, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return false
	}
	got := sha256.Sum256([]byte(credential))
	return subtle.ConstantTimeCompare(got[:], h.tokenHash[:]) == 1
}

func (h *executorAPI) notFound(w http.ResponseWriter, _ *http.Request) {
	h.fail(w, http.StatusNotFound, "not-found")
}

// fail answers with a fixed error code, never an echo of the request.
func (*executorAPI) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + code + `"}` + "\n")) // a failed write is the client leaving
}

// internal answers 500 and logs the cause, which no answer carries.
func (h *executorAPI) internal(w http.ResponseWriter, r *http.Request, what string, err error) {
	h.logger.ErrorContext(r.Context(), "executor API: "+what, "path", r.URL.Path, "error", err)
	h.fail(w, http.StatusInternalServerError, "internal")
}

// reply answers with a JSON body.
func (h *executorAPI) reply(w http.ResponseWriter, r *http.Request, status int, body any) {
	encoded, err := encodeJSON(body)
	if err != nil {
		h.internal(w, r, "encode an answer", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded) // a failed write is the client leaving
}

// encodeJSON is the encoding of every answer: without the HTML escapes, which would grow a path for nothing.
func encodeJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// decodeStrict decodes one JSON object into v and nothing else: an unknown field, a trailing value and a value that is not an object are errors.
func decodeStrict(body []byte, v any) error {
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("not a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("a value follows the object")
	}
	return nil
}

// jobID reads the {id} of a route: a positive integer in its canonical spelling, or the job is unknown.
func jobID(r *http.Request) (int64, bool) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 || strconv.FormatInt(id, 10) != raw {
		return 0, false
	}
	return id, true
}

// heartbeat extends the lease of the job, never beyond its deadline. It is the executor being seen, whatever it answers.
func (h *executorAPI) heartbeat(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	h.liveness.Saw(now)
	id, ok := jobID(r)
	if !ok {
		h.fail(w, http.StatusNotFound, "not-found")
		return
	}
	expires, err := h.store.Heartbeat(r.Context(), id, r.Header.Get(leaseHeader), now)
	switch {
	case errors.Is(err, execution.ErrNotFound):
		h.fail(w, http.StatusNotFound, "not-found")
	case errors.Is(err, execution.ErrLeaseLost):
		h.fail(w, http.StatusConflict, "lease-lost")
	case err != nil:
		h.internal(w, r, "extend a lease", err)
	default:
		h.reply(w, r, http.StatusOK, map[string]string{"leaseExpiresAt": expires.UTC().Format(time.RFC3339)})
	}
}
