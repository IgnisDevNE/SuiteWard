package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The identifiers of D-COMMANDS and the outbox keys of the bootstrap comments
// (docs/contracts/runtime.md, "Command and bootstrap jobs (M1.6a)").
const (
	githubCommentKind    = "github_comment"
	bootstrapSummaryKey  = "bootstrap-summary"
	bootstrapPromotedKey = "bootstrap-promoted"

	defaultSuite   contract.SuiteID            = "default"
	firstVersion   contract.SuiteVersionID     = "V1"
	firstRevision  contract.ProposalRevisionID = "R1"
	targetPrefix                               = "branch:"
	carrierPrefix                              = "github-pr:"
	maxSummaryRun                              = 20  // paths, include rules and exclude rules the summary carries
	maxSummaryText                             = 150 // bytes of one path, one rule or the branch in the payload
)

// commentTarget is what every github_comment payload carries: the repository
// and the pull request to post on.
type commentTarget struct {
	RepositoryID int64  `json:"repositoryId"`
	Repo         string `json:"repo"` // owner/name
	Pull         int    `json:"pull"`
}

// bootstrapSummaryPayload is the payload of bootstrap-summary:<repository id>:P<n>-R1: the inputs of the
// summary the publisher renders with commandtext.BootstrapSummary.
type bootstrapSummaryPayload struct {
	commentTarget
	Summary bootstrapSummaryInputs `json:"summary"`
}

// bootstrapSummaryInputs mirror commandtext.Summary. Repository text (Branch, Paths, Include, Exclude) is cut to
// maxSummaryText bytes, a cut entry ending in "…", and each list holds at most maxSummaryRun entries, so the payload
// stays within the outbox limit. The totals are the true counts.
type bootstrapSummaryInputs struct {
	Proposal     int      `json:"proposal"`
	Revision     int      `json:"revision"`
	Origin       string   `json:"origin"` // the pinned 40-hex commit
	Branch       string   `json:"branch"`
	Protected    int      `json:"protected"` // all protected files, of which Paths lists the first ones
	Paths        []string `json:"paths"`
	Include      []string `json:"include"`      // the declared include rules
	Exclude      []string `json:"exclude"`      // the declared exclude rules
	IncludeTotal int      `json:"includeTotal"` // how many include rules the declaration has, of which Include lists the first ones
	ExcludeTotal int      `json:"excludeTotal"` // likewise for exclude
	Tier         string   `json:"tier"`         // the effective tier: owner.TierIsolated or owner.TierCoLocated
}

// summary maps the inputs to the values the renderer takes.
func (s bootstrapSummaryInputs) summary() (commandtext.Summary, error) {
	var tier commandtext.Tier
	switch s.Tier {
	case owner.TierIsolated:
		tier = commandtext.Isolated
	case owner.TierCoLocated:
		tier = commandtext.CoLocated
	default:
		return commandtext.Summary{}, fmt.Errorf("unknown tier %q", s.Tier)
	}
	return commandtext.Summary{Proposal: s.Proposal, Revision: s.Revision, Origin: s.Origin, Branch: s.Branch, Protected: s.Protected,
		Paths: s.Paths, Include: s.Include, Exclude: s.Exclude,
		IncludeTotal: s.IncludeTotal, ExcludeTotal: s.ExcludeTotal, Tier: tier}, nil
}

// bootstrapPromotedPayload is the payload of bootstrap-promoted:<repository id>:P<n>-R1.
type bootstrapPromotedPayload struct {
	commentTarget
	Reference string `json:"reference"` // P<n>-R<m>
	Version   string `json:"version"`   // V1
}

// cutMarker ends an entry that cutBytes shortened, so a cut path or rule never looks complete.
const cutMarker = "…"

// cutBytes shortens s to at most limit bytes, marker included, on a UTF-8 boundary. Bytes that are not UTF-8 have no
// boundary: the cut backs up over at most one rune's worth, so a string of stray bytes is never cut to nothing.
func cutBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit - len(cutMarker)
	for back := 0; back < utf8.UTFMax-1 && !utf8.RuneStart(s[cut]); back++ {
		cut--
	}
	return s[:cut] + cutMarker
}

// boundedText returns at most maxSummaryRun entries of list, each cut to maxSummaryText bytes; never nil, so the
// payload holds an array.
func boundedText(list []string) []string {
	bounded := make([]string, 0, min(len(list), maxSummaryRun))
	for _, entry := range list[:min(len(list), maxSummaryRun)] {
		bounded = append(bounded, cutBytes(entry, maxSummaryText))
	}
	return bounded
}

