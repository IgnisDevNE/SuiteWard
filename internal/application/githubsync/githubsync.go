// Package githubsync is the port of the GitHub observation store: what
// synchronization saw on GitHub, as evidence for later phases and never as
// authority (docs/contracts/persistence.md, "GitHub observations"). These
// writes are not governance facts: they take no Suite lock and advance no
// Suite revision.
package githubsync

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

var (
	// ErrInvalidObservation marks a Cycle or Failure the store cannot record
	// (it fails Validate) and a pull number outside the stored range.
	ErrInvalidObservation = errors.New("invalid GitHub observation")
	// ErrCreationTimeChanged marks a comment whose created_at differs from the
	// one stored for its id: GitHub never changes it, so the cycle is not recorded.
	ErrCreationTimeChanged = errors.New("GitHub comment creation time changed")
	// ErrStaleResolution marks a Resolution that no longer applies: the pull is unknown, is open again, or its
	// absence is not the one the resolution was read for. Nothing is written.
	ErrStaleResolution = errors.New("stale pull resolution")
)

// Cycle is one complete, successful cycle of one repository: every page of
// every listing in it succeeded.
type Cycle struct {
	Repo           string
	InstallationID int64
	At             time.Time                 // observation time, from the injected clock
	StartedAt      time.Time                 // GitHub's Date of the cycle's first response (the open pulls listing)
	Requests       int                       // requests the cycle made, at least 1; not stored
	Changed        bool                      // some request was not a 304; not stored
	OpenPulls      []ObservedPull            // the complete open listing
	Comments       map[int][]ObservedComment // for each open pull number, its complete comment listing
}

// ObservedPull is one open pull request as listed.
type ObservedPull struct {
	Number           int
	HeadSHA, HeadRef string
	BaseRef, BaseSHA string
	UpdatedAt        time.Time
	HeadRepo         string // full_name of the head repository, "(deleted)" when GitHub reports none, "" when not observed
}

// ObservedComment is one comment as listed.
type ObservedComment struct {
	CommentID            int64
	AuthorID             int64
	AuthorLogin          string
	CreatedAt, UpdatedAt time.Time
	Body                 string
}

// FailureKind classifies a failed cycle. It is a fixed classification, never
// text GitHub sent.
type FailureKind string

// The classifications of a failed cycle.
const (
	FailureRateLimited     FailureKind = "rate_limited"
	FailureBudgetExhausted FailureKind = "budget_exhausted"
	FailureUnauthorized    FailureKind = "unauthorized"
	FailureForbidden       FailureKind = "forbidden"
	FailureNotFound        FailureKind = "not_found"
	FailureUnprocessable   FailureKind = "unprocessable"
	FailureStatus          FailureKind = "status"
	FailureTransport       FailureKind = "transport"
	FailureDecode          FailureKind = "decode"
)

// Failure is a failed cycle of one repository.
type Failure struct {
	Repo           string
	InstallationID int64
	At             time.Time
	Kind           FailureKind
	Status         int       // the HTTP status for kind status (and the typed kinds), else 0
	WaitUntil      time.Time // set for rate_limited and budget_exhausted
}

// Store keeps the observations. Implementations run the shared conformance
// suite in githubsynctest.
type Store interface {
	// Repository returns the stored state, or found false before its first cycle.
	Repository(ctx context.Context, repo string) (RepositoryState, bool, error)
	// RecordCycle stores one finished cycle of one repository atomically: the
	// repository state, the open pulls seen, the pulls no longer open, and the
	// comment versions seen. A repeated call with the same observations changes
	// nothing. Concurrent calls for one repository are serialized. It returns, in ascending order, the ids of the
	// comments this call stored for the first time: a comment it already had, edited or not, is never returned.
	RecordCycle(ctx context.Context, cycle Cycle) ([]int64, error)
	// RecordFailure stores a failed cycle (its classification and wait-until) without touching observations.
	RecordFailure(ctx context.Context, failure Failure) error
	// Pulls returns every pull ever seen in repo, ordered by number.
	Pulls(ctx context.Context, repo string) ([]PullState, error)
	// Comments returns every version of every comment seen on a pull, ordered by
	// comment id, updated_at and first_seen, then by body digest. A pull number
	// outside 1 to 2^31-1 is ErrInvalidObservation.
	Comments(ctx context.Context, repo string, number int) ([]CommentVersion, error)

	// AwaitingResolution returns the pulls that are not open and whose current absence is unresolved, by number.
	AwaitingResolution(ctx context.Context, repo string) ([]PullState, error)
	// RecordResolution stores the resolution of a pull's current absence and refreshes its head and base. It
	// returns ErrStaleResolution, and writes nothing, unless the pull is not open and its not_open_since equals
	// r.NotOpenSince.
	RecordResolution(ctx context.Context, repo string, r Resolution) error
	// RecordClosed applies one listing of closed pulls. The first call for a repository only sets the closed
	// watermark, one microsecond past its newest item: what it listed, and a merge in the exact moment of its newest
	// item that is listed only later, is history and is never inserted. Later, each merged item at or above the
	// watermark (inclusive) with no stored pull is inserted unresolved, absent since at; stored pulls, unmerged items
	// and items below it are ignored. Then the watermark rises to the greatest updated_at of the listing. The
	// repository must have a recorded cycle or failure.
	RecordClosed(ctx context.Context, repo string, closed []ClosedPull, at time.Time) error
	// Merged returns every pull resolved as merged, by MergedAt and then number.
	Merged(ctx context.Context, repo string) ([]MergedPull, error)
	// MarkProcessed sets the processed flag of a pull: a read optimization, never authority.
	MarkProcessed(ctx context.Context, repo string, number int) error
}

