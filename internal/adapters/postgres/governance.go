// Package postgres implements transactional governance with PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

type ArtifactVerifier interface {
	Verify(context.Context, artifact.Digest) error
}

var ErrSchemaNotReady = errors.New("PostgreSQL governance schema not ready")

type Store struct {
	pool     *pgxpool.Pool
	verifier ArtifactVerifier
}

// TrustedAuthority initializes an unbaselined Suite from authenticated instance
// configuration. It cannot replace authority or import processing history.
type TrustedAuthority struct {
	Canonical  contract.CanonicalSnapshot
	Policy     contract.Policy
	Scheduling contract.Schedule
	Target     contract.IntegrationTargetID
	Proposals  []TrustedProposal
}
type TrustedProposal struct {
	Proposal    contract.Proposal
	Consent     contract.Consent
	Assessments []contract.IntegrityAssessment
}

func NewStore(pool *pgxpool.Pool, verifier ArtifactVerifier) (*Store, error) {
	if pool == nil || verifier == nil {
		return nil, governance.ErrInvalidRequest
	}
	return &Store{pool: pool, verifier: verifier}, nil
}
func (s *Store) InitializeTrusted(ctx context.Context, input TrustedAuthority) error {
	if input.Canonical.IsZero() {
		return governance.ErrInvalidRequest
	}
	suite := input.Canonical.Suite()
	if _, present := suite.CurrentVersionID(); present {
		return governance.ErrInvalidRequest
	}
	state := authorityState{canonical: input.Canonical, policy: input.Policy, scheduling: input.Scheduling, target: input.Target, proposals: map[contract.ProposalID]contract.Proposal{}, consents: map[contract.ProposalID]contract.Consent{}, assessments: map[assessmentKey]contract.IntegrityAssessment{}}
	for _, entry := range input.Proposals {
		if entry.Proposal.IsZero() || len(entry.Consent.Results()) != 0 {
			return governance.ErrInvalidRequest
		}
		reference := entry.Proposal.Current().Binding().Reference()
		if !state.proposals[reference.ProposalID].IsZero() {
			return governance.ErrInvalidRequest
		}
		state.proposals[reference.ProposalID] = entry.Proposal
		state.consents[reference.ProposalID] = entry.Consent
		for _, assessment := range entry.Assessments {
			key := assessmentKey{assessment.Binding().Reference(), assessment.Source()}
			if state.assessments[key].Assurance() != 0 {
				return governance.ErrInvalidRequest
			}
			state.assessments[key] = assessment
		}
	}
	encoded, err := encodeAuthority(state)
	if err != nil {
		return fmt.Errorf("%w: %v", governance.ErrInvalidRequest, err)
	}
	if _, err := decodeAuthority(encoded, string(suite.ProjectID()), string(suite.ID()), "", strconv.FormatUint(uint64(suite.Revision()), 10)); err != nil {
		return governance.ErrInvalidRequest
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return storageError(err)
	}
	defer tx.Rollback(context.Background())
	err = dbgen.New(tx).InsertInitialAuthority(ctx, dbgen.InsertInitialAuthorityParams{ProjectID: string(suite.ProjectID()), SuiteID: string(suite.ID()), AuthorityRevision: strconv.FormatUint(uint64(suite.Revision()), 10), GovernancePayload: encoded})
	if err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}

