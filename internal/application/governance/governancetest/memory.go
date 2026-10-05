// Package governancetest provides an in-memory governance.UnitOfWork for
// behavior tests of the application layer. It mirrors the store semantics of
// the persistence contract in memory and does not verify artifact bytes.
package governancetest

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Memory implements governance.UnitOfWork and governance.Seeder. One mutex
// serializes every unit of work; each works on a copy that replaces the
// stored state only when the unit of work succeeds.
type Memory struct {
	mu   sync.Mutex
	data data
	fail error
}

var (
	_ governance.UnitOfWork = (*Memory)(nil)
	_ governance.Seeder     = (*Memory)(nil)
)

func NewMemory() *Memory {
	return &Memory{data: data{suites: map[suiteKey]*suiteData{}, byOp: map[contract.OperationID]governance.OperationReceipt{}, bySource: map[contract.SourceCommandID]governance.OperationReceipt{}}}
}

// FailNext makes the next write fail with err after it has been applied to
// the working copy, so a rollback discards a real effect.
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
}

type suiteData struct {
	revision  contract.StateRevision
	current   contract.SuiteVersionID
	policy    contract.Policy
	target    contract.IntegrationTargetID
	schedule  contract.Schedule
	history   map[contract.SuiteVersionID]contract.HistoricalCanonical
	proposals map[contract.ProposalID]*proposalData
}

type proposalData struct {
	proposal    contract.Proposal
	assessments map[assessmentKey]contract.IntegrityAssessment
	results     []contract.CommandResult
	aliases     map[contract.OperationID]contract.SourceCommandID
}

func (d data) clone() data {
	c := data{suites: map[suiteKey]*suiteData{}, byOp: maps.Clone(d.byOp), bySource: maps.Clone(d.bySource)}
	for key, s := range d.suites {
		next := *s
		next.history = maps.Clone(s.history)
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
	if err := fn(ctx, &tx{m: m, d: &work, key: key, s: work.suites[key]}); err != nil {
		return err
	}
	m.data = work
	return nil
}

func (m *Memory) Seed(_ context.Context, seed governance.Seed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := seed.Suite
	suite := state.Canonical.Suite()
	key := suiteKey{suite.ProjectID(), suite.ID()}
	current, _ := suite.CurrentVersionID()
	if key.suite == "" || state.Policy.ProjectID() != key.project || state.Schedule.IsZero() || state.Target == "" {
		return fmt.Errorf("%w: incomplete suite state", governance.ErrInvalidRequest)
	}
	if _, exists := m.data.suites[key]; exists {
		return fmt.Errorf("%w: suite already seeded", governance.ErrInvalidRequest)
	}
	s := &suiteData{revision: suite.Revision(), current: current, policy: state.Policy, target: state.Target, schedule: state.Schedule,
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
	return nil
}

type tx struct {
	m   *Memory
	d   *data
	key suiteKey
	s   *suiteData
}

func (t *tx) Suite(context.Context) (governance.SuiteState, error) {
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
	return governance.SuiteState{Canonical: canonical, Policy: t.s.policy, Target: t.s.target, Schedule: t.s.schedule}, nil
}

func (t *tx) Proposal(_ context.Context, id contract.ProposalID) (contract.Proposal, contract.Consent, error) {
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

func (t *tx) Assessment(_ context.Context, reference contract.ProposalReference, source contract.SourceRevision) (contract.IntegrityAssessment, bool, error) {
	if p, ok := t.s.proposals[reference.ProposalID]; ok {
		a, found := p.assessments[assessmentKey{reference, source}]
		return a, found, nil
	}
	return contract.IntegrityAssessment{}, false, nil
}

func (t *tx) Version(_ context.Context, id contract.SuiteVersionID) (contract.HistoricalCanonical, bool, error) {
	h, ok := t.s.history[id]
	return h, ok, nil
}

func (t *tx) PromotionFor(_ context.Context, reference contract.ProposalReference) (contract.PromotionRecord, bool, error) {
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

func (t *tx) Receipt(_ context.Context, id contract.OperationID) (governance.OperationReceipt, bool, error) {
	r, ok := t.d.byOp[id]
	return r, ok, nil
}

func (t *tx) ReceiptBySource(_ context.Context, id contract.SourceCommandID) (governance.OperationReceipt, bool, error) {
	r, ok := t.d.bySource[id]
	return r, ok, nil
}

func (t *tx) AppendConsent(_ context.Context, w governance.ConsentWrite) error {
	command := w.Result.Command()
	p, ok := t.s.proposals[command.Reference().ProposalID]
	if !ok {
		return governance.ErrNotFound
	}
	if _, taken := t.d.byOp[w.OperationID]; taken || w.OperationID != command.OperationID() || w.Result.Duplicate() != w.Alias {
		return governance.ErrOperationConflict
	}
	receipt := w.Receipt
	operation := governance.OperationReceipt{Kind: governance.OperationConsent, ProjectID: t.key.project, SuiteID: t.key.suite, Consent: &receipt}
	_, sourceKnown := t.d.bySource[command.SourceCommandID()]
	switch {
	case w.Alias && !sourceKnown:
		return fmt.Errorf("%w: alias for an unknown source command", governance.ErrInvalidState)
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

func (t *tx) RecordPromotion(_ context.Context, w governance.PromotionWrite) error {
	record := w.Receipt.Record
	if _, taken := t.d.byOp[record.OperationID()]; taken {
		return governance.ErrOperationConflict
	}
	_, versionTaken := t.s.history[w.Version.ID()]
	if _, promoted := t.promotionFor(record.Binding().Reference()); versionTaken || promoted {
		return governance.ErrVersionConflict
	}
	h, err := contract.NewHistoricalCanonical(w.Version, record)
	if err != nil {
		return fmt.Errorf("%w: %w", governance.ErrInvalidRequest, err)
	}
	receipt := w.Receipt
	t.s.history[w.Version.ID()], t.s.current, t.s.schedule = h, w.Version.ID(), w.Schedule
	t.d.byOp[record.OperationID()] = governance.OperationReceipt{Kind: receipt.Identity.Kind, ProjectID: t.key.project, SuiteID: t.key.suite, Promotion: &receipt}
	return t.written()
}

// written bumps the Suite revision and applies an injected failure.
func (t *tx) written() error {
	t.s.revision++
	if err := t.m.fail; err != nil {
		t.m.fail = nil
		return err
	}
	return nil
}