// newSummaryMessage builds the bootstrap-summary message of the proposal in inputs, bounding its repository text.
func newSummaryMessage(target commentTarget, inputs bootstrapSummaryInputs) (governance.OutboxMessage, error) {
	inputs.IncludeTotal, inputs.ExcludeTotal = len(inputs.Include), len(inputs.Exclude) // what the owner is told is left out
	inputs.Branch = cutBytes(inputs.Branch, maxSummaryText)
	inputs.Paths, inputs.Include, inputs.Exclude = boundedText(inputs.Paths), boundedText(inputs.Include), boundedText(inputs.Exclude)
	payload, err := json.Marshal(bootstrapSummaryPayload{commentTarget: target, Summary: inputs})
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the bootstrap summary: %w", err)
	}
	key := fmt.Sprintf("%s:%d:%s", bootstrapSummaryKey, target.RepositoryID, commandtext.Reference(inputs.Proposal, inputs.Revision))
	return governance.OutboxMessage{Key: key, Kind: githubCommentKind, Payload: payload}, nil
}

// newPromotedMessage builds the bootstrap-promoted message of the baseline reference (P<n>-R<m>).
func newPromotedMessage(target commentTarget, reference string) (governance.OutboxMessage, error) {
	payload, err := json.Marshal(bootstrapPromotedPayload{commentTarget: target, Reference: reference, Version: string(firstVersion)})
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the bootstrap promotion: %w", err)
	}
	return governance.OutboxMessage{Key: fmt.Sprintf("%s:%d:%s", bootstrapPromotedKey, target.RepositoryID, reference), Kind: githubCommentKind, Payload: payload}, nil
}

// bootstrapReader is the part of the GitHub reader the bootstrap uses.
type bootstrapReader interface {
	Repository(ctx context.Context, repo string) (github.Repository, error)
	BranchHead(ctx context.Context, repo, branch string) (string, error)
	Tree(ctx context.Context, repo, commitSHA string) (inventory.Tree, error)
}

// pullLister is the part of the observation store the bootstrap reads.
type pullLister interface {
	Pulls(ctx context.Context, repo string) ([]githubsync.PullState, error)
}

var (
	// errGitHubRead marks an error caused by reading GitHub: the project waits for the next run. Its text is never
	// logged, because it may hold what GitHub sent.
	errGitHubRead = errors.New("github read failed")
	// errRepositoryChanged marks a repository name that no longer identifies the enrolled repository.
	errRepositoryChanged = errors.New("repository is not the enrolled one")
)

// opaque is an error whose text is fixed. A run error ends in river_job.errors, and what a lower layer says may hold
// a path or a branch name the repository chose; Unwrap keeps errors.Is and errors.As working on the cause.
type opaque struct {
	what  string
	cause error
}

func (e *opaque) Error() string { return e.what + " failed" }
func (e *opaque) Unwrap() error { return e.cause }

// githubRead marks err as a failed GitHub read, unless it already says that the source itself is invalid.
func githubRead(err error) error {
	if errors.Is(err, inventory.ErrInvalidTree) {
		return err
	}
	return fmt.Errorf("%w: %w", errGitHubRead, err)
}

// invalidSource reports whether err says the source tree or its declaration cannot be used, which is a fact about
// the commit and not a failed read.
func invalidSource(err error) bool {
	return errors.Is(err, inventory.ErrInvalidTree) || errors.Is(err, inventory.ErrUnsafeEntry) || errors.Is(err, scope.ErrInvalidDeclaration)
}

// githubTree is an inventory.Tree whose failures are GitHub reads and that reads each file once: the inventory,
// the summary and the content store all need the same bytes.
type githubTree struct {
	tree  inventory.Tree
	files map[string][]byte
}

func (t *githubTree) Entries(ctx context.Context) ([]inventory.Entry, error) {
	entries, err := t.tree.Entries(ctx)
	if err != nil {
		return nil, githubRead(err)
	}
	return entries, nil
}

func (t *githubTree) Read(ctx context.Context, path string) ([]byte, error) {
	if data, read := t.files[path]; read {
		return data, nil
	}
	data, err := t.tree.Read(ctx, path)
	if err != nil {
		return nil, githubRead(err)
	}
	t.files[path] = data
	return data, nil
}