func (s *Store) Load(ctx context.Context, request governance.ReadRequest) (governance.Snapshot, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return governance.Snapshot{}, storageError(err)
	}
	defer tx.Rollback(context.Background())
	queries := dbgen.New(tx)
	operation, err := s.findReceipt(ctx, queries, string(request.OperationID), false)
	if err != nil {
		return governance.Snapshot{}, err
	}
	source, err := s.findReceipt(ctx, queries, string(request.SourceCommandID), true)
	if err != nil {
		return governance.Snapshot{}, err
	}
	// An exact operation replay requires its original outcome, not current authority.
	if operation.Kind != 0 {
		if err := tx.Commit(ctx); err != nil {
			return governance.Snapshot{}, storageError(err)
		}
		return governance.Snapshot{Operation: operation, Source: source}, nil
	}
	if strings.TrimSpace(string(request.Reference.ProjectID)) == "" || strings.TrimSpace(string(request.Reference.SuiteID)) == "" {
		return governance.Snapshot{}, governance.ErrInvalidRequest
	}
	row, err := queries.GetAuthority(ctx, dbgen.GetAuthorityParams{ProjectID: string(request.Reference.ProjectID), SuiteID: string(request.Reference.SuiteID)})
	if errors.Is(err, pgx.ErrNoRows) && source.Kind != 0 {
		if err := tx.Commit(ctx); err != nil {
			return governance.Snapshot{}, storageError(err)
		}
		return governance.Snapshot{Source: source}, nil
	}
	if err != nil {
		return governance.Snapshot{}, storageError(err)
	}
	state, err := decodeAuthority(row.GovernancePayload, row.ProjectID, row.SuiteID, row.CurrentVersionID.String, row.AuthorityRevision)
	if err != nil {
		return governance.Snapshot{}, err
	}
	snapshot, err := s.snapshot(ctx, queries, state, request)
	if err != nil {
		return governance.Snapshot{}, err
	}
	snapshot.Operation = operation
	snapshot.Source = source
	if snapshot.Proposal.IsZero() {
		if source.Kind == 0 {
			return governance.Snapshot{}, governance.ErrNotFound
		}
		snapshot = governance.Snapshot{Source: source}
	}
	if err := tx.Commit(ctx); err != nil {
		return governance.Snapshot{}, storageError(err)
	}
	return snapshot, nil
}

func (s *Store) findReceipt(ctx context.Context, queries *dbgen.Queries, id string, source bool) (governance.OperationReceipt, error) {
	if id == "" {
		return governance.OperationReceipt{}, nil
	}
	var row dbgen.OperationReceipt
	var err error
	if source {
		row, err = queries.FindConsentSource(ctx, id)
	} else {
		row, err = queries.FindOperation(ctx, id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return governance.OperationReceipt{}, nil
	}
	if err != nil {
		return governance.OperationReceipt{}, storageError(err)
	}
	receipt, err := decodeReceipt(row.ReceiptPayload, row.Kind, row.ProjectID, row.SuiteID, row.OperationID)
	if err != nil {
		return governance.OperationReceipt{}, err
	}
	if receipt.Kind == governance.OperationConsent {
		if source && receipt.Consent.Result.Command().OperationID() != contract.OperationID(row.OperationID) {
			return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
		}
		if source && string(receipt.Consent.Result.Command().SourceCommandID()) != id {
			return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
		}
		if version := receipt.Consent.PromotedVersionID; version != "" {
			history, _, err := s.loadVersion(ctx, queries, row.ProjectID, row.SuiteID, string(version))
			if err != nil || history.IsZero() {
				if err != nil {
					return governance.OperationReceipt{}, err
				}
				return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
			}
		}
	} else {
		effect, _ := receipt.Promotion.Decision.Effect()
		if receipt.Promotion.Identity.Request.OperationID != contract.OperationID(row.OperationID) {
			return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
		}
		history, protected, err := s.loadVersion(ctx, queries, row.ProjectID, row.SuiteID, string(effect.Version().ID()))
		if err != nil {
			return governance.OperationReceipt{}, err
		}
		if history.IsZero() || !reflect.DeepEqual(history.Version(), effect.Version()) || !protected.Equal(receipt.Promotion.Identity.Request.Proposed) {
			return governance.OperationReceipt{}, governance.ErrInvalidSnapshot
		}
	}
	return receipt, nil
}

func (s *Store) snapshot(ctx context.Context, queries *dbgen.Queries, state authorityState, request governance.ReadRequest) (governance.Snapshot, error) {
	suite := state.canonical.Suite()
	snapshot := governance.Snapshot{Fence: governance.AuthorityFence{ProjectID: suite.ProjectID(), SuiteID: suite.ID(), Revision: suite.Revision()}, Canonical: state.canonical, Policy: state.policy, Scheduling: state.scheduling, Target: state.target, Proposal: state.proposals[request.Reference.ProposalID], Consent: state.consents[request.Reference.ProposalID], Assessment: state.assessments[assessmentKey{request.Reference, request.AssessmentSource}]}
	if current, present := suite.CurrentVersionID(); present {
		history, protected, err := s.loadVersion(ctx, queries, string(suite.ProjectID()), string(suite.ID()), string(current))
		if err != nil {
			return governance.Snapshot{}, err
		}
		if history.IsZero() || !reflect.DeepEqual(history.Version(), state.canonical.Version()) || !protected.Equal(state.canonical.Contract()) || !reflect.DeepEqual(history.Record(), state.canonical.Record()) {
			return governance.Snapshot{}, governance.ErrInvalidSnapshot
		}
	}
	if request.HistoricalVersionID != "" {
		history, _, err := s.loadVersion(ctx, queries, string(suite.ProjectID()), string(suite.ID()), string(request.HistoricalVersionID))
		if err != nil {
			return governance.Snapshot{}, err
		}
		snapshot.History = history
	}
	row, err := queries.FindPromotionByReference(ctx, dbgen.FindPromotionByReferenceParams{ProjectID: string(request.Reference.ProjectID), SuiteID: string(request.Reference.SuiteID), ProposalID: string(request.Reference.ProposalID), ProposalRevisionID: string(request.Reference.RevisionID)})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return governance.Snapshot{}, storageError(err)
	}
	if err == nil {
		receipt, err := decodeReceipt(row.PromotionPayload, row.OperationKind, row.ProjectID, row.SuiteID, row.OperationID)
		if err != nil {
			return governance.Snapshot{}, err
		}
		effect, present := receipt.Promotion.Decision.Effect()
		if !present || effect.Promotion().OperationID() != contract.OperationID(row.OperationID) || effect.Promotion().Binding().Reference() != request.Reference {
			return governance.Snapshot{}, governance.ErrInvalidSnapshot
		}
		snapshot.HistoricalPromotion = effect.Promotion()
	}
	return snapshot, nil
}

