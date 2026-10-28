package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The github_check publisher (docs/contracts/runtime.md, "Outbox kind github_check"). The check job decides the
// states and writes the messages through checks.Store.Publish; the publisher only posts the run of a message that
// is still current.

const (
	githubCheckKind = "github_check"
	checkKey        = "check"
	// checkName is the name of the check run (D-CHECKS), required by the repository's protection.
	checkName = "SuiteWard / Contract"
)

var (
	headPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	versionPattern = regexp.MustCompile(`^V([1-9][0-9]{0,8})$`)
)

// checkPayload is the payload of check:<repository id>:<pull>:<generation>: the decided state of one pull
// request's head. It holds identifiers and codes, never text from the repository or a comment.
type checkPayload struct {
	commentTarget
	HeadSHA    string `json:"headSha"`
	Generation int64  `json:"generation"`
	State      string `json:"state"`
	Reason     string `json:"reason"`              // a stable reason code of the runtime contract
	Reference  string `json:"reference,omitempty"` // P<n>-R<m>, the revision the check concerns
	Tier       string `json:"tier"`                // the effective tier: owner.TierIsolated or owner.TierCoLocated
	Version    string `json:"version,omitempty"`   // V<n>, the canonical version
	// A runner-uncovered check lists its runner files: at most 20, each cut to maxSummaryText bytes, and how many there are. No other reason has them.
	Paths      []string `json:"paths,omitempty"`
	PathsTotal int      `json:"pathsTotal,omitempty"`
	// Execution counts the tests of a complete canonical run under canonical: required, and no other check has it (docs/contracts/runtime.md, "The canonical gate").
	Execution *checkExecution `json:"execution,omitempty"`
	// Unblocked marks a check that the owner's unblock of an unavailable canonical run lifted.
	Unblocked bool `json:"unblocked,omitempty"`
	// ApproveMode marks an approval-missing check whose approve names a mode, which a required receipt needs (docs/contracts/runtime.md, "The canonical gate", "The approve command of an approval-missing check").
	ApproveMode bool `json:"approveMode,omitempty"`
}

// checkExecution are the counts of a complete canonical run: the tests that ran (every declared test but the excluded ones) and how many of them passed, were
// skipped, failed, were missing or flaky, and how many are excluded and were not run.
type checkExecution struct {
	Tests    int `json:"tests"`
	Passed   int `json:"passed"`
	Skipped  int `json:"skipped"`
	Failed   int `json:"failed"`
	Missing  int `json:"missing"`
	Flaky    int `json:"flaky"`
	Excluded int `json:"excluded"`
}

// checkCountsOf is the counts of a complete canonical run as a check states them: the tests that ran are every declared test but the excluded ones. The receipt page states
// the same counts, as the commandtext type of the same fields.
func checkCountsOf(c domain.CanonicalCounts) *checkExecution {
	return &checkExecution{Tests: c.Pass + c.Fail + c.Skipped + c.Missing + c.Flaky, Passed: c.Pass, Skipped: c.Skipped, Failed: c.Fail, Missing: c.Missing, Flaky: c.Flaky, Excluded: c.Excluded}
}

// output validates the payload and renders the title and the summary of its check run; key only names the
// message in an error, which never quotes the payload.
func (p checkPayload) output(key string) (title, summary string, err error) {
	c, err := p.check(key)
	if err != nil {
		return "", "", err
	}
	return renderCheck(key, c)
}

// check validates the fields of the payload and returns what its output states. It does not render, so a message that
// is dropped as stale never meets a reason that no longer exists.
func (p checkPayload) check(key string) (commandtext.Check, error) {
	state, tier := checkStates[checks.State(p.State)], checkTiers[p.Tier]
	if err := p.validate(); err != nil {
		return commandtext.Check{}, keyError(githubCheckKind, key, err.Error())
	}
	switch {
	case !headPattern.MatchString(p.HeadSHA):
		return commandtext.Check{}, keyError(githubCheckKind, key, "the head is not 40 lowercase hexadecimal digits")
	case p.Generation < 1:
		return commandtext.Check{}, keyError(githubCheckKind, key, "the generation must be positive")
	case state == 0 || tier == 0:
		return commandtext.Check{}, keyError(githubCheckKind, key, "the state or the tier is unknown")
	case slices.ContainsFunc(p.Paths, func(path string) bool { return len(path) > maxSummaryText }):
		return commandtext.Check{}, keyError(githubCheckKind, key, "a path is too long")
	}
	c := commandtext.Check{State: state, Reason: p.Reason, Tier: tier, Paths: p.Paths, PathsTotal: p.PathsTotal, Unblocked: p.Unblocked, ApproveMode: p.ApproveMode}
	if p.Execution != nil {
		counts := commandtext.CheckExecution(*p.Execution)
		c.Execution = &counts
	}
	if p.Reference != "" {
		var ok bool
		if c.Proposal, c.Revision, ok = commandtext.ParseReference(p.Reference); !ok {
			return commandtext.Check{}, keyError(githubCheckKind, key, "the reference is not P<n>-R<m>")
		}
	}
	if p.Version != "" {
		m := versionPattern.FindStringSubmatch(p.Version)
		if m == nil {
			return commandtext.Check{}, keyError(githubCheckKind, key, "the version is not V<n>")
		}
		c.Version, _ = strconv.Atoi(m[1]) // the pattern admits one to nine digits
	}
	return c, nil
}

