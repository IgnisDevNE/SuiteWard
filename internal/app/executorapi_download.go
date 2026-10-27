package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// leasedJob reads the job of the route under /jobs/{id} and checks that the request holds its lease: 404 for a job that is not there, 409 for a
// lease that is not the job's current one, or has run out, or a job that is no longer leased. It answers and returns false when the request ends.
func (h *executorAPI) leasedJob(w http.ResponseWriter, r *http.Request, now time.Time) (execution.Job, bool) {
	id, ok := jobID(r)
	if !ok {
		h.fail(w, http.StatusNotFound, "not-found")
		return execution.Job{}, false
	}
	job, found, err := h.store.Job(r.Context(), id)
	switch {
	case err != nil:
		h.internal(w, r, "read a job", err)
		return execution.Job{}, false
	case !found:
		h.fail(w, http.StatusNotFound, "not-found")
		return execution.Job{}, false
	case job.CheckLease(r.Header.Get(leaseHeader), now) != nil:
		h.fail(w, http.StatusConflict, "lease-lost")
		return execution.Job{}, false
	}
	return job, true
}

// downloadDeadline is the bound of a download: the job's remaining time, ten minutes at most. The listener's own 30 s write bound is moved to it
// for this request, and the same instant bounds what the request reads. It answers 500 when the response cannot take a deadline of its own.
func (h *executorAPI) downloadDeadline(w http.ResponseWriter, r *http.Request, job execution.Job, now time.Time) (time.Time, bool) {
	deadline := now.Add(maxDownloadTime)
	if job.DeadlineAt.Before(deadline) {
		deadline = job.DeadlineAt
	}
	if err := http.NewResponseController(w).SetWriteDeadline(deadline); err != nil {
		h.internal(w, r, "extend the write deadline of a download", err)
		return time.Time{}, false
	}
	return deadline, true
}

// archive streams the tar.gz of the head or the base of a job from GitHub, through the reader with archiveBytes as the limit. The status is sent
// with the first byte: before it a failure is still an answer, after it the response is aborted.
func (h *executorAPI) archive(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	job, ok := h.leasedJob(w, r, now)
	if !ok {
		return
	}
	sha := ""
	switch side := chi.URLParam(r, "side"); {
	case side == "head":
		sha = job.HeadSHA
	case side == "base" && job.Plan == execution.PlanReceipt:
		sha = job.BaseSHA
	default:
		h.fail(w, http.StatusNotFound, "not-found")
		return
	}
	repo, err := h.repoName(r.Context(), job.RepositoryID)
	if err != nil {
		h.internal(w, r, "name the repository of an archive", err)
		return
	}
	deadline, ok := h.downloadDeadline(w, r, job, now)
	if !ok {
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), deadline)
	defer cancel()
	stream := &archiveStream{w: w, rc: http.NewResponseController(w)}
	_, err = h.archives(ctx, repo, sha, stream, jobLimits.ArchiveBytes)
	switch {
	case err == nil:
		stream.start() // an archive with no byte is still a 200
	case stream.started:
		h.logGitHubFailure(ctx, "archive", job.ID, err)
		panic(http.ErrAbortHandler) // the status is out: the connection ends, and the executor sees a broken stream
	case errors.Is(err, rest.ErrTooLarge):
		h.fail(w, http.StatusRequestEntityTooLarge, "too-large")
	case errors.Is(err, github.ErrInvalidArgument):
		h.internal(w, r, "name an archive", err) // the job holds a commit and a repository that were checked: this is a bug
	default:
		h.logGitHubFailure(ctx, "archive", job.ID, err)
		h.fail(w, http.StatusBadGateway, "github")
	}
}

// archiveStream is where the archive is copied to. The 200 and its headers go out with the first byte, and are flushed at once so that the answer
// is chunked and has no Content-Length, which the archive does not have either.
type archiveStream struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	started bool
	err     error // the flush of the status failed
}

func (s *archiveStream) Write(p []byte) (int, error) {
	s.start()
	if s.err != nil {
		return 0, s.err
	}
	return s.w.Write(p)
}

func (s *archiveStream) start() {
	if s.started {
		return
	}
	s.started = true
	s.w.Header().Set("Content-Type", "application/gzip")
	s.w.WriteHeader(http.StatusOK)
	s.err = s.rc.Flush()
}

// file serves one file of the job's manifest from the content store: the bytes are the store's, checked against their digest by the store, and a
// digest that the manifest does not list is never read.
func (h *executorAPI) file(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	job, ok := h.leasedJob(w, r, now)
	if !ok {
		return
	}
	digest, err := artifact.ParseDigest(chi.URLParam(r, "digest"))
	if err != nil {
		h.fail(w, http.StatusNotFound, "not-found") // a text that is no digest is none of the manifest's
		return
	}
	manifest, err := h.manifestOf(r.Context(), job)
	if err != nil {
		h.internal(w, r, "read the manifest of a job", err)
		return
	}
	if !lists(manifest, digest) {
		h.fail(w, http.StatusNotFound, "not-found")
		return
	}
	if _, ok := h.downloadDeadline(w, r, job, now); !ok {
		return
	}
	size, err := h.content.Size(r.Context(), digest)
	if err != nil {
		h.internal(w, r, "size a file of the manifest", err)
		return
	}
	if size > jobLimits.FileBytes {
		h.fail(w, http.StatusRequestEntityTooLarge, "too-large")
		return
	}
	content, err := h.content.Read(r.Context(), digest)
	if err != nil {
		h.internal(w, r, "read a file of the manifest", err) // the store has no valid content for a digest that the manifest names
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(content)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, bytes.NewReader(content)) // a failed write is the client leaving
}

// lists reports whether a file of the manifest has the digest.
func lists(manifest artifact.Manifest, digest artifact.Digest) bool {
	for _, entry := range manifest.Entries() {
		if entry.Content == digest {
			return true
		}
	}
	return false
}
