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
)

// Cycle is one complete, successful cycle of one repository: every page of
// every listing in it succeeded.
type Cycle struct {
	Repo           string
	InstallationID int64
	At             time.Time                 // observation time, from the injected clock
	OpenPulls      []ObservedPull            // the complete open listing
	Comments       map[int][]ObservedComment // for each open pull number, its complete comment listing
}

// ObservedPull is one open pull request as listed.
type ObservedPull struct {
	Number           int
	HeadSHA, HeadRef string
	BaseRef, BaseSHA string
	UpdatedAt        time.Time
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
	// nothing. Concurrent calls for one repository are serialized.
	RecordCycle(ctx context.Context, cycle Cycle) error
	// RecordFailure stores a failed cycle (its classification and wait-until) without touching observations.
	RecordFailure(ctx context.Context, failure Failure) error
	// Pulls returns every pull ever seen in repo, ordered by number.
	Pulls(ctx context.Context, repo string) ([]PullState, error)
	// Comments returns every version of every comment seen on a pull, ordered by comment id and then updated_at.
	Comments(ctx context.Context, repo string, number int) ([]CommentVersion, error)
}

// RepositoryState is what the last cycles of one repository left behind.
type RepositoryState struct {
	Repo           string // owner/name
	InstallationID int64
	LastSuccess    time.Time   // zero before the first successful cycle
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
	Body         string
	BodyDigest   artifact.Digest
	FirstSeen    time.Time
	NotSeenSince time.Time // zero while the comment appears in its PR's full listing; set at the first full listing without it
}

// Validate rejects a cycle the store cannot record consistently: a missing
// repository, installation or time, a pull listed twice, a comment listing for
// a pull that is not open (or none for one that is), or a comment id listed
// twice or with an unset time.
func (c Cycle) Validate() error {
	if err := validateRepository(c.Repo, c.InstallationID); err != nil {
		return err
	}
	if c.At.IsZero() {
		return errors.New("github cycle has no observation time")
	}
	open := make(map[int]bool, len(c.OpenPulls))
	for _, pull := range c.OpenPulls {
		if pull.Number <= 0 || pull.Number > math.MaxInt32 || open[pull.Number] {
			return fmt.Errorf("github cycle lists pull %d twice or outside the pull number range", pull.Number)
		}
		open[pull.Number] = true
	}
	for number := range c.Comments {
		if !open[number] {
			return fmt.Errorf("github cycle lists comments of pull %d, which is not open in the cycle", number)
		}
	}
	seen := map[int64]bool{}
	for number := range open {
		listing, ok := c.Comments[number]
		if !ok {
			return fmt.Errorf("github cycle has no comment listing for open pull %d", number)
		}
		for _, comment := range listing {
			if comment.CommentID <= 0 || seen[comment.CommentID] || comment.CreatedAt.IsZero() || comment.UpdatedAt.IsZero() {
				return fmt.Errorf("github cycle has comment %d twice, with an invalid id or without a time", comment.CommentID)
			}
			seen[comment.CommentID] = true
		}
	}
	return nil
}

// Validate rejects a failure the store cannot classify.
func (f Failure) Validate() error {
	if err := validateRepository(f.Repo, f.InstallationID); err != nil {
		return err
	}
	switch f.Kind {
	case FailureRateLimited, FailureBudgetExhausted, FailureUnauthorized, FailureForbidden, FailureNotFound,
		FailureUnprocessable, FailureStatus, FailureTransport, FailureDecode:
	default:
		return fmt.Errorf("github failure has unknown kind %q", f.Kind)
	}
	if f.Status != 0 && (f.Status < 100 || f.Status > 599) {
		return fmt.Errorf("github failure has invalid status %d", f.Status)
	}
	return nil
}

func validateRepository(repo string, installation int64) error {
	if strings.TrimSpace(repo) == "" || installation <= 0 {
		return errors.New("github observation has no repository or installation")
	}
	return nil
}
