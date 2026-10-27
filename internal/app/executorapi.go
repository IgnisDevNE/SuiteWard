package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

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
	stopping func() bool
	logger   *slog.Logger
	now      func() time.Time
}

// newExecutorAPI composes the handler served under /executor/v1.
func newExecutorAPI(d executorDeps) (http.Handler, error) {
	_ = d
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }), nil
}
