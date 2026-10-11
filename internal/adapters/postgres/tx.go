package postgres

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// tx is the governance.Tx of one unit of work. It is valid only while the
// callback of Store.Do runs.
type tx struct {
	q         *dbgen.Queries
	raw       pgx.Tx
	project   contract.ProjectID
	suite     contract.SuiteID
	artifacts ArtifactVerifier
	jobs      JobInserter
}

var _ governance.Tx = (*tx)(nil)

func optionalText(value string) pgtype.Text { return pgtype.Text{String: value, Valid: value != ""} }

// lock reads the Suite row and holds its lock until the transaction ends.
func (t *tx) lock(ctx context.Context) (dbgen.Suite, error) {
	row, err := t.q.LockSuite(ctx, dbgen.LockSuiteParams{ProjectID: string(t.project), SuiteID: string(t.suite)})
	if errors.Is(err, pgx.ErrNoRows) {
		return dbgen.Suite{}, fmt.Errorf("%w: suite %q of project %q", governance.ErrNotFound, t.suite, t.project)
	}
	if err != nil {
		return dbgen.Suite{}, fmt.Errorf("lock suite: %w", err)
	}
	return row, nil
}

func (t *tx) inScope(reference contract.ProposalReference) bool {
	return reference.ProjectID == t.project && reference.SuiteID == t.suite
}

// Reads reconstitute domain values through their public constructors.

func (t *tx) Suite(ctx context.Context) (governance.SuiteState, error) {
	row, err := t.lock(ctx)
	if err != nil {
		return governance.SuiteState{}, err
	}
	policyRow, err := t.q.GetPolicy(ctx, dbgen.GetPolicyParams{ProjectID: row.ProjectID, RevisionID: row.PolicyRevisionID})
	if errors.Is(err, pgx.ErrNoRows) {
		return governance.SuiteState{}, invalidState("governing policy", err)
	}
	if err != nil {
		return governance.SuiteState{}, fmt.Errorf("load governing policy: %w", err)
	}
	kind, err := principalKindFromCode(policyRow.OwnerKind)
	if err != nil {
		return governance.SuiteState{}, invalidState("policy owner", err)
	}
	owner, err := contract.NewPrincipal(contract.PrincipalID(policyRow.OwnerID), kind)
	if err != nil {
		return governance.SuiteState{}, invalidState("policy owner", err)
	}
	policy, err := contract.NewPolicy(contract.ProjectID(policyRow.ProjectID), contract.PolicyRevisionID(policyRow.RevisionID), owner)
	if err != nil {
		return governance.SuiteState{}, invalidState("governing policy", err)
	}
	canonical, err := t.canonical(ctx, row)
	if err != nil {
		return governance.SuiteState{}, err
	}
	return governance.SuiteState{Canonical: canonical, Policy: policy, Target: contract.IntegrationTargetID(row.TargetID)}, nil
}

func (t *tx) canonical(ctx context.Context, row dbgen.Suite) (contract.CanonicalSnapshot, error) {
	suite, err := contract.NewSuite(t.project, t.suite, contract.SuiteVersionID(row.CurrentVersionID.String), contract.StateRevision(row.Revision))
	if err != nil {
		return contract.CanonicalSnapshot{}, invalidState("suite", err)
	}
	if !row.CurrentVersionID.Valid {
		snapshot, err := contract.NewCanonicalSnapshot(suite, contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{})
		if err != nil {
			return contract.CanonicalSnapshot{}, invalidState("canonical snapshot", err)
		}
		return snapshot, nil
	}
	current, found, err := t.Version(ctx, contract.SuiteVersionID(row.CurrentVersionID.String))
	if err != nil {
		return contract.CanonicalSnapshot{}, err
	}
	if !found {
		return contract.CanonicalSnapshot{}, invalidState("current version", errors.New("the suite points at a version that is not stored"))
	}
	binding := current.Record().Binding()
	protected, err := contract.NewProtectedContract(current.Version().Manifest(), binding.ScopeDigest(), binding.CoveredInputs())
	if err != nil {
		return contract.CanonicalSnapshot{}, invalidState("protected contract", err)
	}
	snapshot, err := contract.NewCanonicalSnapshot(suite, current.Version(), protected, current.Record())
	if err != nil {
		return contract.CanonicalSnapshot{}, invalidState("canonical snapshot", err)
	}
	return snapshot, nil
}