func (s *Store) loadVersion(ctx context.Context, queries *dbgen.Queries, project, suite, version string) (contract.HistoricalCanonical, contract.ProtectedContract, error) {
	row, err := queries.GetVersion(ctx, dbgen.GetVersionParams{ProjectID: project, SuiteID: suite, VersionID: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return contract.HistoricalCanonical{}, contract.ProtectedContract{}, nil
	}
	if err != nil {
		return contract.HistoricalCanonical{}, contract.ProtectedContract{}, storageError(err)
	}
	checkpoint, err := contract.RestoreStateCheckpoint(row.VersionPayload)
	history := checkpoint.History
	protected := checkpoint.Protected
	if err != nil || history.IsZero() || string(history.Version().ProjectID()) != project || string(history.Version().SuiteID()) != suite || string(history.Version().ID()) != version || history.Version().Manifest().Digest().String() != row.ManifestDigest || protected.IsZero() || protected.Manifest().Digest() != history.Version().Manifest().Digest() || !protected.ScopeDigest().IsZero() && protected.ScopeDigest() != history.Record().Binding().ScopeDigest() || !reflect.DeepEqual(protected.CoveredInputs(), history.Record().Binding().CoveredInputs()) {
		return contract.HistoricalCanonical{}, contract.ProtectedContract{}, governance.ErrInvalidSnapshot
	}
	if err := s.verifyManifest(ctx, history.Version().Manifest()); err != nil {
		return contract.HistoricalCanonical{}, contract.ProtectedContract{}, err
	}
	return history, protected, nil
}
func (s *Store) verifyManifest(ctx context.Context, manifest artifact.Manifest) error {
	for _, entry := range manifest.Entries() {
		if err := s.verifier.Verify(ctx, entry.Content); err != nil {
			return fmt.Errorf("verify canonical content: %w", err)
		}
	}
	return nil
}
func storageError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return governance.ErrNotFound
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23505" {
		switch databaseError.ConstraintName {
		case "suites_pkey":
			return governance.ErrAuthorityConflict
		case "suite_versions_pkey", "promotions_reference_key", "promotions_version_key":
			return governance.ErrVersionConflict
		case "operation_receipts_pkey", "consent_sources_pkey":
			return governance.ErrOperationConflict
		}
	}
	return fmt.Errorf("PostgreSQL governance storage: %w", err)
}

var _ governance.Store = (*Store)(nil)
