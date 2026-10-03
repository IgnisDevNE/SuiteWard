package contract

import (
	"fmt"
	"maps"
	"math"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

type checkpointData struct {
	Version     int              `json:"version"`
	Canonical   *canonicalData   `json:"canonical,omitempty"`
	Proposal    *proposalData    `json:"proposal,omitempty"`
	Policy      *policyData      `json:"policy,omitempty"`
	Consent     *consentData     `json:"consent,omitempty"`
	Scheduling  *scheduleData    `json:"scheduling,omitempty"`
	Assessment  *assessmentData  `json:"assessment,omitempty"`
	Integration *integrationData `json:"integration,omitempty"`
	History     *historyData     `json:"history,omitempty"`
	Decision    *decisionData    `json:"decision,omitempty"`
	Result      *resultData      `json:"result,omitempty"`
	Protected   *protectedData   `json:"protected,omitempty"`
	Command     *commandData     `json:"command,omitempty"`
}
type entryData struct {
	Path    string
	Content string
}
type manifestData struct {
	Entries []entryData
	Digest  string
}
type protectedData struct {
	Manifest manifestData
	Scope    string
	Covered  map[string]string
}
type bindingData struct {
	Reference         ProposalReference
	ExpectedCanonical SuiteVersionID
	Manifest, Scope   string
	PolicyRevision    PolicyRevisionID
	Covered           map[string]string
}
type principalData struct {
	ID   PrincipalID
	Kind PrincipalKind
}
type policyData struct {
	Project  ProjectID
	Revision PolicyRevisionID
	Owner    PrincipalID
}
type revisionData struct {
	Binding bindingData
	Origin  SourceRevision
	Carrier ApprovalCarrierID
}
type proposalData struct{ Revisions []revisionData }
type commandData struct {
	OperationID     OperationID
	SourceCommandID SourceCommandID
	Actor           principalData
	Reference       ProposalReference
	Carrier         ApprovalCarrierID
	Action          ConsentAction
	Order           CommandOrder
}
type resultData struct {
	Command   commandData
	Outcome   ConsentOutcome
	Reason    ConsentReason
	Duplicate bool
}
type consentStateData struct {
	Revision ProposalRevisionID
	Actor    principalData
	Binding  bindingData
	Active   bool
	Order    CommandOrder
}
type consentData struct {
	Project    ProjectID
	Suite      SuiteID
	Proposal   ProposalID
	Results    []resultData
	States     []consentStateData
	Operations map[OperationID]SourceCommandID
}
type priorityCommandData struct {
	OperationID     OperationID
	SourceCommandID SourceCommandID
	Actor           principalData
	Project         ProjectID
	Suite           SuiteID
	Proposal        ProposalID
	Carrier         ApprovalCarrierID
	Order           CommandOrder
}
type priorityResultData struct {
	Command   priorityCommandData
	Outcome   PriorityOutcome
	Reason    PriorityReason
	Duplicate bool
}
type scheduleEntryData struct {
	Proposal ProposalID
	Carrier  ApprovalCarrierID
	State    ScheduleEntryState
}
type scheduleData struct {
	Project    ProjectID
	Suite      SuiteID
	Generation ScheduleGeneration
	Entries    []scheduleEntryData
	Pending    *priorityCommandData
	Results    []priorityResultData
	Operations map[OperationID]SourceCommandID
	Order      CommandOrder
}
type suiteData struct {
	Project  ProjectID
	Suite    SuiteID
	Current  SuiteVersionID
	Revision StateRevision
}
type versionData struct {
	Project  ProjectID
	Suite    SuiteID
	ID       SuiteVersionID
	Manifest manifestData
}
type recordData struct {
	Operation  OperationID
	Version    SuiteVersionID
	Binding    bindingData
	Carrier    ApprovalCarrierID
	Source     SourceRevision
	Target     IntegrationTargetID
	RecordedAt time.Time
	Corrects   SuiteVersionID
}
type canonicalData struct {
	Suite     suiteData
	Version   *versionData
	Protected *protectedData
	Record    *recordData
}
type evidenceData struct {
	Emitter PrincipalID
	Source  SourceRevision
	Binding bindingData
	Outcome IntegrityOutcome
}
type assessmentData struct {
	Source    SourceRevision
	Binding   bindingData
	Evidence  *evidenceData
	Reason    IntegrityReason
	Assurance AssuranceLevel
}
type integrationData struct {
	Project ProjectID
	Target  IntegrationTargetID
	Source  SourceRevision
	Carrier ApprovalCarrierID
	Kind    IntegrationKind
}
type historyData struct {
	Version versionData
	Record  recordData
}
type effectData struct {
	ExpectedCanonical SuiteVersionID
	ExpectedState     StateRevision
	ExpectedSchedule  ScheduleGeneration
	Suite             suiteData
	Version           versionData
	Record            recordData
}
type decisionData struct {
	Outcome PromotionOutcome
	Reason  PromotionReason
	Effect  *effectData
}

func manifestDataOf(m artifact.Manifest) manifestData {
	data := manifestData{Digest: m.Digest().String(), Entries: make([]entryData, 0, len(m.Entries()))}
	for _, e := range m.Entries() {
		data.Entries = append(data.Entries, entryData{e.Path, e.Content.String()})
	}
	return data
}
func (d manifestData) restore() (artifact.Manifest, error) {
	entries := make([]artifact.Entry, 0, len(d.Entries))
	for _, e := range d.Entries {
		digest, err := artifact.ParseDigest(e.Content)
		if err != nil {
			return artifact.Manifest{}, err
		}
		entries = append(entries, artifact.Entry{Path: e.Path, Content: digest})
	}
	m, err := artifact.NewManifest(entries)
	if err != nil {
		return m, err
	}
	if m.Digest().String() != d.Digest {
		return artifact.Manifest{}, ErrInvalidCheckpoint
	}
	return m, nil
}
func protectedDataOf(p ProtectedContract) *protectedData {
	return &protectedData{manifestDataOf(p.Manifest()), p.ScopeDigest().String(), p.CoveredInputs()}
}
func (d *protectedData) restore() (ProtectedContract, error) {
	if d == nil {
		return ProtectedContract{}, nil
	}
	m, e := d.Manifest.restore()
	if e != nil {
		return ProtectedContract{}, e
	}
	scope, e := artifact.ParseDigest(d.Scope)
	if e != nil {
		return ProtectedContract{}, e
	}
	return NewProtectedContract(m, scope, d.Covered)
}
func bindingDataOf(b ApprovalBinding) bindingData {
	return bindingData{b.Reference(), b.ExpectedCanonical(), b.ManifestDigest().String(), b.ScopeDigest().String(), b.PolicyRevisionID(), b.CoveredInputs()}
}
func (d bindingData) restore() (ApprovalBinding, error) {
	m, e := artifact.ParseDigest(d.Manifest)
	if e != nil {
		return ApprovalBinding{}, e
	}
	scope, e := artifact.ParseDigest(d.Scope)
	if e != nil {
		return ApprovalBinding{}, e
	}
	return NewApprovalBinding(BindingInput{Reference: d.Reference, ExpectedCanonical: d.ExpectedCanonical, Manifest: m, Scope: scope, PolicyRevision: d.PolicyRevision, CoveredInputs: d.Covered})
}
func principalDataOf(p Principal) principalData     { return principalData{p.ID(), p.Kind()} }
func (d principalData) restore() (Principal, error) { return NewPrincipal(d.ID, d.Kind) }
func policyDataOf(p Policy) *policyData {
	return &policyData{p.ProjectID(), p.RevisionID(), p.OwnerID()}
}
func (d *policyData) restore() (Policy, error) {
	if d == nil {
		return Policy{}, nil
	}
	owner, e := NewPrincipal(d.Owner, Human)
	if e != nil {
		return Policy{}, e
	}
	return NewPolicy(d.Project, d.Revision, owner)
}
func proposalDataOf(p Proposal) *proposalData {
	d := &proposalData{Revisions: make([]revisionData, 0, len(p.revisions))}
	for _, r := range p.revisions {
		d.Revisions = append(d.Revisions, revisionData{bindingDataOf(r.Binding()), r.Origin(), r.Carrier()})
	}
	return d
}
func (d *proposalData) restore() (Proposal, error) {
	if d == nil {
		return Proposal{}, nil
	}
	if len(d.Revisions) == 0 {
		return Proposal{}, ErrInvalidProposal
	}
	var p Proposal
	for i, r := range d.Revisions {
		b, e := r.Binding.restore()
		if e != nil {
			return Proposal{}, e
		}
		revision, e := NewProposalRevision(b, r.Origin, r.Carrier)
		if e != nil {
			return Proposal{}, e
		}
		if i == 0 {
			p, e = NewProposal(revision)
		} else {
			p, e = p.Revise(revision)
		}
		if e != nil {
			return Proposal{}, e
		}
	}
	return p, nil
}
func commandDataOf(c Command) *commandData {
	return &commandData{c.OperationID(), c.SourceCommandID(), principalDataOf(c.Actor()), c.Reference(), c.Carrier(), c.Action(), c.Order()}
}
func (d *commandData) restore() (Command, error) {
	if d == nil {
		return Command{}, nil
	}
	actor, e := d.Actor.restore()
	if e != nil {
		return Command{}, e
	}
	return NewCommand(CommandInput{OperationID: d.OperationID, SourceCommandID: d.SourceCommandID, Actor: actor, Reference: d.Reference, Carrier: d.Carrier, Action: d.Action, Order: d.Order})
}
func resultDataOf(r CommandResult) *resultData {
	return &resultData{*commandDataOf(r.Command()), r.Outcome(), r.Reason(), r.Duplicate()}
}
func (d *resultData) restore() (CommandResult, error) {
	if d == nil {
		return CommandResult{}, nil
	}
	c, e := d.Command.restore()
	if e != nil {
		return CommandResult{}, e
	}
	if d.Outcome < ConsentApproved || d.Outcome > ConsentRejected || d.Reason > ConsentReasonObsoleteCommand {
		return CommandResult{}, ErrInvalidCheckpoint
	}
	if d.Outcome == ConsentRejected {
		if d.Reason == ConsentReasonNone {
			return CommandResult{}, ErrInvalidCheckpoint
		}
	} else if d.Reason != ConsentReasonNone || (d.Outcome == ConsentApproved) != (c.Action() == ApproveConsent) {
		return CommandResult{}, ErrInvalidCheckpoint
	}
	return CommandResult{command: c, outcome: d.Outcome, reason: d.Reason, duplicate: d.Duplicate}, nil
}
func suiteDataOf(s Suite) suiteData {
	current, _ := s.CurrentVersionID()
	return suiteData{s.ProjectID(), s.ID(), current, s.Revision()}
}
func (d suiteData) restore() (Suite, error) {
	return NewSuite(d.Project, d.Suite, d.Current, d.Revision)
}
func versionDataOf(v SuiteVersion) *versionData {
	return &versionData{v.ProjectID(), v.SuiteID(), v.ID(), manifestDataOf(v.Manifest())}
}
func (d *versionData) restore() (SuiteVersion, error) {
	if d == nil {
		return SuiteVersion{}, nil
	}
	m, e := d.Manifest.restore()
	if e != nil {
		return SuiteVersion{}, e
	}
	return NewSuiteVersion(d.Project, d.Suite, d.ID, m)
}
func recordDataOf(r PromotionRecord) *recordData {
	return &recordData{r.OperationID(), r.VersionID(), bindingDataOf(r.Binding()), r.Carrier(), r.Source(), r.Target(), r.RecordedAt(), r.CorrectsVersionID()}
}
func (d *recordData) restore() (PromotionRecord, error) {
	if d == nil {
		return PromotionRecord{}, nil
	}
	b, e := d.Binding.restore()
	if e != nil {
		return PromotionRecord{}, e
	}
	return NewPromotionRecord(PromotionRecordInput{OperationID: d.Operation, VersionID: d.Version, Binding: b, Carrier: d.Carrier, Source: d.Source, Target: d.Target, RecordedAt: d.RecordedAt, CorrectsVersionID: d.Corrects})
}
func canonicalDataOf(c CanonicalSnapshot) *canonicalData {
	d := &canonicalData{Suite: suiteDataOf(c.Suite())}
	if _, present := c.Suite().CurrentVersionID(); present {
		d.Version = versionDataOf(c.Version())
		d.Protected = protectedDataOf(c.Contract())
		d.Record = recordDataOf(c.Record())
	}
	return d
}
func (d *canonicalData) restore() (CanonicalSnapshot, error) {
	if d == nil {
		return CanonicalSnapshot{}, nil
	}
	s, e := d.Suite.restore()
	if e != nil {
		return CanonicalSnapshot{}, e
	}
	v, e := d.Version.restore()
	if e != nil {
		return CanonicalSnapshot{}, e
	}
	p, e := d.Protected.restore()
	if e != nil {
		return CanonicalSnapshot{}, e
	}
	r, e := d.Record.restore()
	if e != nil {
		return CanonicalSnapshot{}, e
	}
	return NewCanonicalSnapshot(s, v, p, r)
}
func assessmentDataOf(a IntegrityAssessment) *assessmentData {
	d := &assessmentData{Source: a.Source(), Binding: bindingDataOf(a.Binding()), Reason: a.Reason(), Assurance: a.Assurance()}
	if e, present := a.Evidence(); present {
		d.Evidence = &evidenceData{e.EmitterID(), e.Source(), bindingDataOf(e.Binding()), e.Outcome()}
	}
	return d
}
func (d *assessmentData) restore() (IntegrityAssessment, error) {
	if d == nil {
		return IntegrityAssessment{}, nil
	}
	b, e := d.Binding.restore()
	if e != nil {
		return IntegrityAssessment{}, e
	}
	var evidence *IntegrityEvidence
	if d.Evidence != nil {
		eb, e := d.Evidence.Binding.restore()
		if e != nil {
			return IntegrityAssessment{}, e
		}
		ev, e := NewIntegrityEvidence(d.Evidence.Emitter, d.Evidence.Source, eb, d.Evidence.Outcome)
		if e != nil {
			return IntegrityAssessment{}, e
		}
		evidence = &ev
	}
	a, e := AssessIntegrity(d.Source, b, evidence)
	if e != nil {
		return IntegrityAssessment{}, e
	}
	if a.Reason() != d.Reason || a.Assurance() != d.Assurance {
		return IntegrityAssessment{}, ErrInvalidCheckpoint
	}
	return a, nil
}
func integrationDataOf(i Integration) *integrationData {
	return &integrationData{i.ProjectID(), i.Target(), i.Source(), i.Carrier(), i.Kind()}
}
func (d *integrationData) restore() (Integration, error) {
	if d == nil {
		return Integration{}, nil
	}
	return NewIntegration(d.Project, d.Target, d.Source, d.Carrier, d.Kind)
}
func (d *historyData) restore() (HistoricalCanonical, error) {
	if d == nil {
		return HistoricalCanonical{}, nil
	}
	v, e := d.Version.restore()
	if e != nil {
		return HistoricalCanonical{}, e
	}
	r, e := d.Record.restore()
	if e != nil {
		return HistoricalCanonical{}, e
	}
	return NewHistoricalCanonical(v, r)
}
func decisionDataOf(d PromotionDecision) *decisionData {
	data := &decisionData{Outcome: d.Outcome(), Reason: d.Reason()}
	if effect, present := d.Effect(); present {
		data.Effect = &effectData{effect.ExpectedCanonicalID(), effect.ExpectedStateRevision(), effect.ExpectedSchedulingGeneration(), suiteDataOf(effect.Suite()), *versionDataOf(effect.Version()), *recordDataOf(effect.Promotion())}
	}
	return data
}
func (d *decisionData) restore() (PromotionDecision, error) {
	if d == nil {
		return PromotionDecision{}, nil
	}
	if d.Outcome < PromotionBlocked || d.Outcome > PromotionProposed {
		return PromotionDecision{}, ErrInvalidCheckpoint
	}
	if d.Outcome != PromotionProposed {
		if d.Effect != nil || d.Reason > PromotionReasonCorrectionContextReused || (d.Outcome == PromotionBlocked) == (d.Reason == PromotionReasonNone) {
			return PromotionDecision{}, ErrInvalidCheckpoint
		}
		return PromotionDecision{outcome: d.Outcome, reason: d.Reason}, nil
	}
	if d.Effect == nil || d.Reason != PromotionReasonNone {
		return PromotionDecision{}, ErrInvalidCheckpoint
	}
	e := d.Effect
	s, err := e.Suite.restore()
	if err != nil {
		return PromotionDecision{}, err
	}
	v, err := e.Version.restore()
	if err != nil {
		return PromotionDecision{}, err
	}
	r, err := e.Record.restore()
	if err != nil {
		return PromotionDecision{}, err
	}
	current, _ := s.CurrentVersionID()
	ref := r.Binding().Reference()
	if e.ExpectedState == StateRevision(math.MaxUint64) || s.Revision() != e.ExpectedState+1 || e.ExpectedSchedule == 0 || e.ExpectedCanonical != r.Binding().ExpectedCanonical() || current != v.ID() || v.ID() != r.VersionID() || s.ProjectID() != v.ProjectID() || s.ID() != v.SuiteID() || ref.ProjectID != s.ProjectID() || ref.SuiteID != s.ID() || r.Binding().ManifestDigest() != v.Manifest().Digest() {
		return PromotionDecision{}, ErrInvalidCheckpoint
	}
	return PromotionDecision{outcome: d.Outcome, effect: PromotionEffect{expectedCanonical: e.ExpectedCanonical, expectedState: e.ExpectedState, expectedSchedule: e.ExpectedSchedule, suite: s, version: v, promotion: r}}, nil
}

func (d checkpointData) restore() (StateCheckpoint, error) {
	var s StateCheckpoint
	var e error
	if s.Canonical, e = d.Canonical.restore(); e != nil {
		return s, e
	}
	if s.Proposal, e = d.Proposal.restore(); e != nil {
		return s, e
	}
	if s.Policy, e = d.Policy.restore(); e != nil {
		return s, e
	}
	if s.Consent, e = d.Consent.restore(); e != nil {
		return s, e
	}
	if s.Scheduling, e = d.Scheduling.restore(); e != nil {
		return s, e
	}
	if s.Assessment, e = d.Assessment.restore(); e != nil {
		return s, e
	}
	if s.Integration, e = d.Integration.restore(); e != nil {
		return s, e
	}
	if s.History, e = d.History.restore(); e != nil {
		return s, e
	}
	if s.PromotionDecision, e = d.Decision.restore(); e != nil {
		return s, e
	}
	if s.CommandResult, e = d.Result.restore(); e != nil {
		return s, e
	}
	if s.Protected, e = d.Protected.restore(); e != nil {
		return s, e
	}
	if s.Command, e = d.Command.restore(); e != nil {
		return s, e
	}
	if !s.Proposal.IsZero() && s.Consent.project != "" && !s.Consent.contains(s.Proposal.Current().Binding().Reference()) {
		return StateCheckpoint{}, checkpointProblem("proposal consent scope")
	}
	return s, nil
}

func validCheckpointID(value string) bool { return strings.TrimSpace(value) != "" }
func checkpointProblem(kind string) error {
	return fmt.Errorf("%w: inconsistent %s", ErrInvalidCheckpoint, kind)
}

// Maps are cloned on both sides of the persistence boundary.
func cloneCheckpointOperations(operations map[OperationID]SourceCommandID) map[OperationID]SourceCommandID {
	return maps.Clone(operations)
}
