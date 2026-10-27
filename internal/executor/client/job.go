package client

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// Job is the answer of POST /lease: what the executor runs and the lease that it holds (docs/contracts/runtime.md, "The executor
// API"). The client has checked its shape, which is all that it knows of it.
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

const (
	maxImageBytes = 300 // the longest image reference of the contract
	// The largest limits that are believed: bounds that no machine reaches, so that no product of limits overflows.
	maxBytesLimit = int64(1) << 40
	maxCountLimit = 1 << 20
)

var (
	leasePattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	shaPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sourcePattern   = regexp.MustCompile(`^(V[1-9][0-9]{0,8}|P[1-9][0-9]{0,8}-R[1-9][0-9]{0,8})$`)
	profilePattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	imagePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	errLimitInvalid = errors.New("a limit is out of range")
)

// validate checks the shape of a job that the service granted: the fields that the executor reads with no further check.
func (j Job) validate() error {
	switch {
	case j.JobID < 1 || j.Pull < 1:
		return errors.New("the job and the pull request need positive ids")
	case !leasePattern.MatchString(j.LeaseID):
		return errors.New("the lease id is not 32 lowercase hexadecimal digits")
	case j.LeaseExpiresAt.IsZero() || j.DeadlineAt.IsZero():
		return errors.New("the lease expiry and the deadline are required")
	case !profilePattern.MatchString(j.Profile.ID) || len(j.Profile.Image) > maxImageBytes || !imagePattern.MatchString(j.Profile.Image):
		return errors.New("the profile needs an id and an image pinned by digest")
	case !shaPattern.MatchString(j.Head.SHA):
		return errors.New("the head is not 40 lowercase hexadecimal digits")
	}
	if err := j.validatePlan(); err != nil {
		return err
	}
	if err := j.Manifest.validate(); err != nil {
		return err
	}
	return j.Limits.validate()
}

func (j Job) validatePlan() error {
	switch j.Plan {
	case PlanCanonical:
		if j.Base != nil {
			return errors.New("a canonical job has no base")
		}
	case PlanReceipt:
		if j.Base == nil || !shaPattern.MatchString(j.Base.SHA) {
			return errors.New("a receipt job needs a base of 40 lowercase hexadecimal digits")
		}
	default:
		return fmt.Errorf("the plan %q is neither canonical nor receipt", j.Plan)
	}
	return nil
}

func (m Manifest) validate() error {
	if !sourcePattern.MatchString(m.Source) || !digestPattern.MatchString(m.Digest) {
		return errors.New("the manifest needs a source (V<n> or P<n>-R<m>) and a sha256 digest")
	}
	for _, f := range m.Files {
		if f.Path == "" || !digestPattern.MatchString(f.Digest) {
			return errors.New("a manifest file needs a path and a sha256 digest")
		}
	}
	return nil
}

func (l Limits) validate() error {
	bytes := []int64{l.ArchiveBytes, l.FileBytes, l.PrefetchBytes}
	counts := []int{l.TestTimeoutSeconds, l.MemoryMiB, l.CPUs, l.Pids, l.TmpfsMiB, l.PrefetchModules}
	for _, n := range bytes {
		if n < 1 || n > maxBytesLimit {
			return errLimitInvalid
		}
	}
	for _, n := range counts {
		if n < 1 || n > maxCountLimit {
			return errLimitInvalid
		}
	}
	if l.Reruns < 0 || l.Reruns > maxCountLimit || l.TmpfsMiB > l.MemoryMiB {
		return errors.New("the reruns are negative, or the tmpfs is larger than the memory it counts in")
	}
	return nil
}
