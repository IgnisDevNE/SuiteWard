package postgres

import (
	"context"
	"math"
	"reflect"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func (s *Store) lockAuthority(ctx context.Context, fence governance.AuthorityFence) (pgx.Tx, *dbgen.Queries, authorityState, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, authorityState{}, storageError(err)
	}
	queries := dbgen.New(tx)
	row, err := queries.LockAuthority(ctx, dbgen.LockAuthorityParams{ProjectID: string(fence.ProjectID), SuiteID: string(fence.SuiteID)})
	if err != nil {
		tx.Rollback(context.Background())
		return nil, nil, authorityState{}, storageError(err)
	}
	state, err := decodeAuthority(row.GovernancePayload, row.ProjectID, row.SuiteID, row.CurrentVersionID.String, row.AuthorityRevision)
	if err != nil {
		tx.Rollback(context.Background())
		return nil, nil, authorityState{}, err
	}
	if state.canonical.Suite().Revision() != fence.Revision {
		tx.Rollback(context.Background())
		return nil, nil, authorityState{}, governance.ErrAuthorityConflict
	}
	if fence.Revision == contract.StateRevision(math.MaxUint64) {
		tx.Rollback(context.Background())
		return nil, nil, authorityState{}, governance.ErrAuthorityExhausted
	}
	return tx, queries, state, nil
}
func nullableVersion(id contract.SuiteVersionID) pgtype.Text {
	return pgtype.Text{String: string(id), Valid: id != ""}
}
func casAuthority(ctx context.Context, queries *dbgen.Queries, fence governance.AuthorityFence, oldCurrent contract.SuiteVersionID, state authorityState) error {
	encoded, err := encodeAuthority(state)
	if err != nil {
		return governance.ErrInvalidRequest
	}
	newCurrent, _ := state.canonical.Suite().CurrentVersionID()
	count, err := queries.CASAuthority(ctx, dbgen.CASAuthorityParams{ProjectID: string(fence.ProjectID), SuiteID: string(fence.SuiteID), ExpectedRevision: strconv.FormatUint(uint64(fence.Revision), 10), NewRevision: strconv.FormatUint(uint64(state.canonical.Suite().Revision()), 10), ExpectedCurrent: nullableVersion(oldCurrent), NewCurrent: nullableVersion(newCurrent), GovernancePayload: encoded})
	if err != nil {
		return storageError(err)
	}
	if count != 1 {
		return governance.ErrAuthorityConflict
	}
	return nil
}

func (s *Store) CommitConsent(ctx context.Context, fence governance.AuthorityFence, write governance.ConsentWrite) error {
	if write.Command.OperationID() == "" {
		return governance.ErrInvalidRequest
	}
	tx, queries, state, err := s.lockAuthority(ctx, fence)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	request := governance.ReadRequest{Reference: write.Command.Reference(), OperationID: write.Command.OperationID(), SourceCommandID: write.Command.SourceCommandID()}
	snapshot, err := s.snapshot(ctx, queries, state, request)
	if err != nil {
		return err
	}
	if snapshot.Proposal.IsZero() {
		return governance.ErrNotFound
	}
	operation, err := s.findReceipt(ctx, queries, string(write.Command.OperationID()), false)
	if err != nil {
		return err
	}
	if operation.Kind != 0 {
		return governance.ErrOperationConflict
	}
	source, err := s.findReceipt(ctx, queries, string(write.Command.SourceCommandID()), true)
	if err != nil {
		return err
	}
	next, result, err := snapshot.Consent.Apply(snapshot.Proposal, snapshot.Policy, write.Command)
	if err != nil {
		return governance.ErrInvalidRequest
	}
	if !reflect.DeepEqual(next, write.Consent) {
		return governance.ErrInvalidRequest
	}
	if write.Alias {
		if !result.Duplicate() || source.Kind != governance.OperationConsent || source.Consent != write.Receipt || result.Command() != write.Receipt.Result.Command() || result.Outcome() != write.Receipt.Result.Outcome() || result.Reason() != write.Receipt.Result.Reason() {
			return governance.ErrInvalidRequest
		}
	} else {
		if result.Duplicate() || source.Kind != 0 || result.Reason() == contract.ConsentReasonCommandConflict {
			return governance.ErrOperationConflict
		}
		expected := governance.ConsentReceipt{Result: result, EvaluatedReference: snapshot.Proposal.Current().Binding().Reference(), PolicyRevisionID: snapshot.Policy.RevisionID(), CurrentApprovalEligible: next.HasApproval(snapshot.Proposal, snapshot.Policy)}
		if !snapshot.HistoricalPromotion.IsZero() {
			expected.PromotedVersionID = snapshot.HistoricalPromotion.VersionID()
		}
		if expected != write.Receipt {
			return governance.ErrInvalidRequest
		}
	}
	current := state.canonical
	oldCurrent, _ := current.Suite().CurrentVersionID()
	suite, err := contract.NewSuite(fence.ProjectID, fence.SuiteID, oldCurrent, fence.Revision+1)
	if err != nil {
		return governance.ErrInvalidRequest
	}
	state.canonical, err = contract.NewCanonicalSnapshot(suite, current.Version(), current.Contract(), current.Record())
	if err != nil {
		return governance.ErrInvalidRequest
	}
	state.consents[request.Reference.ProposalID] = next
	receipt := governance.OperationReceipt{Kind: governance.OperationConsent, Consent: write.Receipt}
	encoded, err := encodeReceipt(receipt)
	if err != nil {
		return governance.ErrInvalidRequest
	}
	if err := casAuthority(ctx, queries, fence, oldCurrent, state); err != nil {
		return err
	}
	if err := queries.InsertOperation(ctx, dbgen.InsertOperationParams{OperationID: string(write.Command.OperationID()), ProjectID: string(fence.ProjectID), SuiteID: string(fence.SuiteID), Kind: int16(governance.OperationConsent), ReceiptPayload: encoded}); err != nil {
		return storageError(err)
	}
	if !write.Alias {
		if err := queries.InsertConsentSource(ctx, dbgen.InsertConsentSourceParams{SourceCommandID: string(write.Command.SourceCommandID()), OperationID: string(write.Command.OperationID()), ProjectID: string(fence.ProjectID), SuiteID: string(fence.SuiteID)}); err != nil {
			return storageError(err)
		}
		if err := queries.InsertAudit(ctx, dbgen.InsertAuditParams{OperationID: string(write.Command.OperationID()), ProjectID: string(fence.ProjectID), SuiteID: string(fence.SuiteID), EventKind: int16(governance.OperationConsent), EventPayload: encoded}); err != nil {
			return storageError(err)
		}
		if err := queries.InsertAcknowledgment(ctx, dbgen.InsertAcknowledgmentParams{OperationID: string(write.Command.OperationID()), ProjectID: string(fence.ProjectID), SuiteID: string(fence.SuiteID), AcknowledgmentPayload: encoded}); err != nil {
			return storageError(err)
		}
	}
	return storageError(tx.Commit(ctx))
}