func (t *tx) Proposal(ctx context.Context, id contract.ProposalID) (contract.Proposal, contract.Consent, error) {
	scope := dbgen.GetProposalParams{ProjectID: string(t.project), SuiteID: string(t.suite), ProposalID: string(id)}
	row, err := t.q.GetProposal(ctx, scope)
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.Proposal{}, contract.Consent{}, fmt.Errorf("%w: proposal %q", governance.ErrNotFound, id)
	}
	if err != nil {
		return contract.Proposal{}, contract.Consent{}, fmt.Errorf("load proposal: %w", err)
	}
	revisions, err := t.q.ListProposalRevisions(ctx, dbgen.ListProposalRevisionsParams(scope))
	if err != nil {
		return contract.Proposal{}, contract.Consent{}, fmt.Errorf("load proposal revisions: %w", err)
	}
	var proposal contract.Proposal
	for _, revision := range revisions {
		binding, err := bindingFromRevision(revision)
		if err != nil {
			return contract.Proposal{}, contract.Consent{}, invalidState("proposal revision "+revision.RevisionID, err)
		}
		sealed, err := contract.NewProposalRevision(binding, contract.SourceRevision(revision.Origin), contract.ApprovalCarrierID(revision.CarrierID))
		if err != nil {
			return contract.Proposal{}, contract.Consent{}, invalidState("proposal revision "+revision.RevisionID, err)
		}
		if proposal.IsZero() {
			proposal, err = contract.NewProposal(sealed)
		} else {
			proposal, err = proposal.Revise(sealed)
		}
		if err != nil {
			return contract.Proposal{}, contract.Consent{}, invalidState("proposal revision "+revision.RevisionID, err)
		}
	}
	if proposal.IsZero() || string(proposal.Current().Carrier()) != row.CarrierID {
		return contract.Proposal{}, contract.Consent{}, invalidState("proposal "+row.ProposalID, errors.New("revisions are missing or carried elsewhere"))
	}
	consent, err := t.consent(ctx, proposal, dbgen.ListConsentResultsParams(scope))
	if err != nil {
		return contract.Proposal{}, contract.Consent{}, err
	}
	return proposal, consent, nil
}

func (t *tx) consent(ctx context.Context, proposal contract.Proposal, scope dbgen.ListConsentResultsParams) (contract.Consent, error) {
	rows, err := t.q.ListConsentResults(ctx, scope)
	if err != nil {
		return contract.Consent{}, fmt.Errorf("load consent results: %w", err)
	}
	results := make([]contract.CommandResult, 0, len(rows))
	for _, row := range rows {
		actor, err := decodePrincipal(principalDTO{ID: row.ActorID, Kind: row.ActorKind})
		if err != nil {
			return contract.Consent{}, invalidState("consent result "+row.SourceCommandID, err)
		}
		action, err := consentActionFromCode(row.Action)
		if err != nil {
			return contract.Consent{}, invalidState("consent result "+row.SourceCommandID, err)
		}
		command, err := contract.NewCommand(contract.CommandInput{
			OperationID: contract.OperationID(row.OperationID), SourceCommandID: contract.SourceCommandID(row.SourceCommandID), Actor: actor,
			Reference: contract.ProposalReference{ProjectID: contract.ProjectID(row.ProjectID), SuiteID: contract.SuiteID(row.SuiteID), ProposalID: contract.ProposalID(row.ProposalID), RevisionID: contract.ProposalRevisionID(row.RevisionID)},
			Carrier:   contract.ApprovalCarrierID(row.CarrierID), Action: action, Order: contract.CommandOrder(row.CommandOrder),
		})
		if err != nil {
			return contract.Consent{}, invalidState("consent result "+row.SourceCommandID, err)
		}
		result, err := reconstituteResult(command, row.Outcome, row.Reason)
		if err != nil {
			return contract.Consent{}, invalidState("consent result "+row.SourceCommandID, err)
		}
		results = append(results, result)
	}
	aliasRows, err := t.q.ListConsentAliases(ctx, dbgen.ListConsentAliasesParams(scope))
	if err != nil {
		return contract.Consent{}, fmt.Errorf("load consent aliases: %w", err)
	}
	aliases := make(map[contract.OperationID]contract.SourceCommandID, len(aliasRows))
	for _, alias := range aliasRows {
		aliases[contract.OperationID(alias.OperationID)] = contract.SourceCommandID(alias.SourceCommandID)
	}
	consent, err := contract.ReconstituteConsent(proposal, results, aliases)
	if err != nil {
		return contract.Consent{}, invalidState("consent history", err)
	}
	return consent, nil
}

