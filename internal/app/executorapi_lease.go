package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// declarationPath is the file that holds a repository's declaration; the lease answer carries its bytes.
const declarationPath = ".suiteward.yml"

// jobLimits are the limits of every job (docs/contracts/runtime.md, "The executor API", the `limits` field).
var jobLimits = struct {
	ArchiveBytes       int64 `json:"archiveBytes"`
	FileBytes          int64 `json:"fileBytes"`
	TestTimeoutSeconds int   `json:"testTimeoutSeconds"`
	MemoryMiB          int   `json:"memoryMiB"`
	CPUs               int   `json:"cpus"`
	PIDs               int   `json:"pids"`
	TmpfsMiB           int   `json:"tmpfsMiB"`
	PrefetchModules    int   `json:"prefetchModules"`
	PrefetchBytes      int64 `json:"prefetchBytes"`
	Reruns             int   `json:"reruns"`
}{ArchiveBytes: 268435456, FileBytes: 8388608, TestTimeoutSeconds: 600, MemoryMiB: 3072, CPUs: 4, PIDs: 512, TmpfsMiB: 1024, PrefetchModules: 2000, PrefetchBytes: 536870912, Reruns: 2}

// leaseAnswer is the 200 answer of POST /lease.
type leaseAnswer struct {
	JobID          int64         `json:"jobId"`
	LeaseID        string        `json:"leaseId"`
	LeaseExpiresAt string        `json:"leaseExpiresAt"`
	DeadlineAt     string        `json:"deadlineAt"`
	Plan           string        `json:"plan"`
	Pull           int           `json:"pull"`
	Profile        leaseProfile  `json:"profile"`
	Head           leaseSide     `json:"head"`
	Base           *leaseSide    `json:"base,omitempty"` // exactly for a receipt
	Manifest       leaseManifest `json:"manifest"`
	Declaration    *string       `json:"declaration,omitempty"` // absent when the manifest has none
	Limits         any           `json:"limits"`
}

type leaseProfile struct {
	ID    string `json:"id"`
	Image string `json:"image"`
}

type leaseSide struct {
	SHA  string      `json:"sha"`
	Tree []treeEntry `json:"tree"`
}

type leaseManifest struct {
	Source string         `json:"source"`
	Digest string         `json:"digest"`
	Files  []manifestFile `json:"files"`
}

type manifestFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// lease grants at most one job. The tree entries of each commit are read while the store holds the job (the prepare hook), so that a read
// that fails or a job that is too large never costs an attempt. It is the executor being seen, whatever it answers.
func (h *executorAPI) lease(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	h.liveness.Saw(now)
	if !h.noLeaseBody(w, r) {
		h.fail(w, http.StatusBadRequest, "invalid")
		return
	}
	leaseID, err := newLeaseID()
	if err != nil {
		h.internal(w, r, "make a lease id", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), leaseReadTimeout)
	defer cancel()
	readBy := time.Now().Add(h.treeTimeout) // the tree reads of the whole lease end here, whatever the clock of the jobs says
	var prepared *leaseAnswer               // prepare may run for more than one job: the last call is the job that is granted
	job, granted, err := h.store.Lease(ctx, execution.LeaseRequest{Now: now, LeaseID: leaseID, JobTimeout: h.times.JobTimeout, GitHubWait: h.times.GitHubWait},
		func(ctx context.Context, job execution.Job) error {
			var err error
			prepared, err = h.prepare(ctx, job, leaseID, readBy)
			return err
		})
	switch {
	case errors.Is(err, execution.ErrGitHubUnavailable):
		h.fail(w, http.StatusBadGateway, "github")
	case err != nil:
		h.leaseFailed(w, r, err)
	case !granted:
		w.WriteHeader(http.StatusNoContent)
	case prepared == nil || prepared.JobID != job.ID:
		h.internal(w, r, "lease a job", fmt.Errorf("the job granted is %d, and the one prepared is not it", job.ID))
	default:
		prepared.LeaseExpiresAt, prepared.DeadlineAt = rfc3339(job.LeaseExpiresAt), rfc3339(job.DeadlineAt)
		h.reply(w, r, http.StatusOK, prepared)
	}
}

