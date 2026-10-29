package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// The three comments of a head (docs/contracts/runtime.md, "The comments of a head"): their keys, their payloads and their renderers. The checks job decides when each is
// written (resultscomment_job.go). A payload holds identifiers, codes and counts, never text from the repository or from the test runner.

const (
	resultsKey       = "results"        // results:<repository id>:<pull>:<head sha>:<canonical job>-<receipt job>
	notRespondingKey = "not-responding" // not-responding:<repository id>:<pull>:<head sha>:<canonical job>-<receipt job>
	runnerBackKey    = "runner-back"    // runner-back:<repository id>:<pull>:<head sha>

	runComplete    = "complete"
	runUnavailable = "unavailable"
)

// canonicalCountKeys are the keys of the counts of a canonical run in a results payload, every one of which is always present.
var canonicalCountKeys = []string{"tests", "passed", "skipped", "failed", "missing", "flaky", "excluded"}

// resultsCanonical is the canonical run of a results payload: complete, with its counts, or unavailable, with the code of its reason.
type resultsCanonical struct {
	Status string          `json:"status"`
	Reason string          `json:"reason,omitempty"` // exactly for an unavailable run
	Counts *checkExecution `json:"counts,omitempty"` // exactly for a complete one
}

// resultsReceipt is the receipt run of a results payload, as resultsCanonical is the canonical one; its counts are the ten of the Receipt check.
type resultsReceipt struct {
	Status string              `json:"status"`
	Reason string              `json:"reason,omitempty"`
	Counts *receiptCheckCounts `json:"counts,omitempty"`
}

// resultsPayload is the payload of results:<repository id>:<pull>:<head sha>:<canonical job>-<receipt job>: one set of runs of a head, final. A plan that is not wanted
// has the job 0 and no run. The verdict of a mode is not here: the renderer derives it from the counts, so a payload cannot state one that its counts do not give.
type resultsPayload struct {
	commentTarget
	Head           string            `json:"head"`
	Tier           string            `json:"tier"` // the effective tier: owner.TierIsolated or owner.TierCoLocated
	CanonicalJob   int64             `json:"canonicalJob"`
	ReceiptJob     int64             `json:"receiptJob"`
	Reference      string            `json:"reference"` // P<n>-R<m>, empty for a head with no revision
	CanonicalLevel string            `json:"canonicalLevel"`
	ReceiptLevel   string            `json:"receiptLevel"`
	Canonical      *resultsCanonical `json:"canonical,omitempty"`
	Receipt        *resultsReceipt   `json:"receipt,omitempty"`
	Weakened       int               `json:"weakened"`            // 0 when unknown
	Unblocked      []string          `json:"unblocked,omitempty"` // the plans whose job has an unblock row, canonical first
	Ended          []string          `json:"ended,omitempty"`     // those whose job the command ended: a subset of Unblocked
	Next           string            `json:"next"`                // unblock, blocked, approve, fix or none
	ApproveMode    bool              `json:"approveMode,omitempty"`
}

// notRespondingPayload is the payload of not-responding:<repository id>:<pull>:<head sha>:<canonical job>-<receipt job>: the jobs that wait for the runner, and whether
// the unblock is offered.
type notRespondingPayload struct {
	commentTarget
	Head         string `json:"head"`
	Tier         string `json:"tier"`
	CanonicalJob int64  `json:"canonicalJob"`
	ReceiptJob   int64  `json:"receiptJob"`
	Unblock      bool   `json:"unblock"`
}

// runnerBackPayload is the payload of runner-back:<repository id>:<pull>:<head sha>: the target, the head and the tier, and nothing else.
type runnerBackPayload struct {
	commentTarget
	Head string `json:"head"`
	Tier string `json:"tier"`
}

// headAndTier reports why a head or a tier cannot be stated.
func headAndTier(head, tier string) error {
	switch {
	case !headPattern.MatchString(head):
		return errors.New("the head is not 40 lowercase hexadecimal digits")
	case checkTiers[tier] == 0:
		return errors.New("the tier is unknown")
	}
	return nil
}

// jobsTail is the end of the key of a comment that names jobs: <pull>:<head sha>:<canonical job>-<receipt job>. validJobs reports whether the pair names a job.
func jobsTail(target commentTarget, head string, pair domain.JobPair) string {
	return fmt.Sprintf("%d:%s:%s", target.Pull, head, pair)
}

