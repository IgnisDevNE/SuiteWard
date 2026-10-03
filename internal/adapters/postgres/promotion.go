package postgres

import (
	"context"
	"reflect"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func (s *Store) CommitPromotion(ctx context.Context, fence governance.AuthorityFence, write governance.PromotionWrite) error {
	identity := write.Receipt.Identity
	request := identity.Request
	if request.OperationID == "" {
		return governance.ErrInvalidRequest
	}
	tx, queries, state, err := s.lockAuthority(ctx, fence)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	operation, err := s.findReceipt(ctx, queries, string(request.OperationID), false)
	if err != nil {
		return err
	}
	if operation.Kind != 0 {
		return governance.ErrOperationConflict
	}
	version, _, err := s.loadVersion(ctx, queries, string(fence.ProjectID), string(fence.SuiteID), string(request.NewVersionID))
	if err != nil {
		return err
	}
	if !version.IsZero() {
		return governance.ErrVersionConflict
	}
	snapshot, err := s.snapshot(ctx, queries, state, governance.ReadRequest{Reference: request.Reference, AssessmentSource: request.AssessmentSource, HistoricalVersionID: identity.CorrectsVersionID})
	if err != nil {
		return err
	}
	input := contract.PromotionInput{Context: contract.PromotionContext{Canonical: snapshot.Canonical, Proposed: request.Proposed, Proposal: snapshot.Proposal, Reference: request.Reference, Carrier: request.Carrier, Policy: snapshot.Policy, Consent: snapshot.Consent, Assessment: snapshot.Assessment, Scheduling: snapshot.Scheduling, ExpectedStateRevision: fence.Revision, ExpectedSchedulingGeneration: snapshot.Scheduling.Generation()}, Integration: request.Integration, Target: snapshot.Target, OperationID: request.OperationID, NewVersionID: request.NewVersionID, RecordedAt: request.RecordedAt, CorrectsVersionID: identity.CorrectsVersionID}
	var decision contract.PromotionDecision
	switch identity.Kind {
	case governance.OperationPromote:
		if identity.BootstrapMode != 0 || identity.CorrectsVersionID != "" {
			return governance.ErrInvalidRequest
		}
		decision, err = contract.DecidePromotion(input)
	case governance.OperationBootstrap:
		if identity.CorrectsVersionID != "" {
			return governance.ErrInvalidRequest
		}
		decision, err = contract.DecideBootstrap(contract.BootstrapInput{Mode: identity.BootstrapMode, Promotion: input})
	case governance.OperationCorrect:
		if identity.BootstrapMode != 0 || identity.CorrectsVersionID == "" {
			return governance.ErrInvalidRequest
		}
		decision, err = contract.DecideCorrection(contract.CorrectionInput{Promotion: input, Target: snapshot.History})
	default:
		return governance.ErrInvalidRequest
	}
	if err != nil || decision.Outcome() != contract.PromotionProposed {
		return governance.ErrInvalidRequest
	}
	identity.Binding = snapshot.Proposal.Current().Binding()
	if !reflect.DeepEqual(write.Receipt, governance.PromotionReceipt{Identity: identity, Decision: decision}) {
		return governance.ErrInvalidRequest
	}
	scheduling, err := snapshot.Scheduling.Observe(snapshot.Proposal, snapshot.Scheduling.Generation(), contract.ObservePromoted)
	if err != nil || !reflect.DeepEqual(scheduling, write.Scheduling) {
		return governance.ErrInvalidRequest
	}
	if err := s.verifyManifest(ctx, request.Proposed.Manifest()); err != nil {
		return err
	}
	effect, _ := decision.Effect()
	state.canonical, err = contract.NewCanonicalSnapshot(effect.Suite(), effect.Version(), request.Proposed, effect.Promotion())
	if err != nil {
		return governance.ErrInvalidRequest
	}
	state.scheduling = scheduling
	history, err := contract.NewHistoricalCanonical(effect.Version(), effect.Promotion())
	if err != nil {
		return governance.ErrInvalidRequest
	}
	versionPayload, err := contract.EncodeStateCheckpoint(contract.StateCheckpoint{History: history, Protected: request.Proposed})
	if err != nil {
		return governance.ErrInvalidRequest
	}
	receipt := governance.OperationReceipt{Kind: identity.Kind, Promotion: write.Receipt}
	encoded, err := encodeReceipt(receipt)
	if err != nil {
		return governance.ErrInvalidRequest
	}
	project, suite := string(fence.ProjectID), string(fence.SuiteID)
	record := effect.Promotion()
	reference := record.Binding().Reference()
	if err := queries.InsertVersion(ctx, dbgen.InsertVersionParams{ProjectID: project, SuiteID: suite, VersionID: string(record.VersionID()), ManifestDigest: effect.Version().Manifest().Digest().String(), VersionPayload: versionPayload}); err != nil {
		return storageError(err)
	}
	oldCurrent, _ := snapshot.Canonical.Suite().CurrentVersionID()
	if err := casAuthority(ctx, queries, fence, oldCurrent, state); err != nil {
		return err
	}
	if err := queries.InsertOperation(ctx, dbgen.InsertOperationParams{OperationID: string(record.OperationID()), ProjectID: project, SuiteID: suite, Kind: int16(identity.Kind), ReceiptPayload: encoded}); err != nil {
		return storageError(err)
	}
	if err := queries.InsertPromotion(ctx, dbgen.InsertPromotionParams{OperationID: string(record.OperationID()), ProjectID: project, SuiteID: suite, OperationKind: int16(identity.Kind), VersionID: string(record.VersionID()), ProposalID: string(reference.ProposalID), ProposalRevisionID: string(reference.RevisionID), ExpectedVersionID: nullableVersion(record.Binding().ExpectedCanonical()), CorrectsVersionID: nullableVersion(record.CorrectsVersionID()), CarrierID: string(record.Carrier()), SourceRevision: string(record.Source()), TargetID: string(record.Target()), RecordedAt: pgtype.Timestamptz{Time: record.RecordedAt(), Valid: true}, PromotionPayload: encoded}); err != nil {
		return storageError(err)
	}
	if err := queries.InsertAudit(ctx, dbgen.InsertAuditParams{OperationID: string(record.OperationID()), ProjectID: project, SuiteID: suite, EventKind: int16(identity.Kind), EventPayload: encoded}); err != nil {
		return storageError(err)
	}
	if err := queries.InsertPublication(ctx, dbgen.InsertPublicationParams{OperationID: string(record.OperationID()), ProjectID: project, SuiteID: suite, PublicationPayload: encoded}); err != nil {
		return storageError(err)
	}
	return storageError(tx.Commit(ctx))
}