// RepositoryState is what the last cycles of one repository left behind.
type RepositoryState struct {
	Repo           string // owner/name
	InstallationID int64
	LastSuccess    time.Time   // zero before the first successful cycle
	CycleStartedAt time.Time   // GitHub's start of the last successful cycle; zero until one recorded it
	LastError      FailureKind // empty after a successful cycle
	LastStatus     int
	WaitUntil      time.Time // zero when not waiting (from RateLimitedError.Until or BudgetExhaustedError.Reset)
}

// PullState is one pull request as last observed.
type PullState struct {
	Number              int
	HeadSHA, HeadRef    string
	BaseRef, BaseSHA    string
	UpdatedAt           time.Time // GitHub's
	FirstSeen, LastSeen time.Time // observation times
	NotOpenSince        time.Time // zero while open; set at the first cycle the PR was absent from the open list
	HeadRepo            string    // "" when not observed (a row from before the column existed, or a cycle without it)
}

// Resolution is what GitHub says about one absence of a pull: merged or closed, and with which commit.
type Resolution struct {
	Number         int
	NotOpenSince   time.Time // the absence it resolves; must equal the stored not_open_since
	Merged         bool
	MergedAt       time.Time // GitHub's; set exactly when Merged
	MergeCommitSHA string    // 40 lowercase hex, or "" (only when Merged and the bound ran out)
	HeadSHA        string    // the head, base and head repository as read at resolution
	HeadRef        string
	BaseRef        string
	BaseSHA        string
	HeadRepo       string
	ResolvedAt     time.Time // injected clock
}

// MergedPull is a pull resolved as merged. PullState holds the refreshed head and base.
type MergedPull struct {
	PullState
	Resolution
	Processed bool // MarkProcessed was called
}

// ClosedPull is one item of the closed listing.
type ClosedPull struct {
	ObservedPull
	MergedAt time.Time // zero for a closed, unmerged pull
}

// CommentVersion is one version of a comment: an edit is a new version and the
// earlier body stays.
type CommentVersion struct {
	PullNumber   int
	CommentID    int64
	AuthorID     int64
	AuthorLogin  string
	CreatedAt    time.Time // GitHub's; never changes for an id
	UpdatedAt    time.Time // GitHub's; a new value is a new version (an edit)
	Body         string    // the raw bytes, whatever they contain (a NUL or invalid UTF-8 included)
	BodyDigest   artifact.Digest
	FirstSeen    time.Time
	NotSeenSince time.Time // zero while the comment appears in its PR's full listing; set at the first full listing without it
}

// ValidatePullNumber rejects a pull number outside what the store keeps
// (1 to 2^31-1).
func ValidatePullNumber(number int) error {
	if number < 1 || number > math.MaxInt32 {
		return fmt.Errorf("%w: pull number %d is outside 1 to %d", ErrInvalidObservation, number, math.MaxInt32)
	}
	return nil
}