// bootstrapProblems keeps, per configured repository name, the classification code of the last problem that stopped
// the bootstrap of its project. It is in memory only: /status shows it, and a restart forgets it until the next run.
type bootstrapProblems struct {
	mu     sync.Mutex
	byRepo map[string]string
}

func (p *bootstrapProblems) set(repo, code string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byRepo == nil {
		p.byRepo = map[string]string{}
	}
	p.byRepo[repo] = code
}

func (p *bootstrapProblems) clear(repo string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.byRepo, repo)
}

func (p *bootstrapProblems) lookup(repo string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.byRepo[repo]
}

// bootstrapJob is the github_bootstrap job: for each enrolled project it creates the Suite, proposes the
// protected baseline of the default branch on the lowest open pull request, and bootstraps the first canonical once
// the owner approved it (ADR 0010, D-COMMANDS). Every step is safe to repeat after a crash.
type bootstrapJob struct {
	repos    []string // the configured repositories
	reader   bootstrapReader
	owners   owner.Store
	suites   governance.UnitOfWork
	content  governance.ContentWriter
	pulls    pullLister         // the synchronization store: which pull requests are open
	tier     string             // the configured INSTALLATION_TIER
	problems *bootstrapProblems // shown in /status
	logger   *slog.Logger
	now      func() time.Time
}

// run advances every enrolled, configured project. A GitHub read failure skips that project until the next run;
// any other failure fails the run after the other projects were tried.
func (b *bootstrapJob) run(ctx context.Context) error {
	enrollment, enrolled, err := b.owners.Current(ctx)
	if err != nil {
		return fmt.Errorf("read the enrollment: %w", err)
	}
	if !enrolled {
		return nil
	}
	var failures []error
	for _, project := range enrollment.Projects {
		if !slices.Contains(b.repos, project.Repo) {
			continue
		}
		if err := b.project(ctx, enrollment, project); err != nil {
			if cancelled := ctx.Err(); cancelled != nil {
				return cancelled
			}
			failures = append(failures, fmt.Errorf("bootstrap %s: %w", project.Repo, err))
		}
	}
	return errors.Join(failures...)
}

// project advances one project and returns only the failures that fail the run.
func (b *bootstrapJob) project(ctx context.Context, enrollment owner.State, p owner.ProjectPolicy) error {
	err := b.advance(ctx, enrollment, p)
	var stopped bootstrapStop
	switch {
	case err == nil:
		b.problems.clear(p.Repo)
		return nil
	case ctx.Err() != nil: // a cancelled read is not a GitHub failure
		return ctx.Err()
	case errors.Is(err, errGitHubRead):
		logReadFailure(ctx, b.logger, "github bootstrap read failed", p.Repo, err)
		b.problems.set(p.Repo, "read-failed")
		return nil
	case errors.Is(err, errRepositoryChanged):
		b.logger.WarnContext(ctx, "github bootstrap skipped: the repository name no longer identifies the enrolled repository", "repo", p.Repo)
		b.problems.set(p.Repo, "repository-changed")
		return nil
	case invalidSource(err):
		b.logger.WarnContext(ctx, "github bootstrap skipped: the default branch cannot be proposed as a baseline", "repo", p.Repo)
		b.problems.set(p.Repo, "invalid-source")
		return nil
	case errors.As(err, &stopped):
		b.problems.set(p.Repo, string(stopped))
		return nil
	}
	b.problems.set(p.Repo, "failed")
	return err
}

