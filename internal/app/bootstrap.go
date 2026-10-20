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
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
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
	githubCommentKind     = "github_comment"
	bootstrapSummaryKey   = "bootstrap-summary"
	bootstrapPromotedKey  = "bootstrap-promoted"
	bootstrapNoticeKey    = "bootstrap-notice"    // bootstrap-notice:<repository id>:<pull>, written by the checks job
	bootstrapCancelledKey = "bootstrap-cancelled" // bootstrap-cancelled:<repository id>:P<n>-R<m>, written by the checks job
	sourceBranch          = "branch"              // the payload values of bootstrapSummaryInputs.Source
	sourcePull            = "pull"

	defaultSuite   contract.SuiteID            = "default"
	firstVersion   contract.SuiteVersionID     = "V1"
	firstRevision  contract.ProposalRevisionID = "R1"
	targetPrefix                               = "branch:"
	carrierPrefix                              = "github-pr:"
	maxSummaryRun                              = 20  // paths, include, runner and exclude rules the summary carries
	maxSummaryText                             = 150 // bytes of one path, one rule or the branch in the payload
	maxRunnerText                              = 30  // bytes of one runner rule: with a repository of the 140 characters GitHub allows, three lists of 150 bytes, the branch and 20 of these make 59.2 KB when every byte is escaped, 2 KB under maxOutboxPayload
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

// bootstrapSummaryInputs mirror commandtext.Summary. Repository text (Branch, Paths, Include, Runner, Exclude) is cut to
// maxSummaryText bytes, a cut entry ending in "…", and each list holds at most maxSummaryRun entries, so the payload
// stays within the outbox limit (a runner rule to maxRunnerText bytes, so that four lists fit). The totals are the true counts.
type bootstrapSummaryInputs struct {
	Proposal     int      `json:"proposal"`
	Revision     int      `json:"revision"`
	Source       string   `json:"source"`           // branch or pull
	Origin       string   `json:"origin"`           // the pinned 40-hex commit, or the head of the pull request
	Branch       string   `json:"branch,omitempty"` // absent from a pull request
	Protected    int      `json:"protected"`        // all protected files, of which Paths lists the first ones
	Paths        []string `json:"paths"`
	Include      []string `json:"include"`               // the declared include rules
	Runner       []string `json:"runner,omitzero"`       // the declared runner rules; nil in a message written before M1.9-E4, which omitempty would not tell from none
	Exclude      []string `json:"exclude"`               // the declared exclude rules
	IncludeTotal int      `json:"includeTotal"`          // how many include rules the declaration has, of which Include lists the first ones
	ExcludeTotal int      `json:"excludeTotal"`          // likewise for exclude
	RunnerTotal  int      `json:"runnerTotal,omitempty"` // likewise for runner
	Tier         string   `json:"tier"`                  // the effective tier: owner.TierIsolated or owner.TierCoLocated
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
	var source commandtext.Source
	switch s.Source {
	case "", sourceBranch: // a message written before M1.7b has no source
	case sourcePull:
		source = commandtext.SourcePull
	default:
		return commandtext.Summary{}, fmt.Errorf("unknown source %q", s.Source)
	}
	return commandtext.Summary{Proposal: s.Proposal, Revision: s.Revision, Source: source, Origin: s.Origin, Branch: s.Branch, Protected: s.Protected,
		Paths: s.Paths, Include: s.Include, Runner: s.Runner, Exclude: s.Exclude,
		IncludeTotal: s.IncludeTotal, RunnerTotal: s.RunnerTotal, ExcludeTotal: s.ExcludeTotal, Tier: tier}, nil
}

// bootstrapPromotedPayload is the payload of bootstrap-promoted:<repository id>:P<n>-R1.
type bootstrapPromotedPayload struct {
	commentTarget
	Reference string `json:"reference"` // P<n>-R<m>
	Version   string `json:"version"`   // V1
}

// bootstrapNoticePayload is the payload of bootstrap-notice:<repository id>:<pull>: the pull request to say how to start or skip on.
type bootstrapNoticePayload struct{ commentTarget }