// leaseFailed answers 500 for a lease that failed on the service's side, and logs it once: with the job, when a job could not be prepared.
func (h *executorAPI) leaseFailed(w http.ResponseWriter, r *http.Request, err error) {
	var failed *jobError
	if errors.As(err, &failed) {
		h.internal(w, r, "prepare a job for a lease", failed.err, "job", failed.job)
		return
	}
	h.internal(w, r, "lease a job", err)
}

// noLeaseBody reports whether the request has no body or the empty object: any other body is refused.
func (*executorAPI) noLeaseBody(w http.ResponseWriter, r *http.Request) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLeaseRequest))
	if err != nil {
		return false
	}
	var none struct{}
	return len(bytes.TrimSpace(body)) == 0 || decodeStrict(body, &none) == nil
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// newLeaseID is 32 lower-case hexadecimal digits, new for each grant.
func newLeaseID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// prepare reads what the answer for the job holds and refuses a job that is too large: the manifest and the declaration from the content store,
// then the tree of each commit from GitHub. A GitHub failure wraps ErrGitHubUnavailable and a limit wraps ErrTooLarge; any other error is the
// service's own and ends the request with nothing written. The answer's lease times are placeholders of their real length.
func (h *executorAPI) prepare(ctx context.Context, job execution.Job, leaseID string, readBy time.Time) (*leaseAnswer, error) {
	answer, err := h.build(ctx, job, leaseID, readBy)
	if errors.Is(err, execution.ErrTooLarge) {
		h.logger.InfoContext(ctx, "executor API: a job is too large to lease", "job", job.ID, "reason", err)
	}
	if err != nil {
		return nil, &jobError{job: job.ID, err: err}
	}
	return answer, nil
}

// jobError says which job a lease could not prepare. It unwraps to the cause, so the store still tells a GitHub failure and a limit from the rest, and
// the request that ends with the failure can log the job with it: the queue is stuck on a job that cannot be prepared.
type jobError struct {
	job int64
	err error
}

func (e *jobError) Error() string { return fmt.Sprintf("job %d: %v", e.job, e.err) }

func (e *jobError) Unwrap() error { return e.err }

func (h *executorAPI) build(ctx context.Context, job execution.Job, leaseID string, readBy time.Time) (*leaseAnswer, error) {
	repo, err := h.repoName(ctx, job.RepositoryID)
	if err != nil {
		return nil, err
	}
	manifest, declaration, err := h.governing(ctx, job)
	if err != nil {
		return nil, err
	}
	answer := &leaseAnswer{JobID: job.ID, LeaseID: leaseID, LeaseExpiresAt: rfc3339(time.Time{}), DeadlineAt: rfc3339(time.Time{}), Plan: string(job.Plan), Pull: job.PullNumber,
		Profile: leaseProfile{ID: job.Profile, Image: goprofile.Image}, Manifest: manifest, Declaration: declaration, Limits: jobLimits}
	if answer.Head, err = h.side(ctx, job, repo, job.HeadSHA, readBy); err != nil {
		return nil, err
	}
	if job.Plan == execution.PlanReceipt {
		base, err := h.side(ctx, job, repo, job.BaseSHA, readBy)
		if err != nil {
			return nil, err
		}
		answer.Base = &base
	}
	encoded, err := encodeJSON(answer)
	if err != nil {
		return nil, fmt.Errorf("encode the lease answer of job %d: %w", job.ID, err)
	}
	if len(encoded) > maxLeaseAnswer {
		return nil, fmt.Errorf("%w: the answer is %d bytes, over %d", execution.ErrTooLarge, len(encoded), maxLeaseAnswer)
	}
	return answer, nil
}