func (b *bootstrapJob) advance(ctx context.Context, enrollment owner.State, p owner.ProjectPolicy) error {
	project := owner.ProjectID(p.RepositoryID)
	state, err := b.baseline(ctx, project)
	if errors.Is(err, governance.ErrNotFound) { // the Suite does not exist: the only case that creates it
		repo, err := b.repository(ctx, p)
		if err != nil {
			return err
		}
		if err := b.suites.CreateSuite(ctx, project, defaultSuite, enrollment.PolicyRevision, contract.IntegrationTargetID(targetPrefix+repo.DefaultBranch)); err != nil {
			return &opaque{what: "create the suite", cause: err}
		}
		state, err = b.baseline(ctx, project)
		if err != nil {
			return fmt.Errorf("read the baseline state: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("read the baseline state: %w", err)
	}
	switch {
	case state.canonical:
		return nil
	case state.approved:
		return b.promote(ctx, p, state)
	default:
		return b.propose(ctx, enrollment, p, state)
	}
}

// repository reads the repository and requires it to be the enrolled one.
func (b *bootstrapJob) repository(ctx context.Context, p owner.ProjectPolicy) (github.Repository, error) {
	repo, err := b.reader.Repository(ctx, p.Repo)
	if err != nil {
		return github.Repository{}, githubRead(err)
	}
	if repo.ID != p.RepositoryID {
		return github.Repository{}, errRepositoryChanged
	}
	return repo, nil
}

// baselineState is what the bootstrap decides on, read from the Suite in one unit of work.
type baselineState struct {
	canonical bool
	target    contract.IntegrationTargetID
	latest    int // the number of the latest proposal; 0 when there is none
	reference contract.ProposalReference
	carrier   contract.ApprovalCarrierID
	origin    contract.SourceRevision
	binding   contract.ApprovalBinding
	approved  bool // the owner's approval of the latest revision is current
	assessed  bool // an assessment of the origin is recorded
	passed    bool // and it passed
}

func proposalID(n int) contract.ProposalID { return contract.ProposalID("P" + strconv.Itoa(n)) }

func carrierID(repositoryID int64, pull int) contract.ApprovalCarrierID {
	return contract.ApprovalCarrierID(carrierPrefix + strconv.FormatInt(repositoryID, 10) + "/" + strconv.Itoa(pull))
}

// pullOfCarrier is the pull request number of a carrier this job stored.
func pullOfCarrier(repositoryID int64, carrier contract.ApprovalCarrierID) (int, error) {
	number, ok := strings.CutPrefix(string(carrier), carrierPrefix+strconv.FormatInt(repositoryID, 10)+"/")
	pull, err := strconv.Atoi(number)
	if !ok || err != nil {
		return 0, fmt.Errorf("the stored carrier %q is not a pull request of repository %d", carrier, repositoryID)
	}
	return pull, nil
}

// baseline reads the Suite; the proposals have no listing, so it follows P1, P2, ... to the last one.
func (b *bootstrapJob) baseline(ctx context.Context, project contract.ProjectID) (s baselineState, err error) {
	err = b.suites.Do(ctx, project, defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return fmt.Errorf("load suite: %w", err)
		}
		_, s.canonical = state.Canonical.Suite().CurrentVersionID()
		s.target = state.Target
		if s.canonical {
			return nil
		}
		var (
			latest  contract.Proposal
			consent contract.Consent
		)
		for n := 1; ; n++ {
			proposal, granted, err := tx.Proposal(ctx, proposalID(n))
			if errors.Is(err, governance.ErrNotFound) {
				break
			}
			if err != nil {
				return fmt.Errorf("load proposal P%d: %w", n, err)
			}
			s.latest, latest, consent = n, proposal, granted
		}
		if s.latest == 0 {
			return nil
		}
		current := latest.Current()
		s.reference, s.carrier, s.origin, s.binding = current.Binding().Reference(), current.Carrier(), current.Origin(), current.Binding()
		s.approved = consent.HasApproval(latest, state.Policy)
		assessment, found, err := tx.Assessment(ctx, s.reference, s.origin)
		if err != nil {
			return fmt.Errorf("load assessment: %w", err)
		}
		s.assessed, s.passed = found, found && assessment.Passed()
		return nil
	})
	return s, err
}

// openPulls returns the numbers of the pull requests the synchronization last saw open, lowest first.
func openPulls(pulls []githubsync.PullState) []int {
	var open []int
	for _, pull := range pulls {
		if pull.NotOpenSince.IsZero() {
			open = append(open, pull.Number)
		}
	}
	slices.Sort(open)
	return open
}

