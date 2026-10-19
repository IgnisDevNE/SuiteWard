package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

// maxLoginBytes is the longest GitHub login.
const maxLoginBytes = 39

var loginChars = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// User is a GitHub account as the owner lookup reads it. ID is the identity;
// Login is for display; Type is "User", "Organization" or "Bot".
type User struct {
	ID    int64
	Login string
	Type  string
}

// Repository is a GitHub repository. ID is the project identity (D-IDENTITY).
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
	// ClassicAbsent: GitHub answered 404, the branch has no classic protection.
	ClassicAbsent ClassicState = iota
	// ClassicUnreadable: GitHub answered 403.
	ClassicUnreadable
	// ClassicPresent: the protection was read.
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

// Protection is what GitHub says protects one branch: typed facts, not a
// verdict. Rules holds the required_status_checks rules of the effective
// rules (none when RulesForbidden); FromRuleset is true when any effective
// rule, of whatever type, came from a ruleset; RulesForbidden is true when
// GitHub answered 403 to the effective rules.
type Protection struct {
	RulesForbidden bool
	Rules          []Rule
	FromRuleset    bool
	Classic        Classic
}

type userJSON struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

// User returns the account named login.
func (r *Reader) User(ctx context.Context, login string) (User, error) {
	if len(login) > maxLoginBytes || !loginChars.MatchString(login) {
		return User{}, fmt.Errorf("%w: login %q is not a GitHub login", ErrInvalidArgument, login)
	}
	path := "/users/" + login
	resp, err := r.client.Get(ctx, path)
	if err != nil {
		return User{}, fmt.Errorf("user %s: %w", login, err)
	}
	var u userJSON
	if err := decode(path, resp.Body, &u); err != nil {
		return User{}, err
	}
	if u.ID <= 0 || u.Login == "" || u.Type == "" {
		return User{}, fmt.Errorf("%w: the answer to %s lacks an id, a login or a type", ErrInvalidResponse, path)
	}
	return User(u), nil
}

type repositoryJSON struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	DefaultBranch string `json:"default_branch"`
}

// Repository returns the identity and default branch of repo.
func (r *Reader) Repository(ctx context.Context, repo string) (Repository, error) {
	if err := checkRepo(repo); err != nil {
		return Repository{}, err
	}
	path := "/repos/" + repo
	resp, err := r.client.Get(ctx, path)
	if err != nil {
		return Repository{}, fmt.Errorf("repository %s: %w", repo, err)
	}
	var v repositoryJSON
	if err := decode(path, resp.Body, &v); err != nil {
		return Repository{}, err
	}
	if v.ID <= 0 || v.FullName == "" || v.DefaultBranch == "" {
		return Repository{}, fmt.Errorf("%w: the answer to %s lacks an id, a full name or a default branch", ErrInvalidResponse, path)
	}
	return Repository(v), nil
}

// checkJSON is a required check of a ruleset rule (integration_id) or of
// classic protection (app_id); null and absent decode to 0.
type checkJSON struct {
	Context       string `json:"context"`
	IntegrationID int64  `json:"integration_id"`
	AppID         int64  `json:"app_id"`
}

type ruleJSON struct {
	RulesetSourceType string `json:"ruleset_source_type"`
	RulesetID         int64  `json:"ruleset_id"`
	Type              string `json:"type"`
	Parameters        struct {
		Strict bool        `json:"strict_required_status_checks_policy"`
		Checks []checkJSON `json:"required_status_checks"`
	} `json:"parameters"`
}

type classicJSON struct {
	RequiredStatusChecks struct {
		Strict bool        `json:"strict"`
		Checks []checkJSON `json:"checks"`
	} `json:"required_status_checks"`
	EnforceAdmins struct {
		Enabled bool `json:"enabled"`
	} `json:"enforce_admins"`
}

// statusIs reports whether err is the rest client's answer with status.
func statusIs(err error, status int) bool {
	var se *rest.StatusError
	return errors.As(err, &se) && se.Status == status
}

// typeChecks types the required checks of a rule or of classic protection,
// reading each source id with pin; a check without a context cannot be
// matched, so it is a malformed answer.
func typeChecks(raw []checkJSON, pin func(checkJSON) int64, path string) ([]Check, error) {
	var out []Check
	for _, c := range raw {
		if c.Context == "" {
			return nil, fmt.Errorf("%w: the answer to %s lists a required check without a context", ErrInvalidResponse, path)
		}
		out = append(out, Check{Context: c.Context, SourceID: pin(c)})
	}
	return out, nil
}

// Protection reads the effective rules and the classic protection of branch of
// repo. A 403 on the rules, and a 403 or 404 on the classic protection, are
// facts of the Protection; any other failure is an error.
func (r *Reader) Protection(ctx context.Context, repo, branch string) (Protection, error) {
	if err := checkRepo(repo); err != nil {
		return Protection{}, err
	}
	if branch == "" || branch == "." || branch == ".." {
		return Protection{}, fmt.Errorf("%w: branch %q cannot be named in a URL path", ErrInvalidArgument, branch)
	}
	escaped := url.PathEscape(branch)
	var p Protection

	rulesPath := func(page int) string {
		return fmt.Sprintf("/repos/%s/rules/branches/%s?per_page=%d&page=%d", repo, escaped, pageSize, page)
	}
	rules, _, err := readDecoded[ruleJSON](ctx, r.client, rulesPath)
	switch {
	case statusIs(err, http.StatusForbidden):
		p.RulesForbidden = true
	case err != nil:
		return Protection{}, fmt.Errorf("effective rules of %s: %w", repo, err)
	}
	for _, rule := range rules {
		p.FromRuleset = p.FromRuleset || rule.RulesetSourceType != "" || rule.RulesetID != 0
		if rule.Type != "required_status_checks" {
			continue
		}
		cs, err := typeChecks(rule.Parameters.Checks, func(c checkJSON) int64 { return c.IntegrationID }, rulesPath(1))
		if err != nil {
			return Protection{}, err
		}
		p.Rules = append(p.Rules, Rule{RulesetID: rule.RulesetID, Checks: cs, Strict: rule.Parameters.Strict})
	}

	classicPath := "/repos/" + repo + "/branches/" + escaped + "/protection"
	resp, err := r.client.Get(ctx, classicPath)
	switch {
	case statusIs(err, http.StatusNotFound):
		p.Classic.State = ClassicAbsent
	case statusIs(err, http.StatusForbidden):
		p.Classic.State = ClassicUnreadable
	case err != nil:
		return Protection{}, fmt.Errorf("classic protection of %s: %w", repo, err)
	default:
		var c *classicJSON // a null body decodes to nil
		if err := decode(classicPath, resp.Body, &c); err != nil {
			return Protection{}, err
		}
		if c == nil {
			return Protection{}, fmt.Errorf("%w: the answer to %s is null", ErrInvalidResponse, classicPath)
		}
		cs, err := typeChecks(c.RequiredStatusChecks.Checks, func(c checkJSON) int64 { return c.AppID }, classicPath)
		if err != nil {
			return Protection{}, err
		}
		p.Classic = Classic{State: ClassicPresent, Checks: cs, Strict: c.RequiredStatusChecks.Strict, EnforceAdmins: c.EnforceAdmins.Enabled}
	}
	return p, nil
}
