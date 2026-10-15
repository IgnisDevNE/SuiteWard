// Package governancetest provides an in-memory governance.UnitOfWork for
// behavior tests of the application layer. It mirrors the store semantics of
// the persistence contract in memory and does not verify artifact bytes.
package governancetest

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Memory implements governance.UnitOfWork and governance.Seeder. One mutex
// serializes every unit of work; each works on a copy that replaces the
// stored state only when the unit of work succeeds.
type Memory struct {
	mu       sync.Mutex
	data     data
	policies map[policyKey]contract.Policy // every policy a Seed has registered, for CreateSuite
	fail     error
}

type policyKey struct {
	project  contract.ProjectID
	revision contract.PolicyRevisionID
}

var (
	_ governance.UnitOfWork = (*Memory)(nil)
	_ governance.Seeder     = (*Memory)(nil)
)

// NewMemory returns an empty store; Seed adds Suites before any unit of work.
func NewMemory() *Memory {
	return &Memory{data: data{suites: map[suiteKey]*suiteData{}, byOp: map[contract.OperationID]governance.OperationReceipt{}, bySource: map[contract.SourceCommandID]governance.OperationReceipt{}, outbox: map[string]governance.OutboxMessage{}},
		policies: map[policyKey]contract.Policy{}}
}

// FailNext makes the next write of the next unit of work fail with err after
// it has been applied to the working copy, so a rollback discards a real
// effect. The failure is consumed by that unit of work even if it writes
// nothing.
func (m *Memory) FailNext(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = err
}

type suiteKey struct {
	project contract.ProjectID
	suite   contract.SuiteID
}

type assessmentKey struct {
	reference contract.ProposalReference
	source    contract.SourceRevision
}

// data holds the Suites and the global receipt indexes.
type data struct {
	suites   map[suiteKey]*suiteData
	byOp     map[contract.OperationID]governance.OperationReceipt
	bySource map[contract.SourceCommandID]governance.OperationReceipt
	jobs     []governance.Job
	outbox   map[string]governance.OutboxMessage
}

type suiteData struct {
	revision  contract.StateRevision
	current   contract.SuiteVersionID
	policy    contract.Policy
	target    contract.IntegrationTargetID
	history   map[contract.SuiteVersionID]contract.HistoricalCanonical
	proposals map[contract.ProposalID]*proposalData
	merges    map[contract.ApprovalCarrierID]governance.MergeOutcome // one outcome per carrier, never replaced
}

type proposalData struct {
	proposal    contract.Proposal
	assessments map[assessmentKey]contract.IntegrityAssessment
	results     []contract.CommandResult
	aliases     map[contract.OperationID]contract.SourceCommandID
}

func (d data) clone() data {
	c := data{suites: map[suiteKey]*suiteData{}, byOp: maps.Clone(d.byOp), bySource: maps.Clone(d.bySource), jobs: slices.Clone(d.jobs), outbox: maps.Clone(d.outbox)}
	for key, s := range d.suites {
		next := *s
		next.history = maps.Clone(s.history)
		next.merges = maps.Clone(s.merges)
		next.proposals = map[contract.ProposalID]*proposalData{}
		for id, p := range s.proposals {
			copied := *p
			copied.assessments, copied.results, copied.aliases = maps.Clone(p.assessments), slices.Clone(p.results), maps.Clone(p.aliases)
			next.proposals[id] = &copied
		}
		c.suites[key] = &next
	}
	return c
}