func renderCheck(key string, c commandtext.Check) (title, summary string, err error) {
	_, err = render(githubCheckKind, key, func() string { // a reason or a combination commandtext does not know panics there
		title, summary = commandtext.CheckSummary(c)
		return summary
	})
	return title, summary, err
}

var (
	checkStates = map[checks.State]commandtext.CheckState{checks.Success: commandtext.CheckSuccess, checks.InProgress: commandtext.CheckInProgress, checks.Failure: commandtext.CheckFailure}
	checkTiers  = map[string]commandtext.Tier{owner.TierIsolated: commandtext.Isolated, owner.TierCoLocated: commandtext.CoLocated}
)

// newCheckMessage builds the github_check message of generation for the decided result r of a pull request of
// repo (owner/name). tier is the effective tier and version the canonical version (V<n>), empty for none.
func newCheckMessage(repo string, r checks.Result, tier, version string, generation int64) (governance.OutboxMessage, error) {
	return newCheckMessageWithPaths(repo, r, tier, version, generation, nil, 0)
}

// newCheckMessageWithPaths is newCheckMessage for a runner-uncovered result: paths are its runner files and total how many there are. The
// payload keeps at most 20, each cut to maxSummaryText bytes.
func newCheckMessageWithPaths(repo string, r checks.Result, tier, version string, generation int64, paths []string, total int) (governance.OutboxMessage, error) {
	return newCheckMessageOf(repo, r, tier, version, generation, paths, total, nil, false)
}

// newCheckMessageOf is newCheckMessageWithPaths for a result that execution adds to: counts are those of a complete canonical run, nil for none, and approveMode says that the
// approve of an approval that is missing names a mode.
func newCheckMessageOf(repo string, r checks.Result, tier, version string, generation int64, paths []string, total int, counts *checkExecution, approveMode bool) (governance.OutboxMessage, error) {
	p := checkPayload{commentTarget: commentTarget{RepositoryID: r.RepositoryID, Repo: repo, Pull: r.PullNumber}, HeadSHA: r.HeadSHA, Generation: generation,
		State: string(r.State), Reason: r.Reason, Reference: r.Reference, Tier: tier, Version: version, PathsTotal: total, Execution: counts, Unblocked: r.Unblocked, ApproveMode: approveMode}
	if len(paths) > 0 {
		p.Paths = boundedText(paths)
	}
	key := fmt.Sprintf("%s:%d:%d:%d", checkKey, r.RepositoryID, r.PullNumber, generation)
	if _, _, err := p.output(key); err != nil {
		return governance.OutboxMessage{}, err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the check message: %w", err)
	}
	return governance.OutboxMessage{Key: key, Kind: githubCheckKind, Payload: payload}, nil
}

// splitCheckKey parses check:<repository id>:<pull>:<generation>.
func splitCheckKey(key string) (repositoryID int64, pull int, generation int64, ok bool) {
	return splitGenerationKey(checkKey, key)
}

// splitGenerationKey parses <kind>:<repository id>:<pull>:<generation>, the key of a check message; the numbers are positive and canonical.
func splitGenerationKey(kind, key string) (repositoryID int64, pull int, generation int64, ok bool) {
	parts := strings.Split(key, ":")
	if len(parts) != 4 || parts[0] != kind {
		return 0, 0, 0, false
	}
	var numbers [3]int64
	for i, part := range parts[1:] {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil || n <= 0 || strconv.FormatInt(n, 10) != part {
			return 0, 0, 0, false
		}
		numbers[i] = n
	}
	return numbers[0], int(numbers[1]), numbers[2], numbers[1] <= 1<<31-1
}

type checkRunner interface {
	Post(ctx context.Context, repo string, run github.CheckRun) (int64, error)
	Runs(ctx context.Context, repo, sha, name string, appID int64) ([]github.CheckRunSummary, error)
}

// checkLedger is the part of checks.Store the publisher reads.
type checkLedger interface {
	Current(ctx context.Context, repositoryID int64, pull int) (checks.Record, bool, error)
}