func validJobs(pair domain.JobPair) bool {
	return min(pair.Canonical, pair.Receipt) >= 0 && max(pair.Canonical, pair.Receipt) > 0
}

func (p resultsPayload) jobs() domain.JobPair {
	return domain.JobPair{Canonical: p.CanonicalJob, Receipt: p.ReceiptJob}
}

func (p resultsPayload) key() string {
	return fmt.Sprintf("%s:%d:%s", resultsKey, p.RepositoryID, jobsTail(p.commentTarget, p.Head, p.jobs()))
}

// validate reports why the payload does not hold together, without quoting it. The renderer checks the rest (the codes, the counts and the plans of the text).
func (p resultsPayload) validate() error {
	if err := p.commentTarget.validate(); err != nil {
		return err
	}
	if err := headAndTier(p.Head, p.Tier); err != nil {
		return err
	}
	if _, _, ok := commandtext.ParseReference(p.Reference); !ok && p.Reference != "" {
		return errors.New("the reference is not P<n>-R<m>")
	}
	switch {
	case !validJobs(p.jobs()):
		return errors.New("a job is negative, or no job is named")
	case (p.CanonicalJob > 0) != (p.Canonical != nil) || (p.ReceiptJob > 0) != (p.Receipt != nil):
		return errors.New("a plan has a run exactly when it has a job")
	case p.Weakened < 0:
		return errors.New("the weakened count is negative")
	case p.ApproveMode && p.Next != domain.NextApprove.String():
		return errors.New("a mode is named for a step that is no approve")
	}
	return errors.Join(p.Canonical.validate(), p.Receipt.validate())
}

// validate reports a status that is neither complete nor unavailable, a reason that does not go with it and counts that do not go with it or do not add up.
func (r *resultsCanonical) validate() error {
	if r == nil {
		return nil
	}
	if err := validRun(r.Status, r.Reason, r.Counts != nil); err != nil {
		return err
	}
	if c := r.Counts; c != nil && (min(c.Tests, c.Passed, c.Skipped, c.Failed, c.Missing, c.Flaky, c.Excluded) < 0 || c.Tests != c.Passed+c.Skipped+c.Failed+c.Missing+c.Flaky) {
		return errors.New("the counts of the approved tests are negative or do not add up")
	}
	return nil
}

func (r *resultsReceipt) validate() error {
	if r == nil {
		return nil
	}
	return validRun(r.Status, r.Reason, r.Counts != nil)
}

func validRun(status, reason string, counts bool) error {
	switch {
	case status != runComplete && status != runUnavailable:
		return errors.New("a run is neither complete nor unavailable")
	case (status == runUnavailable) != (reason != ""):
		return errors.New("a run has a reason exactly when it is unavailable")
	case (status == runComplete) != counts:
		return errors.New("a run has counts exactly when it is complete")
	}
	return nil
}

// results maps the payload to what the renderer takes, with the link of the pull request page (empty for none). The renderer refuses what does not fit.
func (p resultsPayload) results(link string) (commandtext.Results, error) {
	canonicalLevel, err := domain.ParseCanonicalLevel(p.CanonicalLevel)
	if err != nil {
		return commandtext.Results{}, err
	}
	receiptLevel, err := domain.ParseReceiptLevel(p.ReceiptLevel)
	if err != nil {
		return commandtext.Results{}, err
	}
	next, err := domain.ParseNext(p.Next)
	if err != nil {
		return commandtext.Results{}, err
	}
	unblocked, err := orderedPlans(p.Unblocked)
	if err != nil {
		return commandtext.Results{}, err
	}
	ended, err := orderedPlans(p.Ended)
	if err != nil {
		return commandtext.Results{}, err
	}
	r := commandtext.Results{Head: p.Head, Pull: p.Pull, Tier: checkTiers[p.Tier], CanonicalLevel: canonicalLevel, ReceiptLevel: receiptLevel, Weakened: p.Weakened,
		Unblocked: unblocked, Ended: ended, Next: next, ApproveMode: p.ApproveMode, PullURL: link}
	if p.Reference != "" {
		r.Proposal, r.Revision, _ = commandtext.ParseReference(p.Reference)
	}
	if c := p.Canonical; c != nil {
		r.Canonical = &commandtext.CanonicalRun{Unavailable: c.Reason}
		if c.Counts != nil {
			r.Canonical.Counts = domain.CanonicalCounts{Pass: c.Counts.Passed, Fail: c.Counts.Failed, Skipped: c.Counts.Skipped, Missing: c.Counts.Missing, Flaky: c.Counts.Flaky, Excluded: c.Counts.Excluded}
		}
	}
	if c := p.Receipt; c != nil {
		r.Receipt = &commandtext.ReceiptRun{Unavailable: c.Reason}
		if n := c.Counts; n != nil {
			r.Receipt.Counts = domain.ReceiptCounts{Excluded: n.Excluded, Missing: n.Missing, Flaky: n.Flaky, Skipped: n.Skipped, Fails: n.Fails, NewAPI: n.NewAPI, Detects: n.Detects, Inert: n.Inert, Unparsed: n.Unparsed}
			r.Receipt.Markers = n.Markers
		}
	}
	return r, nil
}