func (m *Memory) Do(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, fn func(context.Context, governance.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := suiteKey{project, suite}
	if _, ok := m.data.suites[key]; !ok {
		return governance.ErrNotFound
	}
	work := m.data.clone()
	t := &tx{d: &work, key: key, s: work.suites[key], fail: m.fail}
	m.fail = nil
	err := fn(ctx, t)
	t.closed = true
	if err == nil {
		err = ctx.Err() // a canceled unit of work cannot commit
	}
	if err != nil {
		return err
	}
	m.data = work
	return nil
}

// Seed installs trusted initial state for one Suite. It does not advance the
// revision and rejects a Suite that already exists.
func (m *Memory) Seed(_ context.Context, seed governance.Seed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := seed.Suite
	suite := state.Canonical.Suite()
	key := suiteKey{suite.ProjectID(), suite.ID()}
	current, _ := suite.CurrentVersionID()
	if key.suite == "" || state.Policy.ProjectID() != key.project || state.Target == "" {
		return fmt.Errorf("%w: incomplete suite state", governance.ErrInvalidRequest)
	}
	if _, exists := m.data.suites[key]; exists {
		return fmt.Errorf("%w: suite already seeded", governance.ErrInvalidRequest)
	}
	s := &suiteData{revision: suite.Revision(), current: current, policy: state.Policy, target: state.Target,
		history: map[contract.SuiteVersionID]contract.HistoricalCanonical{}, proposals: map[contract.ProposalID]*proposalData{}}
	for _, h := range seed.History {
		s.history[h.Version().ID()] = h
	}
	if _, ok := s.history[current]; current != "" && !ok {
		return fmt.Errorf("%w: current version missing from history", governance.ErrInvalidRequest)
	}
	for _, p := range seed.Proposals {
		id := p.Proposal.Current().Binding().Reference().ProposalID
		pd := &proposalData{proposal: p.Proposal, assessments: map[assessmentKey]contract.IntegrityAssessment{}, aliases: map[contract.OperationID]contract.SourceCommandID{}}
		for _, a := range p.Assessments {
			pd.assessments[assessmentKey{a.Binding().Reference(), a.Source()}] = a
		}
		s.proposals[id] = pd
	}
	m.data.suites[key] = s
	m.policies[policyKey{key.project, state.Policy.RevisionID()}] = state.Policy
	return nil
}

// CreateSuite creates an empty Suite under a policy that a Seed registered.
func (m *Memory) CreateSuite(ctx context.Context, project contract.ProjectID, suite contract.SuiteID, policy contract.PolicyRevisionID, target contract.IntegrationTargetID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if blank(string(project)) || blank(string(suite)) || blank(string(policy)) || blank(string(target)) {
		return fmt.Errorf("%w: a suite needs a project, an id, a policy and a target", governance.ErrInvalidRequest)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	governing, known := m.policies[policyKey{project, policy}]
	if !known {
		return fmt.Errorf("%w: policy %q of project %q", governance.ErrNotFound, policy, project)
	}
	key := suiteKey{project, suite}
	if existing, exists := m.data.suites[key]; exists {
		if existing.policy.RevisionID() != policy || existing.target != target {
			return fmt.Errorf("%w: suite %q of project %q is governed by policy %q targeting %q", governance.ErrConflict, suite, project, existing.policy.RevisionID(), existing.target)
		}
		return nil
	}
	m.data.suites[key] = &suiteData{policy: governing, target: target, history: map[contract.SuiteVersionID]contract.HistoricalCanonical{}, proposals: map[contract.ProposalID]*proposalData{}}
	return nil
}

func blank(value string) bool { return strings.TrimSpace(value) == "" }

type tx struct {
	d      *data
	key    suiteKey
	s      *suiteData
	fail   error // injected failure for the next write
	closed bool  // the unit of work has ended
}

// live fails once the unit of work has ended or its context is canceled.
func (t *tx) live(ctx context.Context) error {
	if t.closed {
		return errors.New("governancetest: transaction used after its unit of work ended")
	}
	return ctx.Err()
}

func (t *tx) Suite(ctx context.Context) (governance.SuiteState, error) {
	if err := t.live(ctx); err != nil {
		return governance.SuiteState{}, err
	}
	suite, err := contract.NewSuite(t.key.project, t.key.suite, t.s.current, t.s.revision)
	if err != nil {
		return governance.SuiteState{}, fmt.Errorf("%w: %w", governance.ErrInvalidState, err)
	}
	var version contract.SuiteVersion
	var protected contract.ProtectedContract
	var record contract.PromotionRecord
	if t.s.current != "" {
		h := t.s.history[t.s.current]
		version, record = h.Version(), h.Record()
		if protected, err = contract.NewProtectedContract(version.Manifest(), record.Binding().ScopeDigest(), record.Binding().CoveredInputs()); err != nil {
			return governance.SuiteState{}, fmt.Errorf("%w: %w", governance.ErrInvalidState, err)
		}
	}
	canonical, err := contract.NewCanonicalSnapshot(suite, version, protected, record)
	if err != nil {
		return governance.SuiteState{}, fmt.Errorf("%w: %w", governance.ErrInvalidState, err)
	}
	return governance.SuiteState{Canonical: canonical, Policy: t.s.policy, Target: t.s.target}, nil
}

func (t *tx) Proposal(ctx context.Context, id contract.ProposalID) (contract.Proposal, contract.Consent, error) {
	if err := t.live(ctx); err != nil {
		return contract.Proposal{}, contract.Consent{}, err
	}
	p, ok := t.s.proposals[id]
	if !ok {
		return contract.Proposal{}, contract.Consent{}, governance.ErrNotFound
	}
	consent, err := contract.ReconstituteConsent(p.proposal, p.results, p.aliases)
	if err != nil {
		return contract.Proposal{}, contract.Consent{}, fmt.Errorf("%w: %w", governance.ErrInvalidState, err)
	}
	return p.proposal, consent, nil
}

func (t *tx) Assessment(ctx context.Context, reference contract.ProposalReference, source contract.SourceRevision) (contract.IntegrityAssessment, bool, error) {
	if err := t.live(ctx); err != nil {
		return contract.IntegrityAssessment{}, false, err
	}
	if p, ok := t.s.proposals[reference.ProposalID]; ok {
		a, found := p.assessments[assessmentKey{reference, source}]
		return a, found, nil
	}
	return contract.IntegrityAssessment{}, false, nil
}

func (t *tx) Version(ctx context.Context, id contract.SuiteVersionID) (contract.HistoricalCanonical, bool, error) {
	if err := t.live(ctx); err != nil {
		return contract.HistoricalCanonical{}, false, err
	}
	h, ok := t.s.history[id]
	return h, ok, nil
}

func (t *tx) PromotionFor(ctx context.Context, reference contract.ProposalReference) (contract.PromotionRecord, bool, error) {
	if err := t.live(ctx); err != nil {
		return contract.PromotionRecord{}, false, err
	}
	record, found := t.promotionFor(reference)
	return record, found, nil
}

func (t *tx) promotionFor(reference contract.ProposalReference) (contract.PromotionRecord, bool) {
	for _, h := range t.s.history {
		if h.Record().Binding().Reference() == reference {
			return h.Record(), true
		}
	}
	return contract.PromotionRecord{}, false
}

func (t *tx) Receipt(ctx context.Context, id contract.OperationID) (governance.OperationReceipt, bool, error) {
	if err := t.live(ctx); err != nil {
		return governance.OperationReceipt{}, false, err
	}
	r, ok := t.d.byOp[id]
	return r, ok, nil
}

func (t *tx) ReceiptBySource(ctx context.Context, id contract.SourceCommandID) (governance.OperationReceipt, bool, error) {
	if err := t.live(ctx); err != nil {
		return governance.OperationReceipt{}, false, err
	}
	r, ok := t.d.bySource[id]
	return r, ok, nil
}

func (t *tx) ChangeProposalFor(ctx context.Context, carrier contract.ApprovalCarrierID) (contract.ProposalID, bool, error) {
	if err := t.live(ctx); err != nil {
		return "", false, err
	}
	var found []contract.ProposalID
	for id, p := range t.s.proposals {
		if p.proposal.Current().Carrier() != carrier {
			continue
		}
		isChange := !slices.ContainsFunc(p.proposal.Revisions(), func(r contract.ProposalRevision) bool {
			_, promoted := t.promotionFor(r.Binding().Reference())
			return promoted || r.Binding().ExpectedCanonical() == ""
		})
		if isChange {
			found = append(found, id)
		}
	}
	switch len(found) {
	case 0:
		return "", false, nil
	case 1:
		return found[0], true, nil
	}
	return "", false, fmt.Errorf("%w: proposals of %q: %d change proposals share one carrier", governance.ErrInvalidState, carrier, len(found))
}

func (t *tx) NextProposalID(ctx context.Context) (contract.ProposalID, error) {
	if err := t.live(ctx); err != nil {
		return "", err
	}
	return governance.NextNumberedProposalID(slices.Collect(maps.Keys(t.s.proposals))), nil
}

func (t *tx) AppendConsent(ctx context.Context, w governance.ConsentWrite) error {
	if err := t.live(ctx); err != nil {
		return err
	}
	command := w.Result.Command()
	p, ok := t.s.proposals[command.Reference().ProposalID]
	if !ok {
		return governance.ErrNotFound
	}
	if _, taken := t.d.byOp[w.OperationID]; taken || w.Result.Duplicate() != w.Alias || (!w.Alias && w.OperationID != command.OperationID()) {
		return governance.ErrOperationConflict
	}
	receipt := w.Receipt
	operation := governance.OperationReceipt{Kind: governance.OperationConsent, ProjectID: t.key.project, SuiteID: t.key.suite, Consent: &receipt}
	original, sourceKnown := t.d.bySource[command.SourceCommandID()]
	switch {
	case w.Alias && !sourceKnown:
		return fmt.Errorf("%w: alias for an unknown source command", governance.ErrInvalidState)
	case w.Alias && *original.Consent != receipt:
		return fmt.Errorf("%w: alias receipt differs from the original receipt", governance.ErrInvalidRequest)
	case w.Alias:
		p.aliases[w.OperationID] = command.SourceCommandID()
	case sourceKnown:
		return governance.ErrOperationConflict
	default:
		p.results = append(p.results, w.Result)
		t.d.bySource[command.SourceCommandID()] = operation
	}
	t.d.byOp[w.OperationID] = operation
	return t.written()
}

func (t *tx) RecordPromotion(ctx context.Context, w governance.PromotionWrite) error {
	if err := t.live(ctx); err != nil {
		return err
	}
	record := w.Receipt.Record
	if _, taken := t.d.byOp[record.OperationID()]; taken {
		return governance.ErrOperationConflict
	}
	_, versionTaken := t.s.history[w.Version.ID()]
	if _, promoted := t.promotionFor(record.Binding().Reference()); versionTaken || promoted {
		return governance.ErrVersionConflict
	}
	if p, known := t.s.proposals[record.Binding().Reference().ProposalID]; !known {
		return governance.ErrNotFound
	} else if _, err := p.proposal.Lookup(record.Binding().Reference(), record.Carrier()); err != nil {
		return fmt.Errorf("%w: promoted revision: %w", governance.ErrNotFound, err)
	}
	h, err := contract.NewHistoricalCanonical(w.Version, record)
	if err != nil {
		return fmt.Errorf("%w: %w", governance.ErrInvalidRequest, err)
	}
	receipt := w.Receipt
	t.s.history[w.Version.ID()], t.s.current = h, w.Version.ID()
	t.d.byOp[record.OperationID()] = governance.OperationReceipt{Kind: receipt.Identity.Kind, ProjectID: t.key.project, SuiteID: t.key.suite, Promotion: &receipt}
	return t.written()
}

func (t *tx) AppendProposalRevision(ctx context.Context, w governance.ProposalWrite) error {
	if err := t.live(ctx); err != nil {
		return err
	}
	revision := w.Revision
	reference := revision.Binding().Reference()
	if revision.IsZero() || reference.ProjectID != t.key.project || reference.SuiteID != t.key.suite {
		return fmt.Errorf("%w: a proposal revision belongs to the locked suite", governance.ErrInvalidRequest)
	}
	p, found := t.s.proposals[reference.ProposalID]
	if !found {
		proposal, err := contract.NewProposal(revision)
		if err != nil {
			return fmt.Errorf("%w: %w", governance.ErrInvalidRequest, err)
		}
		t.s.proposals[reference.ProposalID] = &proposalData{proposal: proposal, assessments: map[assessmentKey]contract.IntegrityAssessment{}, aliases: map[contract.OperationID]contract.SourceCommandID{}}
		return t.written()
	}
	next, err := p.proposal.Revise(revision)
	switch {
	case errors.Is(err, contract.ErrProposalContextMismatch), errors.Is(err, contract.ErrRevisionExists):
		return fmt.Errorf("%w: %w", governance.ErrProposalConflict, err)
	case err != nil:
		return fmt.Errorf("%w: %w", governance.ErrInvalidRequest, err)
	}
	p.proposal = next
	return t.written()
}

func (t *tx) RecordAssessment(ctx context.Context, w governance.AssessmentWrite) error {
	if err := t.live(ctx); err != nil {
		return err
	}
	if err := w.Validate(t.key.project, t.key.suite); err != nil {
		return err
	}
	p, ok := t.s.proposals[w.Reference.ProposalID]
	if !ok {
		return fmt.Errorf("%w: proposal %q", governance.ErrNotFound, w.Reference.ProposalID)
	}
	expected, ok := revisionBinding(p.proposal, w.Reference.RevisionID)
	if !ok {
		return fmt.Errorf("%w: revision %q of proposal %q", governance.ErrNotFound, w.Reference.RevisionID, w.Reference.ProposalID)
	}
	if cited, ok := revisionBinding(p.proposal, w.Evidence.Binding().Reference().RevisionID); !ok || !cited.Equal(w.Evidence.Binding()) {
		return fmt.Errorf("%w: evidence cites a binding that is not a stored revision of proposal %q", governance.ErrInvalidRequest, w.Reference.ProposalID)
	}
	key := assessmentKey{w.Reference, w.Source}
	if stored, exists := p.assessments[key]; exists {
		if w.Matches(stored) {
			return nil
		}
		return fmt.Errorf("%w: the assessment of %q at %q holds other evidence", governance.ErrConflict, w.Reference.RevisionID, w.Source)
	}
	assessment, err := contract.AssessIntegrity(w.Source, expected, &w.Evidence)
	if err != nil {
		return fmt.Errorf("%w: %w", governance.ErrInvalidRequest, err)
	}
	p.assessments[key] = assessment
	return t.written()
}

func revisionBinding(proposal contract.Proposal, id contract.ProposalRevisionID) (contract.ApprovalBinding, bool) {
	for _, revision := range proposal.Revisions() {
		if revision.Binding().Reference().RevisionID == id {
			return revision.Binding(), true
		}
	}
	return contract.ApprovalBinding{}, false
}

var _ QueueInspector = (*Memory)(nil)

func (m *Memory) QueuedJobKinds(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	kinds := make([]string, len(m.data.jobs))
	for i, job := range m.data.jobs {
		kinds[i] = job.Kind
	}
	slices.Sort(kinds)
	return kinds, nil
}

func (m *Memory) OutboxKeys(context.Context) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Sorted(maps.Keys(m.data.outbox)), nil
}

func (t *tx) Enqueue(ctx context.Context, job governance.Job) error {
	if err := t.live(ctx); err != nil {
		return err
	}
	if err := job.Validate(); err != nil {
		return err
	}
	t.d.jobs = append(t.d.jobs, job)
	return t.injected()
}

func (t *tx) Outbox(ctx context.Context, message governance.OutboxMessage) error {
	if err := t.live(ctx); err != nil {
		return err
	}
	if err := message.Validate(); err != nil {
		return err
	}
	if _, taken := t.d.outbox[message.Key]; taken {
		return fmt.Errorf("%w: outbox key %q", governance.ErrOperationConflict, message.Key)
	}
	t.d.outbox[message.Key] = message
	return t.injected()
}

// written bumps the Suite revision and applies an injected failure.
func (t *tx) written() error {
	t.s.revision++
	return t.injected()
}

// injected applies the failure injected by FailNext, once.
func (t *tx) injected() error {
	if err := t.fail; err != nil {
		t.fail = nil
		return err
	}
	return nil
}

func (t *tx) MergeOutcome(ctx context.Context, carrier contract.ApprovalCarrierID) (governance.MergeOutcome, bool, error) {
	if err := t.live(ctx); err != nil {
		return governance.MergeOutcome{}, false, err
	}
	outcome, found := t.s.merges[carrier]
	if found && outcome.Reference != nil {
		reference := *outcome.Reference // the caller must not reach the stored value
		outcome.Reference = &reference
	}
	return outcome, found, nil
}

// RecordMergeOutcome checks what the foreign keys of the schema check and, like it, leaves the Suite revision alone.
func (t *tx) RecordMergeOutcome(ctx context.Context, w governance.MergeWrite) error {
	if err := t.live(ctx); err != nil {
		return err
	}
	if err := w.Validate(); err != nil {
		return err
	}
	outcome := w.Outcome
	if _, known := t.s.history[outcome.Version]; !known {
		return fmt.Errorf("%w: version %q", governance.ErrNotFound, outcome.Version)
	}
	if _, known := t.s.history[outcome.Corrects]; outcome.Corrects != "" && !known {
		return fmt.Errorf("%w: corrected version %q", governance.ErrNotFound, outcome.Corrects)
	}
	if reference := outcome.Reference; reference != nil {
		stored := t.s.proposals[reference.ProposalID]
		if reference.ProjectID != t.key.project || reference.SuiteID != t.key.suite || stored == nil {
			return fmt.Errorf("%w: the proposal of %v is not in the locked suite", governance.ErrInvalidRequest, *reference)
		}
		if _, known := revisionBinding(stored.proposal, reference.RevisionID); !known {
			return fmt.Errorf("%w: the revision of %v is unknown", governance.ErrInvalidRequest, *reference)
		}
		copied := *reference
		outcome.Reference = &copied
	}
	if _, taken := t.s.merges[w.Carrier]; taken {
		return fmt.Errorf("%w: the merge of %q already has an outcome", governance.ErrConflict, w.Carrier)
	}
	if t.s.merges == nil {
		t.s.merges = map[contract.ApprovalCarrierID]governance.MergeOutcome{}
	}
	t.s.merges[w.Carrier] = outcome
	return t.injected()
}