// bootstrapCancelledPayload is the payload of bootstrap-cancelled:<repository id>:P<n>-R<m>: the carrier whose proposal a push cancelled.
type bootstrapCancelledPayload struct {
	commentTarget
	Reference string `json:"reference"` // P<n>-R<m>, the cancelled revision
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

// maxOutboxPayload is what a message of the comment kinds that carry repository text may marshal to: the outbox takes 64 KiB, and
// JSON writes <, > and & as six bytes, so text that is long in escapes could fill it. A payload over it cannot be rendered.
const maxOutboxPayload = 60 << 10

// maxDeclarationBytes caps the .suiteward.yml of an untrusted tree before it is parsed: a real declaration is a few
// lines, and the YAML parser should not be handed whatever a head branch puts there.
const maxDeclarationBytes = 64 << 10

// marshalPayload encodes a payload, and refuses one over maxOutboxPayload.
func marshalPayload(payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxOutboxPayload {
		return nil, fmt.Errorf("the payload is %d bytes, over %d", len(raw), maxOutboxPayload)
	}
	return raw, nil
}

// boundedText returns at most maxSummaryRun entries of list, each cut to maxSummaryText bytes; never nil, so the
// payload holds an array.
func boundedText(list []string) []string { return boundedTextTo(list, maxSummaryText) }

// boundedTextTo is boundedText with each entry cut to limit bytes.
func boundedTextTo(list []string, limit int) []string {
	bounded := make([]string, 0, min(len(list), maxSummaryRun))
	for _, entry := range list[:min(len(list), maxSummaryRun)] {
		bounded = append(bounded, cutBytes(entry, limit))
	}
	return bounded
}

// newSummaryMessage builds the bootstrap-summary message of the proposal in inputs, bounding its repository text.
func newSummaryMessage(target commentTarget, inputs bootstrapSummaryInputs) (governance.OutboxMessage, error) {
	inputs.IncludeTotal, inputs.RunnerTotal, inputs.ExcludeTotal = len(inputs.Include), len(inputs.Runner), len(inputs.Exclude) // what the owner is told is left out
	inputs.Branch = cutBytes(inputs.Branch, maxSummaryText)
	inputs.Paths, inputs.Include, inputs.Runner, inputs.Exclude = boundedText(inputs.Paths), boundedText(inputs.Include), boundedTextTo(inputs.Runner, maxRunnerText), boundedText(inputs.Exclude)
	payload, err := marshalPayload(bootstrapSummaryPayload{commentTarget: target, Summary: inputs})
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

// newNoticeMessage builds the bootstrap-notice message of the pull request in target.
func newNoticeMessage(target commentTarget) (governance.OutboxMessage, error) {
	payload, err := json.Marshal(bootstrapNoticePayload{target})
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the bootstrap notice: %w", err)
	}
	return governance.OutboxMessage{Key: fmt.Sprintf("%s:%d:%d", bootstrapNoticeKey, target.RepositoryID, target.Pull), Kind: githubCommentKind, Payload: payload}, nil
}

// newCancelledMessage builds the bootstrap-cancelled message of the proposal revision reference (P<n>-R<m>) carried by the pull request in target.
func newCancelledMessage(target commentTarget, reference string) (governance.OutboxMessage, error) {
	payload, err := json.Marshal(bootstrapCancelledPayload{commentTarget: target, Reference: reference})
	if err != nil {
		return governance.OutboxMessage{}, fmt.Errorf("encode the bootstrap cancellation: %w", err)
	}
	return governance.OutboxMessage{Key: fmt.Sprintf("%s:%d:%s", bootstrapCancelledKey, target.RepositoryID, reference), Kind: githubCommentKind, Payload: payload}, nil
}

// inventoryInputs adds what the inventory says to the inputs of a summary: the protected files and the scope rules.
func inventoryInputs(inputs bootstrapSummaryInputs, inv inventory.Inventory) bootstrapSummaryInputs {
	inputs.Protected = len(inv.Manifest().Entries())
	for _, entry := range inv.Manifest().Entries() {
		inputs.Paths = append(inputs.Paths, entry.Path)
	}
	for _, rule := range inv.Scope().Declaration().Include() {
		inputs.Include = append(inputs.Include, rule.String())
	}
	for _, rule := range inv.Scope().Declaration().Runner() {
		inputs.Runner = append(inputs.Runner, rule.String())
	}
	for _, rule := range inv.Scope().Declaration().Exclude() {
		inputs.Exclude = append(inputs.Exclude, rule.String())
	}
	return inputs
}

// bootstrapReader is the part of the GitHub reader the bootstrap uses: it creates the Suite and re-reads the origin of an approved baseline.
type bootstrapReader interface {
	Repository(ctx context.Context, repo string) (github.Repository, error)
	Tree(ctx context.Context, repo, commitSHA string) (inventory.Tree, error)
}

// pullLister is the part of the observation store the checks job reads.
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

// bootstrapJob is the github_bootstrap job: for each enrolled project it creates the Suite and bootstraps the first
// canonical from the Suite's latest bootstrap proposal once it is a branch proposal the owner approved (ADR 0010,
// D-COMMANDS). It proposes nothing: the commands job writes every proposal. Every step is safe to repeat after a crash.
type bootstrapJob struct {
	repos    []string // the configured repositories
	reader   bootstrapReader
	owners   owner.Store
	suites   governance.UnitOfWork
	problems *bootstrapProblems // shown in /status
	logger   *slog.Logger
	now      func() time.Time

	promoted bool // the current run promoted V1; runs never overlap
}

// run advances every project and wakes the checks when it promoted V1 (docs/contracts/runtime.md, "Queues and
// wake-ups"). A run that failed after a promotion still wakes them.
func (b *bootstrapJob) run(ctx context.Context) (river.Wake, error) {
	b.promoted = false
	err := b.advanceAll(ctx)
	if b.promoted {
		return river.Wake{river.ChecksKind}, err
	}
	return nil, err
}

// advanceAll advances every enrolled, configured project. A GitHub read failure skips that project until the next
// run; any other failure fails the run after the other projects were tried.
func (b *bootstrapJob) advanceAll(ctx context.Context) error {
	return forEachConfiguredProject(ctx, b.owners, b.repos, "bootstrap", b.project)
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
	if state.canonical || !state.branch || !state.approved {
		return nil
	}
	return b.promote(ctx, p, state)
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
	branch    bool // the Suite's latest bootstrap proposal is a branch proposal: the fields below are its
	reference contract.ProposalReference
	carrier   contract.ApprovalCarrierID
	origin    contract.SourceRevision
	binding   contract.ApprovalBinding
	approved  bool // the owner's approval of its current revision is current
	assessed  bool // an assessment of the origin is recorded
	passed    bool // and it passed
}

func proposalID(n int) contract.ProposalID { return contract.ProposalID("P" + strconv.Itoa(n)) }

func carrierID(repositoryID int64, pull int) contract.ApprovalCarrierID {
	return contract.ApprovalCarrierID(carrierPrefix + strconv.FormatInt(repositoryID, 10) + "/" + strconv.Itoa(pull))
}

// pullOfCarrier is the pull request number of a carrier the commands job stored.
func pullOfCarrier(repositoryID int64, carrier contract.ApprovalCarrierID) (int, error) {
	number, ok := strings.CutPrefix(string(carrier), carrierPrefix+strconv.FormatInt(repositoryID, 10)+"/")
	pull, err := strconv.Atoi(number)
	if !ok || err != nil {
		return 0, fmt.Errorf("the stored carrier %q is not a pull request of repository %d", carrier, repositoryID)
	}
	return pull, nil
}

// baseline reads the Suite. The bootstrap decides only on the Suite's latest bootstrap proposal, and only when it is a
// branch proposal: a pull proposal becomes canonical by the merge of its carrier, and a newer proposal of either kind
// supersedes an older one.
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
		id, found, err := tx.LatestBootstrapProposal(ctx)
		if err != nil || !found {
			return err
		}
		proposal, consent, err := tx.Proposal(ctx, id)
		if err != nil {
			return fmt.Errorf("load the latest bootstrap proposal: %w", err)
		}
		current := proposal.Current()
		if s.branch = current.OriginKind() == contract.OriginBranch; !s.branch {
			return nil
		}
		s.reference, s.carrier, s.origin, s.binding = current.Binding().Reference(), current.Carrier(), current.Origin(), current.Binding()
		s.approved = consent.HasApproval(proposal, state.Policy)
		assessment, found, err := tx.Assessment(ctx, s.reference, s.origin)
		if err != nil {
			return fmt.Errorf("load assessment: %w", err)
		}
		s.assessed, s.passed = found, found && assessment.Passed()
		return nil
	})
	return s, err
}

