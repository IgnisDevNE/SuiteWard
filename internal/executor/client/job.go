package client

import (
	"time"
)

// Job is the answer of POST /lease: what the executor runs and the lease that it holds (docs/contracts/runtime.md, "The executor
// API"). Validate has checked its shape, which is all the client knows of it.
type Job struct {
	JobID          int64     `json:"jobId"`
	LeaseID        string    `json:"leaseId"`
	LeaseExpiresAt time.Time `json:"leaseExpiresAt"`
	DeadlineAt     time.Time `json:"deadlineAt"`
	Plan           string    `json:"plan"` // canonical or receipt
	Pull           int       `json:"pull"`
	Profile        Profile   `json:"profile"`
	Head           Commit    `json:"head"`
	Base           *Commit   `json:"base,omitempty"` // present exactly for a receipt
	Manifest       Manifest  `json:"manifest"`
	// Declaration is the raw .suiteward.yml that Manifest names; empty when the manifest has none.
	Declaration []byte `json:"declaration,omitempty"`
	Limits      Limits `json:"limits"`
}

// The plans of a job.
const (
	PlanCanonical = "canonical"
	PlanReceipt   = "receipt"
)

// Profile is the language profile of a job and its pinned image.
type Profile struct {
	ID    string `json:"id"`
	Image string `json:"image"`
}

// Commit is a commit and the entries of its tree.
type Commit struct {
	SHA  string      `json:"sha"`
	Tree []TreeEntry `json:"tree"`
}

// TreeEntry is one entry of a commit's tree: its path, its git mode and the git id of its blob (of the commit, for a submodule).
type TreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	SHA  string `json:"sha"`
}

// Manifest is the governing manifest of a canonical job, or the canonical version that a receipt's revision expects.
type Manifest struct {
	Source string         `json:"source"` // V<n> or P<n>-R<m>
	Digest string         `json:"digest"` // sha256:<64 hex>
	Files  []ManifestFile `json:"files"`
}

// ManifestFile is one file of a manifest: Digest is what GET /jobs/{id}/files/{digest} takes.
type ManifestFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// Limits are the limits of a job.
type Limits struct {
	ArchiveBytes       int64 `json:"archiveBytes"`
	FileBytes          int64 `json:"fileBytes"`
	TestTimeoutSeconds int   `json:"testTimeoutSeconds"`
	MemoryMiB          int   `json:"memoryMiB"`
	CPUs               int   `json:"cpus"`
	Pids               int   `json:"pids"`
	TmpfsMiB           int   `json:"tmpfsMiB"`
	PrefetchModules    int   `json:"prefetchModules"`
	PrefetchBytes      int64 `json:"prefetchBytes"`
	Reruns             int   `json:"reruns"`
}

// validate checks the shape of a job that the service granted: the fields the executor reads with no further check.
func (j Job) validate() error {
	return nil
}