// orderedPlans reads a list of plans that the payload spells in the order of planNames (canonical first, each at most once).
func orderedPlans(names []string) (commandtext.Plans, error) {
	plans, ok := plansOf(names)
	if !ok || !slices.Equal(planNames(plans), names) {
		return commandtext.Plans{}, errors.New("the plans are not canonical and receipt, canonical first, each at most once")
	}
	return plans, nil
}

// text renders the comment of the payload stored under key, with the link of the page (empty for none), without quoting the payload in an error.
func (p resultsPayload) text(key, link string) (string, error) {
	r, err := p.results(link)
	if err != nil {
		return "", keyError(githubCommentKind, key, "the payload cannot be rendered")
	}
	return render(githubCommentKind, key, func() string { return commandtext.ResultsComment(r) })
}

// newResultsMessage builds the results comment of a set. A payload the publisher could not render is an error, so nobody writes a message that nobody can post.
func newResultsMessage(p resultsPayload) (governance.OutboxMessage, error) {
	if err := p.validate(); err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("results comment: %w", err)
	}
	if _, err := p.text(resultsKey, ""); err != nil {
		return governance.OutboxMessage{}, err
	}
	payload, err := marshalPayload(p)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the results comment: %w", err)
	}
	return governance.OutboxMessage{Key: p.key(), Kind: githubCommentKind, Payload: payload}, nil
}

func (p notRespondingPayload) jobs() domain.JobPair {
	return domain.JobPair{Canonical: p.CanonicalJob, Receipt: p.ReceiptJob}
}

func (p notRespondingPayload) key() string {
	return fmt.Sprintf("%s:%d:%s", notRespondingKey, p.RepositoryID, jobsTail(p.commentTarget, p.Head, p.jobs()))
}

func (p notRespondingPayload) validate() error {
	if err := p.commentTarget.validate(); err != nil {
		return err
	}
	if err := headAndTier(p.Head, p.Tier); err != nil {
		return err
	}
	if !validJobs(p.jobs()) {
		return errors.New("a job is negative, or no job is named")
	}
	return nil
}

func (p notRespondingPayload) text(key, link string) (string, error) {
	return render(githubCommentKind, key, func() string {
		return commandtext.NotRespondingComment(commandtext.NotResponding{Head: p.Head, Pull: p.Pull, Unblock: p.Unblock, PullURL: link})
	})
}

// newNotRespondingMessage builds the not-responding comment of the jobs that wait for the runner.
func newNotRespondingMessage(p notRespondingPayload) (governance.OutboxMessage, error) {
	if err := p.validate(); err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("not-responding comment: %w", err)
	}
	if _, err := p.text(notRespondingKey, ""); err != nil {
		return governance.OutboxMessage{}, err
	}
	payload, err := marshalPayload(p)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the not-responding comment: %w", err)
	}
	return governance.OutboxMessage{Key: p.key(), Kind: githubCommentKind, Payload: payload}, nil
}

func (p runnerBackPayload) key() string {
	return fmt.Sprintf("%s:%d:%d:%s", runnerBackKey, p.RepositoryID, p.Pull, p.Head)
}

