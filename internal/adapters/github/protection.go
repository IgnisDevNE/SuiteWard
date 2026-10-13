package github

import (
	"context"
	"errors"
)

var errNotImplemented = errors.New("github: not implemented")

// User is a GitHub account as the owner lookup reads it.
type User struct {
	ID    int64
	Login string
	Type  string
}

// Repository is a GitHub repository; ID is the project identity.
type Repository struct {
	ID            int64
	FullName      string
	Private       bool
	DefaultBranch string
}

// Check is one required status check. SourceID is the integration id of a
// ruleset rule or the app id of classic protection; 0 means not pinned, and
// classic protection can also say -1 (any source).
type Check struct {
	Context  string
	SourceID int64
}

// Rule is one required_status_checks rule of an active ruleset.
type Rule struct {
	RulesetID int64
	Checks    []Check
	Strict    bool
}

// ClassicState tells whether classic branch protection could be read.
type ClassicState int

const (
	ClassicAbsent ClassicState = iota
	ClassicUnreadable
	ClassicPresent
)

// Classic is the classic branch protection; Checks, Strict and EnforceAdmins
// are meaningful only when State is ClassicPresent.
type Classic struct {
	State         ClassicState
	Checks        []Check
	Strict        bool
	EnforceAdmins bool
}

// Protection is what GitHub says protects one branch: typed facts, not a verdict.
type Protection struct {
	RulesForbidden bool
	Rules          []Rule
	FromRuleset    bool
	Classic        Classic
}

func (r *Reader) User(ctx context.Context, login string) (User, error) {
	return User{}, errNotImplemented
}

func (r *Reader) Repository(ctx context.Context, repo string) (Repository, error) {
	return Repository{}, errNotImplemented
}

func (r *Reader) Protection(ctx context.Context, repo, branch string) (Protection, error) {
	return Protection{}, errNotImplemented
}