func (t *tx) revisionBinding(ctx context.Context, proposal contract.ProposalID, revision contract.ProposalRevisionID) (contract.ApprovalBinding, error) {
	row, err := t.q.GetProposalRevision(ctx, dbgen.GetProposalRevisionParams{ProjectID: string(t.project), SuiteID: string(t.suite), ProposalID: string(proposal), RevisionID: string(revision)})
	if err != nil {
		return contract.ApprovalBinding{}, fmt.Errorf("load proposal revision %q: %w", revision, err)
	}
	binding, err := bindingFromRevision(row)
	if err != nil {
		return contract.ApprovalBinding{}, invalidState("proposal revision "+row.RevisionID, err)
	}
	return binding, nil
}

func (t *tx) Assessment(ctx context.Context, reference contract.ProposalReference, source contract.SourceRevision) (contract.IntegrityAssessment, bool, error) {
	if !t.inScope(reference) {
		return contract.IntegrityAssessment{}, false, nil
	}
	row, err := t.q.GetAssessment(ctx, dbgen.GetAssessmentParams{ProjectID: string(t.project), SuiteID: string(t.suite), ProposalID: string(reference.ProposalID), RevisionID: string(reference.RevisionID), Source: string(source)})
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.IntegrityAssessment{}, false, nil
	}
	if err != nil {
		return contract.IntegrityAssessment{}, false, fmt.Errorf("load assessment: %w", err)
	}
	expected, err := t.revisionBinding(ctx, reference.ProposalID, reference.RevisionID)
	if err != nil {
		return contract.IntegrityAssessment{}, false, err
	}
	var evidence *contract.IntegrityEvidence
	if row.Outcome.Valid {
		// The evidence binding is rebuilt from the revision it cites, which belongs to this proposal.
		observed, err := t.revisionBinding(ctx, reference.ProposalID, contract.ProposalRevisionID(row.EvidenceRevisionID.String))
		if err != nil {
			return contract.IntegrityAssessment{}, false, err
		}
		outcome, err := integrityOutcomeFromCode(row.Outcome.String)
		if err != nil {
			return contract.IntegrityAssessment{}, false, invalidState("assessment", err)
		}
		sealed, err := contract.NewIntegrityEvidence(contract.PrincipalID(row.EvidenceEmitter.String), contract.SourceRevision(row.EvidenceSource.String), observed, outcome)
		if err != nil {
			return contract.IntegrityAssessment{}, false, invalidState("assessment evidence", err)
		}
		evidence = &sealed
	}
	assessment, err := contract.AssessIntegrity(source, expected, evidence)
	if err != nil {
		return contract.IntegrityAssessment{}, false, invalidState("assessment", err)
	}
	return assessment, true, nil
}