// propose proposes the baseline when the Suite has no proposal, or its latest proposal is unapproved and its carrier
// is no longer open, and the repository has an open pull request. "Open" is what the synchronization observed:
// it is the same store the command job approves from, so no extra GitHub call is needed.
func (b *bootstrapJob) propose(ctx context.Context, enrollment owner.State, p owner.ProjectPolicy, s baselineState) error {
	pulls, err := b.pulls.Pulls(ctx, p.Repo)
	if err != nil {
		return fmt.Errorf("read the observed pull requests: %w", err)
	}
	open := openPulls(pulls)
	if len(open) == 0 {
		return bootstrapStop("no-open-pull-request")
	}
	if s.latest > 0 && slices.ContainsFunc(open, func(n int) bool { return carrierID(p.RepositoryID, n) == s.carrier }) {
		return nil
	}
	branch, ok := strings.CutPrefix(string(s.target), targetPrefix)
	if !ok {
		return errors.New("the suite does not target a branch")
	}
	if _, err := b.repository(ctx, p); err != nil {
		return err
	}
	head, err := b.reader.BranchHead(ctx, p.Repo, branch)
	if err != nil {
		return githubRead(err)
	}
	read, err := b.reader.Tree(ctx, p.Repo, head)
	if err != nil {
		return githubRead(err)
	}
	tree := &githubTree{tree: read, files: map[string][]byte{}}
	inv, err := inventory.Build(ctx, tree)
	if err != nil {
		return &opaque{what: "inventory of the default branch", cause: err}
	}
	if len(inv.Manifest().Entries()) == 0 { // ADR 0010: never manufacture an empty canonical suite; Info because it repeats every run
		b.logger.InfoContext(ctx, "github bootstrap awaiting an initial contract: the default branch has no protected files", "repo", p.Repo)
		return bootstrapStop("awaiting-initial-contract")
	}
	tier, _ := effectiveTier(b.tier, enrollment, true)
	number, pull := s.latest+1, open[0]
	inputs := bootstrapSummaryInputs{Proposal: number, Revision: 1, Origin: head, Branch: branch, Protected: len(inv.Manifest().Entries()), Tier: tier}
	for _, entry := range inv.Manifest().Entries() {
		inputs.Paths = append(inputs.Paths, entry.Path)
	}
	for _, rule := range inv.Scope().Declaration().Include() {
		inputs.Include = append(inputs.Include, rule.String())
	}
	for _, rule := range inv.Scope().Declaration().Exclude() {
		inputs.Exclude = append(inputs.Exclude, rule.String())
	}
	message, err := newSummaryMessage(commentTarget{RepositoryID: p.RepositoryID, Repo: p.Repo, Pull: pull}, inputs)
	if err != nil {
		return err
	}
	request := governance.BaselineRequest{
		Reference: contract.ProposalReference{ProjectID: owner.ProjectID(p.RepositoryID), SuiteID: defaultSuite, ProposalID: proposalID(number), RevisionID: firstRevision},
		Carrier:   carrierID(p.RepositoryID, pull), Origin: contract.SourceRevision(head), Tree: tree, Outbox: []governance.OutboxMessage{message},
	}
	result, err := governance.ProposeBaseline(ctx, b.suites, b.content, request)
	if err != nil {
		return &opaque{what: "propose the baseline", cause: err}
	}
	b.logger.InfoContext(ctx, "github baseline proposed", "repo", p.Repo, "proposal", number, "pull", pull, "committed", result.Revise.Committed)
	return nil
}

// promote records the integrity assessment of the origin and bootstraps the first canonical from the approved baseline.
// Only a Passed assessment promotes. A Failed or Unavailable one is recorded, and the next runs stop at that
// record without reading GitHub: the origin is a pinned commit, so reading it again cannot change the outcome. Such an
// approved proposal never moves to P<n+1>, because only an unapproved one does.
func (b *bootstrapJob) promote(ctx context.Context, p owner.ProjectPolicy, s baselineState) error {
	if s.assessed && !s.passed {
		b.logger.InfoContext(ctx, "github bootstrap stopped: the origin did not pass its integrity check", "repo", p.Repo, "proposal", s.latest)
		return bootstrapStop("origin-not-passed")
	}
	var inv inventory.Inventory
	var evidence contract.IntegrityEvidence
	read, err := b.reader.Tree(ctx, p.Repo, string(s.origin))
	if err != nil {
		err = githubRead(err)
	} else {
		inv, err = inventory.Build(ctx, &githubTree{tree: read, files: map[string][]byte{}})
	}
	switch {
	case err == nil:
		evidence, err = governance.HeadEvidence(inv, s.binding, s.origin)
	case invalidSource(err): // the commit cannot be read as a valid tree: Unavailable
		evidence, err = contract.NewIntegrityEvidence(governance.ServiceEmitter.ID(), s.origin, s.binding, contract.IntegrityUnavailable)
	default: // a failed read says nothing about the commit, and a recorded outcome is immutable: wait for the next run
		return &opaque{what: "read the origin", cause: err}
	}
	if err != nil {
		return fmt.Errorf("build the integrity evidence: %w", err)
	}
	err = b.suites.Do(ctx, owner.ProjectID(p.RepositoryID), defaultSuite, func(ctx context.Context, tx governance.Tx) error {
		return tx.RecordAssessment(ctx, governance.AssessmentWrite{Reference: s.reference, Source: s.origin, Evidence: evidence})
	})
	switch {
	case errors.Is(err, governance.ErrConflict): // an earlier, different outcome stands; assessments are immutable
		b.logger.WarnContext(ctx, "github bootstrap stopped: the recorded assessment of the origin disagrees with this read", "repo", p.Repo, "proposal", s.latest)
		return bootstrapStop("assessment-conflict")
	case err != nil:
		return fmt.Errorf("record the assessment: %w", err)
	case evidence.Outcome() != contract.IntegrityPassed:
		b.logger.WarnContext(ctx, "github bootstrap stopped: the origin did not pass its integrity check", "repo", p.Repo, "proposal", s.latest)
		return bootstrapStop("origin-not-passed")
	}
	return b.bootstrap(ctx, p, s, inv)
}

