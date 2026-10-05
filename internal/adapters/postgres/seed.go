package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// Seed writes the trusted initial state of one Suite in one transaction. It
// takes the Suite's revision as given, never advances it, and does not verify
// artifact content: seeded history was verified by whatever produced it.
// A Suite that already exists, or state that contradicts itself, is rejected
// with ErrInvalidRequest and leaves nothing behind.
func (s *Store) Seed(ctx context.Context, seed governance.Seed) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSeed(seed); err != nil {
		return err
	}
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin seed transaction: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		rollback, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
		defer cancel()
		_ = transaction.Rollback(rollback)
	}()
	if err := writeSeed(ctx, dbgen.New(transaction), seed); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := transaction.Commit(ctx); err != nil {
		return seedFailure("commit seed", err)
	}
	committed = true
	return nil
}

// seedFailure reports a failed seed statement. A violated constraint means
// the supplied state contradicts the schema or the stored state.
func seedFailure(what string, err error) error {
	var failure *pgconn.PgError
	if errors.As(err, &failure) && len(failure.Code) == 5 && failure.Code[:2] == "23" {
		return fmt.Errorf("%w: %s: %w", governance.ErrInvalidRequest, what, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func seedInvalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: seed: %s", governance.ErrInvalidRequest, fmt.Sprintf(format, arguments...))
}

// validateSeed checks everything that can be checked without the database.
func validateSeed(seed governance.Seed) error {
	state := seed.Suite
	suite := state.Canonical.Suite()
	project, id := suite.ProjectID(), suite.ID()
	if state.Canonical.IsZero() || state.Target == "" || state.Policy.RevisionID() == "" || state.Policy.ProjectID() != project ||
		state.Schedule.IsZero() || state.Schedule.ProjectID() != project || state.Schedule.SuiteID() != id {
		return seedInvalid("incomplete or inconsistent suite state")
	}
	current, hasCurrent := suite.CurrentVersionID()
	inHistory := map[contract.SuiteVersionID]struct{}{}
	for _, historical := range seed.History {
		version, record := historical.Version(), historical.Record()
		reference := record.Binding().Reference()
		if historical.IsZero() || version.ProjectID() != project || version.SuiteID() != id || reference.ProjectID != project || reference.SuiteID != id {
			return seedInvalid("history entry %q belongs to another suite", version.ID())
		}
		if !exactMicroseconds(record.RecordedAt()) {
			return seedInvalid("version %q was recorded at a time PostgreSQL cannot store exactly", version.ID())
		}
		inHistory[version.ID()] = struct{}{}
	}
	if _, ok := inHistory[current]; hasCurrent && !ok {
		return seedInvalid("current version %q is missing from the history", current)
	}
	carriers := map[contract.ProposalID]contract.ApprovalCarrierID{}
	for _, seeded := range seed.Proposals {
		if seeded.Proposal.IsZero() {
			return seedInvalid("proposal without revisions")
		}
		bindings := map[contract.ProposalRevisionID]contract.ApprovalBinding{}
		first := seeded.Proposal.Revisions()[0].Binding().Reference()
		for _, revision := range seeded.Proposal.Revisions() {
			reference := revision.Binding().Reference()
			if reference.ProjectID != project || reference.SuiteID != id || reference.ProposalID != first.ProposalID {
				return seedInvalid("revision %q of proposal %q belongs to another suite or proposal", reference.RevisionID, first.ProposalID)
			}
			bindings[reference.RevisionID] = revision.Binding()
		}
		carriers[first.ProposalID] = seeded.Proposal.Current().Carrier()
		for _, assessment := range seeded.Assessments {
			if err := validateAssessment(first.ProposalID, bindings, assessment); err != nil {
				return err
			}
		}
	}
	for _, entry := range state.Schedule.Entries() {
		if carrier, seeded := carriers[entry.ProposalID()]; !seeded || carrier != entry.Carrier() {
			return seedInvalid("schedule entry %q has no seeded proposal on carrier %q", entry.ProposalID(), entry.Carrier())
		}
	}
	return nil
}

// validateAssessment rejects an assessment that could not be rebuilt from what
// is stored: both its expected binding and its evidence binding are rebuilt
// from revisions of this proposal, so each must equal the revision it names.
func validateAssessment(proposal contract.ProposalID, bindings map[contract.ProposalRevisionID]contract.ApprovalBinding, assessment contract.IntegrityAssessment) error {
	reference := assessment.Binding().Reference()
	if stored, ok := bindings[reference.RevisionID]; !ok || reference.ProposalID != proposal || !stored.Equal(assessment.Binding()) {
		return seedInvalid("assessment of %q names no stored revision of proposal %q", reference.RevisionID, proposal)
	}
	if evidence, present := assessment.Evidence(); present {
		cited := evidence.Binding().Reference()
		if stored, ok := bindings[cited.RevisionID]; !ok || cited.ProposalID != proposal || !stored.Equal(evidence.Binding()) {
			return seedInvalid("evidence for proposal %q cites revision %q of proposal %q, which is not a stored revision of that proposal", proposal, cited.RevisionID, cited.ProposalID)
		}
	}
	return nil
}

func writeSeed(ctx context.Context, q *dbgen.Queries, seed governance.Seed) error {
	state := seed.Suite
	suite := state.Canonical.Suite()
	project, id := string(suite.ProjectID()), string(suite.ID())
	if err := seedPolicy(ctx, q, state.Policy); err != nil {
		return err
	}
	current, _ := suite.CurrentVersionID()
	// The current-version pointer is checked when the transaction commits.
	if err := q.InsertSuite(ctx, dbgen.InsertSuiteParams{ProjectID: project, SuiteID: id, Revision: int64(suite.Revision()), CurrentVersionID: optionalText(string(current)),
		TargetID: string(state.Target), PolicyRevisionID: string(state.Policy.RevisionID()), ScheduleGeneration: int64(state.Schedule.Generation())}); err != nil {
		return seedFailure("insert suite", err)
	}
	for _, seeded := range seed.Proposals {
		if err := seedProposal(ctx, q, seeded.Proposal); err != nil {
			return err
		}
	}
	for _, historical := range seed.History {
		if err := seedVersion(ctx, q, historical); err != nil {
			return err
		}
	}
	for _, seeded := range seed.Proposals {
		for _, assessment := range seeded.Assessments {
			if err := seedAssessment(ctx, q, assessment); err != nil {
				return err
			}
		}
	}
	for i, entry := range state.Schedule.Entries() {
		code, err := scheduleStateCode(entry.State())
		if err != nil {
			return seedInvalid("schedule entry %q: %v", entry.ProposalID(), err)
		}
		if err := q.InsertScheduleEntry(ctx, dbgen.InsertScheduleEntryParams{ProjectID: project, SuiteID: id, ProposalID: string(entry.ProposalID()),
			CarrierID: string(entry.Carrier()), Position: int64(i + 1), State: code}); err != nil {
			return seedFailure("insert schedule entry "+string(entry.ProposalID()), err)
		}
	}
	return nil
}

// seedPolicy shares an existing policy revision between Suites, but refuses a
// revision that is already stored with another owner.
func seedPolicy(ctx context.Context, q *dbgen.Queries, policy contract.Policy) error {
	kind, err := principalKindCode(contract.Human) // the MVP registers a human owner
	if err != nil {
		return err
	}
	stored, err := q.GetPolicy(ctx, dbgen.GetPolicyParams{ProjectID: string(policy.ProjectID()), RevisionID: string(policy.RevisionID())})
	switch {
	case err == nil:
		if stored.OwnerID != string(policy.OwnerID()) || stored.OwnerKind != kind {
			return seedInvalid("policy revision %q is already stored with another owner", policy.RevisionID())
		}
		return nil
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("load policy: %w", err)
	}
	if err := q.InsertPolicy(ctx, dbgen.InsertPolicyParams{ProjectID: string(policy.ProjectID()), RevisionID: string(policy.RevisionID()), OwnerID: string(policy.OwnerID()), OwnerKind: kind}); err != nil {
		return seedFailure("insert policy", err)
	}
	return nil
}

func seedProposal(ctx context.Context, q *dbgen.Queries, proposal contract.Proposal) error {
	current := proposal.Current()
	reference := current.Binding().Reference()
	if err := q.InsertProposal(ctx, dbgen.InsertProposalParams{ProjectID: string(reference.ProjectID), SuiteID: string(reference.SuiteID), ProposalID: string(reference.ProposalID), CarrierID: string(current.Carrier())}); err != nil {
		return seedFailure("insert proposal "+string(reference.ProposalID), err)
	}
	for i, revision := range proposal.Revisions() {
		binding := revision.Binding()
		covered, err := encodeCoveredInputs(binding)
		if err != nil {
			return seedInvalid("covered inputs of revision %q: %v", binding.Reference().RevisionID, err)
		}
		if err := q.InsertProposalRevision(ctx, dbgen.InsertProposalRevisionParams{ProjectID: string(reference.ProjectID), SuiteID: string(reference.SuiteID), ProposalID: string(reference.ProposalID),
			RevisionID: string(binding.Reference().RevisionID), Seq: int64(i + 1), Origin: string(revision.Origin()), CarrierID: string(revision.Carrier()),
			ManifestDigest: binding.ManifestDigest().String(), ScopeDigest: binding.ScopeDigest().String(), CoveredInputs: covered,
			ExpectedVersionID: optionalText(string(binding.ExpectedCanonical())), PolicyRevisionID: string(binding.PolicyRevisionID())}); err != nil {
			return seedFailure("insert revision "+string(binding.Reference().RevisionID), err)
		}
	}
	return nil
}

func seedVersion(ctx context.Context, q *dbgen.Queries, historical contract.HistoricalCanonical) error {
	version, record := historical.Version(), historical.Record()
	manifest, err := encodeManifest(version.Manifest())
	if err != nil {
		return seedInvalid("manifest of version %q: %v", version.ID(), err)
	}
	if err := q.InsertSuiteVersion(ctx, dbgen.InsertSuiteVersionParams{ProjectID: string(version.ProjectID()), SuiteID: string(version.SuiteID()), VersionID: string(version.ID()),
		ManifestDigest: version.Manifest().Digest().String(), Manifest: manifest}); err != nil {
		return seedFailure("insert version "+string(version.ID()), err)
	}
	reference := record.Binding().Reference()
	// Seeded history has no operation row: there was no receipt to replay.
	if err := q.InsertPromotion(ctx, dbgen.InsertPromotionParams{OperationID: string(record.OperationID()), ProjectID: string(reference.ProjectID), SuiteID: string(reference.SuiteID),
		VersionID: string(version.ID()), ProposalID: string(reference.ProposalID), RevisionID: string(reference.RevisionID), CarrierID: string(record.Carrier()),
		SourceRevision: string(record.Source()), TargetID: string(record.Target()), RecordedAt: pgtype.Timestamptz{Time: record.RecordedAt().Truncate(time.Microsecond), Valid: true},
		CorrectsVersionID: optionalText(string(record.CorrectsVersionID()))}); err != nil {
		return seedFailure("insert promotion of version "+string(version.ID()), err)
	}
	return nil
}

func seedAssessment(ctx context.Context, q *dbgen.Queries, assessment contract.IntegrityAssessment) error {
	reference := assessment.Binding().Reference()
	row := dbgen.InsertAssessmentParams{ProjectID: string(reference.ProjectID), SuiteID: string(reference.SuiteID), ProposalID: string(reference.ProposalID),
		RevisionID: string(reference.RevisionID), Source: string(assessment.Source())}
	if evidence, present := assessment.Evidence(); present {
		outcome, err := integrityOutcomeCode(evidence.Outcome())
		if err != nil {
			return seedInvalid("evidence of assessment %q: %v", assessment.Source(), err)
		}
		row.EvidenceEmitter = pgtype.Text{String: string(evidence.EmitterID()), Valid: true}
		row.EvidenceSource = pgtype.Text{String: string(evidence.Source()), Valid: true}
		row.EvidenceRevisionID = pgtype.Text{String: string(evidence.Binding().Reference().RevisionID), Valid: true}
		row.Outcome = pgtype.Text{String: outcome, Valid: true}
	}
	if err := q.InsertAssessment(ctx, row); err != nil {
		return seedFailure("insert assessment "+string(assessment.Source()), err)
	}
	return nil
}