// promote records the integrity assessment of the origin and bootstraps the first canonical from the approved baseline.
// Only a Passed assessment promotes. A Failed or Unavailable one is recorded, and the next runs stop at that
// record without reading GitHub: the origin is a pinned commit, so reading it again cannot change the outcome. Such an
// approved proposal never moves to P<n+1>, because only an unapproved one does.
func (b *bootstrapJob) promote(ctx context.Context, p owner.ProjectPolicy, s baselineState) error {
	if s.assessed && !s.passed {
		b.logger.InfoContext(ctx, "github bootstrap stopped: the origin did not pass its integrity check", "repo", p.Repo, "proposal", s.reference.ProposalID)
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
		b.logger.WarnContext(ctx, "github bootstrap stopped: the recorded assessment of the origin disagrees with this read", "repo", p.Repo, "proposal", s.reference.ProposalID)
		return bootstrapStop("assessment-conflict")
	case err != nil:
		return fmt.Errorf("record the assessment: %w", err)
	case evidence.Outcome() != contract.IntegrityPassed:
		b.logger.WarnContext(ctx, "github bootstrap stopped: the origin did not pass its integrity check", "repo", p.Repo, "proposal", s.reference.ProposalID)
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
		code, err := blockedCode(result.Reason)
		if err != nil {
			return err
		}
		b.logger.WarnContext(ctx, "github bootstrap blocked", "repo", p.Repo, "proposal", s.reference.ProposalID, "reason", result.Reason)
		return bootstrapStop(code)
	}
	b.promoted = true
	b.logger.InfoContext(ctx, "github baseline promoted", "repo", p.Repo, "reference", reference, "version", string(firstVersion), "replayed", result.Duplicate)
	return nil
}