func (b *bootstrapJob) bootstrap(ctx context.Context, p owner.ProjectPolicy, s baselineState, inv inventory.Inventory) error {
	proposed, err := inv.Contract()
	if err != nil {
		return fmt.Errorf("contract of the origin: %w", err)
	}
	project := owner.ProjectID(p.RepositoryID)
	integration, err := contract.NewIntegration(project, s.target, s.origin, "", contract.IntegrationExistingBaseline)
	if err != nil {
		return fmt.Errorf("integration of the origin: %w", err)
	}
	pull, err := pullOfCarrier(p.RepositoryID, s.carrier)
	if err != nil {
		return err
	}
	reference := fmt.Sprintf("%s-%s", s.reference.ProposalID, s.reference.RevisionID)
	message, err := newPromotedMessage(commentTarget{RepositoryID: p.RepositoryID, Repo: p.Repo, Pull: pull}, reference)
	if err != nil {
		return err
	}
	// PostgreSQL keeps microseconds and the store refuses a promotion time it would have to round.
	result, err := governance.Bootstrap(ctx, b.suites, governance.PromoteRequest{
		OperationID: contract.OperationID(fmt.Sprintf("bootstrap:%d:%s", p.RepositoryID, reference)), Reference: s.reference, Carrier: s.carrier, Proposed: proposed,
		AssessmentSource: s.origin, Integration: integration, NewVersionID: firstVersion, RecordedAt: b.now().Truncate(time.Microsecond), Outbox: []governance.OutboxMessage{message},
	})
	if err != nil {
		return fmt.Errorf("bootstrap the first canonical: %w", err)
	}
	if result.Outcome != contract.PromotionProposed {
		b.logger.WarnContext(ctx, "github bootstrap blocked", "repo", p.Repo, "proposal", s.latest, "reason", result.Reason)
		return bootstrapStop(blockedCode(result.Reason))
	}
	b.logger.InfoContext(ctx, "github baseline promoted", "repo", p.Repo, "reference", reference, "version", string(firstVersion), "replayed", result.Duplicate)
	return nil
}

// blockedReasons name the reasons of a blocked promotion in the codes of /status.
var blockedReasons = map[contract.PromotionReason]string{
	contract.PromotionReasonContextMismatch:         "context-mismatch",
	contract.PromotionReasonProposalNotCurrent:      "proposal-not-current",
	contract.PromotionReasonCanonicalChanged:        "canonical-changed",
	contract.PromotionReasonPolicyChanged:           "policy-changed",
	contract.PromotionReasonApprovalMissing:         "approval-missing",
	contract.PromotionReasonAssessmentMismatch:      "assessment-mismatch",
	contract.PromotionReasonIntegrityNotPassed:      "integrity-not-passed",
	contract.PromotionReasonIntegrationMissing:      "integration-missing",
	contract.PromotionReasonIntegrationMismatch:     "integration-mismatch",
	contract.PromotionReasonCanonicalPresent:        "canonical-present",
	contract.PromotionReasonEmptyInventory:          "empty-inventory",
	contract.PromotionReasonCorrectionContextReused: "correction-context-reused",
}

// blockedCode is the classification code of a blocked promotion, naming the reason.
func blockedCode(reason contract.PromotionReason) string {
	if name, known := blockedReasons[reason]; known {
		return "blocked-" + name
	}
	return "blocked"
}

// bootstrapStop is why a project's bootstrap stopped or is waiting, without failing the run: a fixed classification code for
// /status, never text from GitHub. The step that stops logs its own reason.
type bootstrapStop string

func (s bootstrapStop) Error() string { return "bootstrap stopped: " + string(s) }