// side reads the tree entries of one commit of the job.
func (h *executorAPI) side(ctx context.Context, job execution.Job, repo, sha string, readBy time.Time) (leaseSide, error) {
	ctx, cancel := context.WithDeadline(ctx, readBy)
	defer cancel()
	entries, err := h.trees(ctx, repo, sha)
	switch {
	case errors.Is(err, github.ErrTreeTruncated):
		return leaseSide{}, fmt.Errorf("%w: GitHub truncated the tree", execution.ErrTooLarge)
	case err != nil:
		h.logGitHubFailure(ctx, "tree", job.ID, err)
		return leaseSide{}, fmt.Errorf("%w: the tree could not be read", execution.ErrGitHubUnavailable)
	case len(entries) > maxTreeEntries:
		return leaseSide{}, fmt.Errorf("%w: %d tree entries, over %d", execution.ErrTooLarge, len(entries), maxTreeEntries)
	case slices.ContainsFunc(entries, func(e treeEntry) bool { return len(e.Path) > maxTreePath }):
		return leaseSide{}, fmt.Errorf("%w: a tree entry has a path over %d bytes", execution.ErrTooLarge, maxTreePath)
	}
	if entries == nil { // the reader is a boundary: an empty tree is a list in the answer, not null
		entries = []treeEntry{}
	}
	return leaseSide{SHA: sha, Tree: entries}, nil
}

// governing reads the manifest that the job runs against and the declaration it names, from the content store. A declaration over 1 MiB is too
// large. A manifest or a file that the store cannot give is the service's own fault.
func (h *executorAPI) governing(ctx context.Context, job execution.Job) (leaseManifest, *string, error) {
	manifest, err := h.manifestOf(ctx, job)
	if err != nil {
		return leaseManifest{}, nil, err
	}
	out := leaseManifest{Source: string(job.GoverningSource), Digest: job.GoverningDigest.String(), Files: []manifestFile{}}
	var declaration *string
	for _, entry := range manifest.Entries() {
		out.Files = append(out.Files, manifestFile{Path: entry.Path, Digest: entry.Content.String()})
		if entry.Path == declarationPath {
			if declaration, err = h.declaration(ctx, entry.Content); err != nil {
				return leaseManifest{}, nil, err
			}
		}
	}
	return out, declaration, nil
}

// manifestOf reads the manifest that the job runs against from the content store. The checks job keeps it there under its own digest.
func (h *executorAPI) manifestOf(ctx context.Context, job execution.Job) (artifact.Manifest, error) {
	raw, err := h.content.Read(ctx, job.GoverningDigest)
	if err != nil {
		return artifact.Manifest{}, fmt.Errorf("read the manifest %s of job %d: %w", job.GoverningDigest, job.ID, err)
	}
	manifest, err := artifact.ParseManifest(raw)
	if err != nil || manifest.Digest() != job.GoverningDigest {
		return artifact.Manifest{}, fmt.Errorf("the manifest %s of job %d is not the encoding of that digest: %v", job.GoverningDigest, job.ID, err)
	}
	return manifest, nil
}

// declaration is the base64 of the declaration's bytes, refused before it is read when it is over 1 MiB.
func (h *executorAPI) declaration(ctx context.Context, digest artifact.Digest) (*string, error) {
	size, err := h.content.Size(ctx, digest)
	if err != nil {
		return nil, fmt.Errorf("size the declaration %s: %w", digest, err)
	}
	if size > maxDeclaration {
		return nil, fmt.Errorf("%w: the declaration is %d bytes, over %d", execution.ErrTooLarge, size, maxDeclaration)
	}
	content, err := h.content.Read(ctx, digest)
	if err != nil {
		return nil, fmt.Errorf("read the declaration %s: %w", digest, err)
	}
	encoded := base64.StdEncoding.EncodeToString(content)
	return &encoded, nil
}

// repoName is the owner/name of an enrolled repository.
func (h *executorAPI) repoName(ctx context.Context, repositoryID int64) (string, error) {
	state, enrolled, err := h.owners.Current(ctx)
	if err != nil {
		return "", fmt.Errorf("read the enrollment: %w", err)
	}
	if !enrolled {
		return "", errors.New("no owner is enrolled, so no repository can be read")
	}
	for _, project := range state.Projects {
		if project.RepositoryID == repositoryID {
			return project.Repo, nil
		}
	}
	return "", fmt.Errorf("repository %d is not enrolled", repositoryID)
}

// logGitHubFailure logs a failed GitHub read by its classification, never by the text of the error: a GitHub answer or a redirect's address
// does not belong in a log.
func (h *executorAPI) logGitHubFailure(ctx context.Context, read string, jobID int64, err error) {
	failure := readFailure(err)
	h.logger.WarnContext(ctx, "executor API: GitHub could not be read", "read", read, "job", jobID, "kind", failure.Kind, "status", failure.Status)
}