// Validate rejects a cycle the store cannot record consistently: a missing
// repository, installation, observation time or start, fewer than one request, a pull number out of range or listed
// twice, a comment listing for a pull that is not open (or none for one that
// is), or a comment id that is not positive, is listed twice, or has an unset
// time. The error wraps ErrInvalidObservation.
func (c Cycle) Validate() error {
	if err := validateRepository(c.Repo, c.InstallationID); err != nil {
		return err
	}
	if c.At.IsZero() {
		return fmt.Errorf("%w: cycle has no observation time", ErrInvalidObservation)
	}
	if c.StartedAt.IsZero() || c.Requests < 1 {
		return fmt.Errorf("%w: cycle has no start in GitHub's clock or no requests", ErrInvalidObservation)
	}
	open := make(map[int]bool, len(c.OpenPulls))
	for _, pull := range c.OpenPulls {
		if err := ValidatePullNumber(pull.Number); err != nil {
			return err
		}
		if open[pull.Number] {
			return fmt.Errorf("%w: cycle lists pull %d twice", ErrInvalidObservation, pull.Number)
		}
		if err := validateHeadRepo(pull.Number, pull.HeadRepo); err != nil {
			return err
		}
		open[pull.Number] = true
	}
	for number := range c.Comments {
		if !open[number] {
			return fmt.Errorf("%w: cycle lists comments of pull %d, which is not open in the cycle", ErrInvalidObservation, number)
		}
	}
	seen := map[int64]bool{}
	for number := range open {
		listing, ok := c.Comments[number]
		if !ok {
			return fmt.Errorf("%w: cycle has no comment listing for open pull %d", ErrInvalidObservation, number)
		}
		for _, comment := range listing {
			if comment.CommentID <= 0 || seen[comment.CommentID] || comment.CreatedAt.IsZero() || comment.UpdatedAt.IsZero() {
				return fmt.Errorf("%w: cycle lists comment %d twice, with an id that is not positive, or without a time", ErrInvalidObservation, comment.CommentID)
			}
			seen[comment.CommentID] = true
		}
	}
	return nil
}

// Validate rejects a resolution the store cannot record consistently. The error wraps ErrInvalidObservation.
func (r Resolution) Validate() error {
	if err := ValidatePullNumber(r.Number); err != nil {
		return err
	}
	if r.NotOpenSince.IsZero() || r.ResolvedAt.IsZero() {
		return fmt.Errorf("%w: resolution of pull %d has no absence or resolution time", ErrInvalidObservation, r.Number)
	}
	if r.Merged == r.MergedAt.IsZero() {
		return fmt.Errorf("%w: resolution of pull %d has merged %t and merged_at %v", ErrInvalidObservation, r.Number, r.Merged, r.MergedAt)
	}
	if err := validateHeadRepo(r.Number, r.HeadRepo); err != nil {
		return err
	}
	if r.MergeCommitSHA != "" && (!r.Merged || !validSHA(r.MergeCommitSHA)) {
		return fmt.Errorf("%w: resolution of pull %d has a merge commit that is not 40 lowercase hex or is not merged", ErrInvalidObservation, r.Number)
	}
	return nil
}

// ValidateClosed rejects a closed listing the store cannot record: a missing time, a pull number out of range or an
// item without GitHub's updated_at. The error wraps ErrInvalidObservation.
func ValidateClosed(closed []ClosedPull, at time.Time) error {
	if at.IsZero() {
		return fmt.Errorf("%w: closed listing has no observation time", ErrInvalidObservation)
	}
	for _, item := range closed {
		if err := ValidatePullNumber(item.Number); err != nil {
			return err
		}
		if item.UpdatedAt.IsZero() {
			return fmt.Errorf("%w: closed pull %d has no updated_at", ErrInvalidObservation, item.Number)
		}
		if err := validateHeadRepo(item.Number, item.HeadRepo); err != nil {
			return err
		}
	}
	return nil
}

// validateHeadRepo accepts "" (not observed, stored as NULL) and a name, and rejects a blank one: the stores keep a
// blank as NULL or as text depending on how they trim, so neither may see one.
func validateHeadRepo(number int, headRepo string) error {
	if headRepo != "" && strings.TrimSpace(headRepo) == "" {
		return fmt.Errorf("%w: pull %d has a blank head repository", ErrInvalidObservation, number)
	}
	return nil
}

func validSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Validate rejects a failure the store cannot classify. The error wraps
// ErrInvalidObservation. A wait kind may carry no WaitUntil: whether a missing
// reset means "wait" is the reconciliation's decision, not the store's.
func (f Failure) Validate() error {
	if err := validateRepository(f.Repo, f.InstallationID); err != nil {
		return err
	}
	switch f.Kind {
	case FailureRateLimited, FailureBudgetExhausted, FailureUnauthorized, FailureForbidden, FailureNotFound,
		FailureUnprocessable, FailureStatus, FailureTransport, FailureDecode:
	default:
		return fmt.Errorf("%w: failure has unknown kind %q", ErrInvalidObservation, f.Kind)
	}
	if f.Status != 0 && (f.Status < 100 || f.Status > 599) {
		return fmt.Errorf("%w: failure has invalid status %d", ErrInvalidObservation, f.Status)
	}
	return nil
}

func validateRepository(repo string, installation int64) error {
	if strings.TrimSpace(repo) == "" || installation <= 0 {
		return fmt.Errorf("%w: no repository or installation", ErrInvalidObservation)
	}
	return nil
}