// checkPublisher is the river.Publisher of the github_check kind.
type checkPublisher struct {
	runs     checkRunner
	ledger   checkLedger
	instance *lazy[string] // commands.Store.Instance, read once
	appID    int64         // the App that posts, whose runs the readback lists
	logger   *slog.Logger
	reports  reportSource // rebuilds the change report of a revision for the numbers of the summary
}

// publish delivers one github_check message: it drops a message whose generation is no longer the pull request's
// current one, skips one the App already posted (its external id is the idempotency key, so at-least-once
// delivery posts once) and otherwise posts the run.
func (p *checkPublisher) publish(ctx context.Context, key string, payload json.RawMessage) error {
	repositoryID, pull, generation, ok := splitCheckKey(key)
	if !ok {
		return keyError(githubCheckKind, key, "not a github_check key")
	}
	c, err := decodePayload[checkPayload](githubCheckKind, key, payload)
	if err != nil {
		return err
	}
	check, err := c.check(key)
	if err != nil {
		return err
	}
	if c.RepositoryID != repositoryID || c.Pull != pull || c.Generation != generation {
		return keyError(githubCheckKind, key, "the payload does not match its key")
	}
	record, found, err := p.ledger.Current(ctx, repositoryID, pull)
	switch {
	case err != nil:
		return fmt.Errorf("%s %s: read the check ledger: %w", githubCheckKind, key, err)
	case !found:
		return keyError(githubCheckKind, key, "the check ledger has no record of the pull request")
	case record.Generation != generation: // a newer state or head superseded this message; it is not rendered, as its reason may no longer exist
		return nil
	}
	if check.Proposal != 0 && check.Version != 0 { // a revision against a canonical: there is something to count
		changes, found, err := summarize(ctx, p.reports, p.logger, githubCheckKind+" "+key, revisionReference(repositoryID, check.Proposal, check.Revision))
		if err != nil {
			return fmt.Errorf("%s %s: %w", githubCheckKind, key, err)
		}
		if found {
			check.Changes = &changes
		}
	}
	title, summary, err := renderCheck(key, check)
	if err != nil && check.Changes != nil { // the check is the gate of the merge: it is posted without numbers that cannot be rendered
		p.logger.WarnContext(ctx, "github_check: the check is posted without the numbers of the change report, which it could not render", "key", key)
		check.Changes = nil
		title, summary, err = renderCheck(key, check)
	}
	if err != nil {
		return err
	}
	run := github.CheckRun{Name: checkName, HeadSHA: c.HeadSHA, Status: "completed", Conclusion: c.State, Title: title, Summary: summary}
	if c.State == string(checks.InProgress) {
		run.Status, run.Conclusion = "in_progress", ""
	}
	return postCheckRun(ctx, checkClient{runs: p.runs, instance: p.instance, appID: p.appID, logger: p.logger}, githubCheckKind, key, c.Repo, run)
}

// checkClient is what both check publishers post with: the check runs of GitHub, the instance id that makes an external id, the App that posts and a logger.
type checkClient struct {
	runs     checkRunner
	instance *lazy[string]
	appID    int64
	logger   *slog.Logger
}

// postCheckRun posts run for the message key of kind unless the App already posted a run with its external id (suiteward:<instance id>:<key>) for the head: the external id
// is the idempotency key, so at-least-once delivery posts once. A head that no longer exists on GitHub (a 422 to the readback or to the post, after a force-push) has
// nothing to be posted for, so the message counts as delivered; it is logged by key and head, never with GitHub's text.
func postCheckRun(ctx context.Context, c checkClient, kind, key, repo string, run github.CheckRun) error {
	instance, err := c.instance.get(ctx)
	if err != nil {
		return fmt.Errorf("%s %s: read the instance id: %w", kind, key, err)
	}
	run.ExternalID = "suiteward:" + instance + ":" + key
	existing, err := c.runs.Runs(ctx, repo, run.HeadSHA, run.Name, c.appID)
	switch {
	case c.vanished(err, kind, key, repo, run.HeadSHA):
		return nil
	case err != nil:
		return githubFailure(kind, key, "list the check runs", err)
	}
	if slices.ContainsFunc(existing, func(r github.CheckRunSummary) bool { return r.ExternalID == run.ExternalID }) {
		return nil
	}
	_, err = c.runs.Post(ctx, repo, run)
	switch {
	case c.vanished(err, kind, key, repo, run.HeadSHA):
		return nil
	case err != nil:
		return githubFailure(kind, key, "post the check run", err)
	}
	return nil
}

// vanished reports whether err says the head no longer exists on GitHub (a 422). It logs the key and the head, never GitHub's text.
func (c checkClient) vanished(err error, kind, key, repo, head string) bool {
	if !errors.Is(err, rest.ErrUnprocessable) {
		return false
	}
	c.logger.Warn(kind+": the head no longer exists on GitHub; the message counts as delivered", "key", key, "repo", repo, "head", head)
	return true
}