func (t *tx) Version(ctx context.Context, id contract.SuiteVersionID) (contract.HistoricalCanonical, bool, error) {
	row, err := t.q.GetVersion(ctx, dbgen.GetVersionParams{ProjectID: string(t.project), SuiteID: string(t.suite), VersionID: string(id)})
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.HistoricalCanonical{}, false, nil
	}
	if err != nil {
		return contract.HistoricalCanonical{}, false, fmt.Errorf("load version: %w", err)
	}
	manifest, err := decodeManifest(row.SuiteVersion.Manifest, row.SuiteVersion.ManifestDigest)
	if err != nil {
		return contract.HistoricalCanonical{}, false, invalidState("manifest of version "+row.SuiteVersion.VersionID, err)
	}
	version, err := contract.NewSuiteVersion(t.project, t.suite, id, manifest)
	if err != nil {
		return contract.HistoricalCanonical{}, false, invalidState("version "+row.SuiteVersion.VersionID, err)
	}
	record, err := recordFromRows(row.Promotion, row.ProposalRevision)
	if err != nil {
		return contract.HistoricalCanonical{}, false, err
	}
	historical, err := contract.NewHistoricalCanonical(version, record)
	if err != nil {
		return contract.HistoricalCanonical{}, false, invalidState("version "+row.SuiteVersion.VersionID, err)
	}
	return historical, true, nil
}