func (p runnerBackPayload) validate() error {
	if err := p.commentTarget.validate(); err != nil {
		return err
	}
	return headAndTier(p.Head, p.Tier)
}

func (p runnerBackPayload) text(key, link string) (string, error) {
	return render(githubCommentKind, key, func() string {
		return commandtext.RunnerBackComment(commandtext.RunnerBack{Head: p.Head, Pull: p.Pull, PullURL: link})
	})
}

// newRunnerBackMessage builds the runner-back comment of a head.
func newRunnerBackMessage(p runnerBackPayload) (governance.OutboxMessage, error) {
	if err := p.validate(); err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("runner-back comment: %w", err)
	}
	if _, err := p.text(runnerBackKey, ""); err != nil {
		return governance.OutboxMessage{}, err
	}
	payload, err := marshalPayload(p)
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the runner-back comment: %w", err)
	}
	return governance.OutboxMessage{Key: p.key(), Kind: githubCommentKind, Payload: payload}, nil
}

// pageLink is the link to the page of the pull request that a comment carries, from the settings: empty without a base address, and empty (with a line in the log) when
// the renderer would refuse it, because the comment is more than its link.
func (p *commentPublisher) pageLink(ctx context.Context, key string, target commentTarget) string {
	if p.reportBase == "" {
		return ""
	}
	link := pullPageURL(p.reportBase, target.Repo, target.Pull)
	if !commandtext.ValidPullLink(link, target.Pull) {
		p.logger.WarnContext(ctx, "github_comment: the comment is posted without its link, which the renderer refuses", "key", key)
		return ""
	}
	return link
}

// decodeResults reads the payload of a results comment strictly: no unknown field, no trailing value and every count of a run present.
func decodeResults(key string, payload json.RawMessage) (resultsPayload, error) {
	p, err := decodePayload[resultsPayload](githubCommentKind, key, payload)
	if err != nil {
		return p, err
	}
	var raw struct {
		Canonical *struct {
			Counts map[string]json.RawMessage `json:"counts"`
		} `json:"canonical"`
		Receipt *struct {
			Counts map[string]json.RawMessage `json:"counts"`
		} `json:"receipt"`
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		return p, keyError(githubCommentKind, key, "the payload does not decode")
	}
	if raw.Canonical != nil && raw.Canonical.Counts != nil {
		if err := requireKeys(key, raw.Canonical.Counts, canonicalCountKeys); err != nil {
			return p, err
		}
	}
	if raw.Receipt != nil && raw.Receipt.Counts != nil {
		if err := requireKeys(key, raw.Receipt.Counts, receiptCountKeys); err != nil {
			return p, err
		}
	}
	return p, nil
}

func requireKeys(key string, counts map[string]json.RawMessage, names []string) error {
	for _, name := range names {
		if _, present := counts[name]; !present {
			return keyError(githubCommentKind, key, "a count of a run is missing")
		}
	}
	return nil
}

func (p *commentPublisher) results(ctx context.Context, key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	r, err := decodeResults(key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	if r.validate() != nil || tail != jobsTail(r.commentTarget, r.Head, r.jobs()) {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	text, err := r.text(key, p.pageLink(ctx, key, r.commentTarget))
	return r.commentTarget, text, err
}

func (p *commentPublisher) notResponding(ctx context.Context, key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	n, err := decodePayload[notRespondingPayload](githubCommentKind, key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	if n.validate() != nil || tail != jobsTail(n.commentTarget, n.Head, n.jobs()) {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	text, err := n.text(key, p.pageLink(ctx, key, n.commentTarget))
	return n.commentTarget, text, err
}

func (p *commentPublisher) runnerBack(ctx context.Context, key, tail string, payload json.RawMessage) (commentTarget, string, error) {
	b, err := decodePayload[runnerBackPayload](githubCommentKind, key, payload)
	if err != nil {
		return commentTarget{}, "", err
	}
	if b.validate() != nil || tail != fmt.Sprintf("%d:%s", b.Pull, b.Head) {
		return commentTarget{}, "", keyError(githubCommentKind, key, "the payload does not match its key")
	}
	text, err := b.text(key, p.pageLink(ctx, key, b.commentTarget))
	return b.commentTarget, text, err
}