// blockedReasons name the reasons of a blocked promotion in the codes of /status.
var blockedReasons = map[contract.PromotionReason]string{
	contract.PromotionReasonContextMismatch:     "context-mismatch",
	contract.PromotionReasonProposalNotCurrent:  "proposal-not-current",
	contract.PromotionReasonCanonicalChanged:    "canonical-changed",
	contract.PromotionReasonPolicyChanged:       "policy-changed",
	contract.PromotionReasonApprovalMissing:     "approval-missing",
	contract.PromotionReasonAssessmentMismatch:  "assessment-mismatch",
	contract.PromotionReasonIntegrityNotPassed:  "integrity-not-passed",
	contract.PromotionReasonIntegrationMissing:  "integration-missing",
	contract.PromotionReasonIntegrationMismatch: "integration-mismatch",
	contract.PromotionReasonCanonicalPresent:    "canonical-present",
	contract.PromotionReasonEmptyInventory:      "empty-inventory",
}

// blockedCode is the classification code of a blocked bootstrap, naming the reason. A reason a bootstrap cannot give
// (none, or the correction's) is an error, not a code.
func blockedCode(reason contract.PromotionReason) (string, error) {
	name, known := blockedReasons[reason]
	if !known {
		return "", fmt.Errorf("a bootstrap was blocked for the reason %d, which it cannot give", reason)
	}
	return "blocked-" + name, nil
}

// bootstrapStop is why a project's bootstrap stopped or is waiting, without failing the run: a fixed classification code for
// /status, never text from GitHub. The step that stops logs its own reason.
type bootstrapStop string

func (s bootstrapStop) Error() string { return "bootstrap stopped: " + string(s) }

// forEachConfiguredProject calls fn for every enrolled project whose repository is configured: the checks, merges and bootstrap jobs, which write only for
// the configured repositories. A cancelled run ends at once; any other failure is labelled and joined after every project was tried.
func forEachConfiguredProject(ctx context.Context, owners ownerSource, repos []string, label string, fn func(context.Context, owner.State, owner.ProjectPolicy) error) error {
	enrollment, enrolled, err := owners.Current(ctx)
	if err != nil {
		return fmt.Errorf("read the enrollment: %w", err)
	}
	if !enrolled { // nothing is written without an owner; /status says why
		return nil
	}
	var failures []error
	for _, project := range enrollment.Projects {
		if !slices.Contains(repos, project.Repo) {
			continue
		}
		if err := fn(ctx, enrollment, project); err != nil {
			if cancelled := ctx.Err(); cancelled != nil {
				return cancelled
			}
			failures = append(failures, fmt.Errorf("%s %s: %w", label, project.Repo, err))
		}
	}
	return errors.Join(failures...)
}