func (t *tx) PromotionFor(ctx context.Context, reference contract.ProposalReference) (contract.PromotionRecord, bool, error) {
	if !t.inScope(reference) {
		return contract.PromotionRecord{}, false, nil
	}
	row, err := t.q.GetPromotionByReference(ctx, dbgen.GetPromotionByReferenceParams{ProjectID: string(t.project), SuiteID: string(t.suite), ProposalID: string(reference.ProposalID), RevisionID: string(reference.RevisionID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.PromotionRecord{}, false, nil
	}
	if err != nil {
		return contract.PromotionRecord{}, false, fmt.Errorf("load promotion: %w", err)
	}
	record, err := recordFromRows(row.Promotion, row.ProposalRevision)
	if err != nil {
		return contract.PromotionRecord{}, false, err
	}
	return record, true, nil
}

// recordFromRows rebuilds a promotion record; the revision row holds its binding.
func recordFromRows(promotion dbgen.Promotion, revision dbgen.ProposalRevision) (contract.PromotionRecord, error) {
	binding, err := bindingFromRevision(revision)
	if err != nil {
		return contract.PromotionRecord{}, invalidState("binding of version "+promotion.VersionID, err)
	}
	record, err := contract.NewPromotionRecord(contract.PromotionRecordInput{
		OperationID: contract.OperationID(promotion.OperationID), VersionID: contract.SuiteVersionID(promotion.VersionID), Binding: binding,
		Carrier: contract.ApprovalCarrierID(promotion.CarrierID), Source: contract.SourceRevision(promotion.SourceRevision), Target: contract.IntegrationTargetID(promotion.TargetID),
		RecordedAt: promotion.RecordedAt.Time.UTC(), CorrectsVersionID: contract.SuiteVersionID(promotion.CorrectsVersionID.String),
	})
	if err != nil {
		return contract.PromotionRecord{}, invalidState("promotion of version "+promotion.VersionID, err)
	}
	return record, nil
}

func (t *tx) Receipt(ctx context.Context, id contract.OperationID) (governance.OperationReceipt, bool, error) {
	row, err := t.q.GetOperation(ctx, string(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return governance.OperationReceipt{}, false, nil
	}
	if err != nil {
		return governance.OperationReceipt{}, false, fmt.Errorf("load operation: %w", err)
	}
	receipt, err := decodeReceipt(row)
	return receipt, err == nil, err
}

func (t *tx) ReceiptBySource(ctx context.Context, id contract.SourceCommandID) (governance.OperationReceipt, bool, error) {
	row, err := t.q.GetOperationBySource(ctx, string(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return governance.OperationReceipt{}, false, nil
	}
	if err != nil {
		return governance.OperationReceipt{}, false, fmt.Errorf("load operation of source command: %w", err)
	}
	receipt, err := decodeReceipt(row)
	return receipt, err == nil, err
}

// Writes store explicit facts. Each one advances the Suite revision by exactly
// one; the use case has already made every decision.

func (t *tx) AppendConsent(ctx context.Context, write governance.ConsentWrite) error {
	command := write.Result.Command()
	switch {
	case write.OperationID == "" || command.OperationID() == "":
		return fmt.Errorf("%w: a consent write needs its operation and command", governance.ErrInvalidRequest)
	case !t.inScope(command.Reference()):
		return fmt.Errorf("%w: command %q belongs to another suite", governance.ErrInvalidRequest, command.SourceCommandID())
	case write.Result.Duplicate() != write.Alias || (!write.Alias && write.OperationID != command.OperationID()):
		return fmt.Errorf("%w: operation %q does not match the command result it records", governance.ErrOperationConflict, write.OperationID)
	case write.Receipt.Result.Command().SourceCommandID() != command.SourceCommandID():
		return fmt.Errorf("%w: the receipt belongs to another source command", governance.ErrInvalidRequest)
	}
	kind, receipt, err := encodeReceipt(governance.OperationConsent, &write.Receipt, nil)
	if err != nil {
		return invalidRequest("consent receipt", err)
	}
	operation := dbgen.InsertOperationParams{OperationID: string(write.OperationID), ProjectID: string(t.project), SuiteID: string(t.suite), Kind: kind,
		SourceCommandID: pgtype.Text{String: string(command.SourceCommandID()), Valid: true}, Receipt: receipt}
	if !write.Alias {
		result, err := t.consentResultRow(write.Result)
		if err != nil {
			return invalidRequest("consent result", err)
		}
		if err := t.q.InsertConsentResult(ctx, result); err != nil {
			return translateWrite("insert consent result", err)
		}
	}
	if err := t.q.InsertOperation(ctx, operation); err != nil {
		return translateWrite("insert consent operation", err)
	}
	return t.bump(ctx, pgtype.Text{})
}

func (t *tx) consentResultRow(result contract.CommandResult) (dbgen.InsertConsentResultParams, error) {
	command := result.Command()
	actor, err := encodePrincipal(command.Actor())
	if err != nil {
		return dbgen.InsertConsentResultParams{}, err
	}
	action, err := consentActionCode(command.Action())
	if err != nil {
		return dbgen.InsertConsentResultParams{}, err
	}
	outcome, err := consentOutcomeCode(result.Outcome())
	if err != nil {
		return dbgen.InsertConsentResultParams{}, err
	}
	reason, err := consentReasonCode(result.Reason())
	if err != nil {
		return dbgen.InsertConsentResultParams{}, err
	}
	if command.Order() > math.MaxInt64 {
		return dbgen.InsertConsentResultParams{}, errors.New("command order exceeds the stored range")
	}
	reference := command.Reference()
	return dbgen.InsertConsentResultParams{
		SourceCommandID: string(command.SourceCommandID()), OperationID: string(command.OperationID()), ProjectID: string(reference.ProjectID), SuiteID: string(reference.SuiteID),
		ProposalID: string(reference.ProposalID), RevisionID: string(reference.RevisionID), ActorID: actor.ID, ActorKind: actor.Kind, CarrierID: string(command.Carrier()),
		Action: action, CommandOrder: int64(command.Order()), Outcome: outcome, Reason: reason,
	}, nil
}

func (t *tx) RecordPromotion(ctx context.Context, write governance.PromotionWrite) error {
	receipt, record := write.Receipt, write.Receipt.Record
	switch {
	case record.IsZero() || write.Version.ID() == "":
		return fmt.Errorf("%w: a promotion write needs its record and version", governance.ErrInvalidRequest)
	case !t.inScope(record.Binding().Reference()) || write.Version.ProjectID() != t.project || write.Version.SuiteID() != t.suite:
		return fmt.Errorf("%w: promotion %q belongs to another suite", governance.ErrInvalidRequest, record.OperationID())
	case receipt.Identity.Request.OperationID != record.OperationID():
		return fmt.Errorf("%w: the receipt request and the promotion record name different operations", governance.ErrInvalidRequest)
	case receipt.Identity.CorrectsVersionID != record.CorrectsVersionID() || !receipt.Identity.Binding.Equal(record.Binding()):
		return fmt.Errorf("%w: the receipt identity and the promotion record disagree about the corrected version or the approval binding", governance.ErrInvalidRequest)
	case !exactMicroseconds(record.RecordedAt()):
		return fmt.Errorf("%w: recorded time %s needs sub-microsecond precision that PostgreSQL does not keep", governance.ErrInvalidRequest, record.RecordedAt().Format(time.RFC3339Nano))
	}
	if _, err := contract.NewHistoricalCanonical(write.Version, record); err != nil {
		return invalidRequest("version and promotion record", err)
	}
	kind, payload, err := encodeReceipt(receipt.Identity.Kind, nil, &receipt)
	if err != nil {
		return invalidRequest("promotion receipt", err)
	}
	manifest, err := encodeManifest(write.Version.Manifest())
	if err != nil {
		return invalidRequest("version manifest", err)
	}
	// The bytes of the version being written are verified now and never again.
	if err := t.verifyArtifacts(ctx, write.Version.Manifest()); err != nil {
		return err
	}
	reference := record.Binding().Reference()
	if err := t.q.InsertOperation(ctx, dbgen.InsertOperationParams{OperationID: string(record.OperationID()), ProjectID: string(t.project), SuiteID: string(t.suite), Kind: kind, Receipt: payload}); err != nil {
		return translateWrite("insert promotion operation", err)
	}
	if err := t.q.InsertSuiteVersion(ctx, dbgen.InsertSuiteVersionParams{ProjectID: string(t.project), SuiteID: string(t.suite), VersionID: string(write.Version.ID()),
		ManifestDigest: write.Version.Manifest().Digest().String(), Manifest: manifest}); err != nil {
		return translateWrite("insert suite version", err)
	}
	if err := t.q.InsertPromotion(ctx, dbgen.InsertPromotionParams{OperationID: string(record.OperationID()), ProjectID: string(t.project), SuiteID: string(t.suite),
		VersionID: string(record.VersionID()), ProposalID: string(reference.ProposalID), RevisionID: string(reference.RevisionID), CarrierID: string(record.Carrier()),
		SourceRevision: string(record.Source()), TargetID: string(record.Target()), RecordedAt: pgtype.Timestamptz{Time: record.RecordedAt(), Valid: true},
		CorrectsVersionID: optionalText(string(record.CorrectsVersionID()))}); err != nil {
		return translateWrite("insert promotion", err)
	}
	return t.bump(ctx, pgtype.Text{String: string(write.Version.ID()), Valid: true})
}

func (t *tx) AppendProposalRevision(ctx context.Context, _ governance.ProposalWrite) error {
	return errors.New("postgres: AppendProposalRevision is not implemented")
}

// exactMicroseconds reports whether PostgreSQL's timestamptz keeps the time exactly.
func exactMicroseconds(moment time.Time) bool { return moment.Equal(moment.Truncate(time.Microsecond)) }

func (t *tx) verifyArtifacts(ctx context.Context, manifest artifact.Manifest) error {
	verified := map[artifact.Digest]struct{}{}
	for _, entry := range manifest.Entries() {
		if _, done := verified[entry.Content]; done {
			continue
		}
		if err := t.artifacts.Verify(ctx, entry.Content); err != nil {
			return fmt.Errorf("verify artifact content %s of %q: %w", entry.Content, entry.Path, err)
		}
		verified[entry.Content] = struct{}{}
	}
	return nil
}

// bump advances the Suite revision by exactly one, optionally moving the
// current version.
func (t *tx) bump(ctx context.Context, current pgtype.Text) error {
	if _, err := t.q.BumpSuiteRevision(ctx, dbgen.BumpSuiteRevisionParams{CurrentVersionID: current, ProjectID: string(t.project), SuiteID: string(t.suite)}); err != nil {
		return fmt.Errorf("advance suite revision: %w", err)
	}
	return nil
}

// Enqueue and Outbox write through the unit of work's own transaction, so
// they commit or roll back with the governance facts and never advance the
// Suite revision.

func (t *tx) Enqueue(ctx context.Context, job governance.Job) error {
	_, err := enqueue(ctx, t.jobs, t.raw, job)
	return err
}

func (t *tx) Outbox(ctx context.Context, message governance.OutboxMessage) error {
	return insertOutbox(ctx, t.q, pgtype.Text{String: string(t.project), Valid: true}, pgtype.Text{String: string(t.suite), Valid: true}, message)
}
