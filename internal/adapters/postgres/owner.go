package postgres

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

var _ owner.Store = (*Store)(nil)

// Enroll implements owner.Store in one transaction. It locks the enrollment
// table first, so the generation and the owner it checks are the ones it
// replaces, and consumes the step last, so a used step writes nothing.
func (s *Store) Enroll(ctx context.Context, e owner.Enrollment) error {
	if err := e.Validate(); err != nil {
		return err
	}
	ownerKind, err := principalKindCode(contract.Human)
	if err != nil {
		return err
	}
	transaction, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin owner enrollment transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollback(ctx, transaction)
		}
	}()
	q := dbgen.New(transaction)

	if err := q.LockOwnerEnrollment(ctx); err != nil {
		return fmt.Errorf("lock owner enrollment: %w", err)
	}
	previous, err := q.GetOwnerEnrollment(ctx)
	enrolled := !errors.Is(err, pgx.ErrNoRows)
	if err != nil && enrolled {
		return fmt.Errorf("read owner enrollment: %w", err)
	}
	projects := slices.Clone(e.Projects)
	slices.SortFunc(projects, func(a, b owner.ProjectPolicy) int { return cmp.Compare(a.RepositoryID, b.RepositoryID) })
	if enrolled && previous.AccountID != e.Owner.AccountID {
		if err := refuseTakeover(ctx, q, projects); err != nil {
			return err
		}
	}

	generation := previous.Generation + 1 // zero before the first enrollment
	revision := string(owner.RevisionID(generation))
	for _, p := range projects {
		err := q.InsertPolicy(ctx, dbgen.InsertPolicyParams{ProjectID: string(owner.ProjectID(p.RepositoryID)), RevisionID: revision,
			OwnerID: string(owner.PrincipalID(e.Owner.AccountID)), OwnerKind: ownerKind})
		if err != nil {
			return fmt.Errorf("write policy of repository %d: %w", p.RepositoryID, err)
		}
	}
	err = q.UpsertOwnerEnrollment(ctx, dbgen.UpsertOwnerEnrollmentParams{Generation: generation, AccountID: e.Owner.AccountID, Login: e.Owner.Login,
		Secret: []byte(e.Secret), Tier: e.Tier, PolicyRevisionID: revision, EnrolledAt: timestamp(e.EnrolledAt)})
	if err != nil {
		return fmt.Errorf("write owner enrollment: %w", err)
	}
	for _, p := range projects {
		err := q.InsertOwnerTestCheck(ctx, dbgen.InsertOwnerTestCheckParams{RepositoryID: p.RepositoryID, PolicyRevisionID: revision,
			ProjectID: string(owner.ProjectID(p.RepositoryID)), Repo: p.Repo, TestCheck: p.TestCheck})
		if err != nil {
			return fmt.Errorf("write test check of repository %d: %w", p.RepositoryID, err)
		}
	}
	inserted, err := q.InsertConsumedStep(ctx, dbgen.InsertConsumedStepParams{AccountID: e.Owner.AccountID, Step: e.Step, ConsumedAt: timestamp(e.EnrolledAt),
		SourceCommandID: string(owner.SetupSource(generation))})
	if err != nil {
		return fmt.Errorf("consume TOTP step: %w", err)
	}
	if inserted == 0 {
		return owner.ErrStepUsed
	}
	if err := transaction.Commit(ctx); err != nil {
		return fmt.Errorf("commit owner enrollment transaction: %w", err)
	}
	committed = true
	return nil
}

// refuseTakeover rejects an enrollment by another owner of a project that already has a Suite.
func refuseTakeover(ctx context.Context, q *dbgen.Queries, projects []owner.ProjectPolicy) error {
	ids := make([]string, len(projects))
	for i, p := range projects {
		ids[i] = string(owner.ProjectID(p.RepositoryID))
	}
	hasSuite, err := q.AnyProjectHasSuite(ctx, ids)
	if err != nil {
		return fmt.Errorf("check Suites of the enrolled projects: %w", err)
	}
	if hasSuite {
		return fmt.Errorf("%w: another owner cannot take over a project that has a Suite", owner.ErrInvalidState)
	}
	return nil
}

// Current implements owner.Store. The enrollment and its projects are read in
// one snapshot, so a concurrent Enroll is seen whole or not at all.
func (s *Store) Current(ctx context.Context) (owner.State, bool, error) {
	transaction, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return owner.State{}, false, fmt.Errorf("begin owner read transaction: %w", err)
	}
	defer rollback(ctx, transaction)
	q := dbgen.New(transaction)
	row, err := q.GetOwnerEnrollment(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return owner.State{}, false, nil
	}
	if err != nil {
		return owner.State{}, false, fmt.Errorf("read owner enrollment: %w", err)
	}
	checks, err := q.ListOwnerTestChecks(ctx, row.PolicyRevisionID)
	if err != nil {
		return owner.State{}, false, fmt.Errorf("read test checks: %w", err)
	}
	if len(checks) == 0 {
		return owner.State{}, false, fmt.Errorf("owner enrollment %s has no projects", row.PolicyRevisionID)
	}
	projects := make([]owner.ProjectPolicy, len(checks))
	for i, c := range checks {
		projects[i] = owner.ProjectPolicy{RepositoryID: c.RepositoryID, Repo: c.Repo, TestCheck: c.TestCheck}
	}
	return owner.State{Owner: owner.Owner{AccountID: row.AccountID, Login: row.Login}, Secret: row.Secret, Projects: projects, Tier: row.Tier,
		PolicyRevision: contract.PolicyRevisionID(row.PolicyRevisionID), EnrolledAt: observedTime(row.EnrolledAt)}, true, nil
}

// ConsumeStep implements owner.Store with one statement that tells an account that is not the owner from a step
// already used. A step that was used is success when its stored source is this one: the earlier call may have
// crashed before its command was recorded.
func (s *Store) ConsumeStep(ctx context.Context, accountID int64, step int64, at time.Time, source contract.SourceCommandID) error {
	if at.IsZero() || source == "" {
		return fmt.Errorf("%w: the consumption time and the source command are required", owner.ErrInvalidState)
	}
	q := dbgen.New(s.pool)
	row, err := q.ConsumeOwnerStep(ctx, dbgen.ConsumeOwnerStepParams{AccountID: accountID, Step: step, ConsumedAt: timestamp(at), SourceCommandID: string(source)})
	if err != nil {
		return fmt.Errorf("consume TOTP step: %w", err)
	}
	if row.Enrolled == 0 {
		return owner.ErrNotEnrolled
	}
	if row.Consumed == 1 {
		return nil
	}
	used, err := q.GetConsumedStepSource(ctx, dbgen.GetConsumedStepSourceParams{AccountID: accountID, Step: step})
	if err != nil {
		return fmt.Errorf("read the source of a used TOTP step: %w", err)
	}
	if used != string(source) {
		return owner.ErrStepUsed
	}
	return nil
}

// RecordProtection implements owner.Store.
func (s *Store) RecordProtection(ctx context.Context, r owner.ProtectionResult) error {
	if err := r.Validate(); err != nil {
		return err
	}
	err := dbgen.New(s.pool).UpsertProtectionResult(ctx, dbgen.UpsertProtectionResultParams{RepositoryID: r.RepositoryID, Result: r.Result,
		Findings: nonNilCodes(r.Findings), Caveats: nonNilCodes(r.Caveats), CheckedAt: timestamp(r.CheckedAt)})
	if err != nil {
		return fmt.Errorf("write protection result: %w", err)
	}
	return nil
}

// nonNilCodes is codes with a nil list as an empty one: pgx sends nil as NULL.
func nonNilCodes(codes []string) []string {
	if codes == nil {
		return []string{}
	}
	return codes
}

// Protection implements owner.Store.
func (s *Store) Protection(ctx context.Context) ([]owner.ProtectionResult, error) {
	rows, err := dbgen.New(s.pool).ListProtectionResults(ctx)
	if err != nil {
		return nil, fmt.Errorf("read protection results: %w", err)
	}
	results := make([]owner.ProtectionResult, len(rows))
	for i, row := range rows {
		results[i] = owner.ProtectionResult{RepositoryID: row.RepositoryID, Result: row.Result, Findings: row.Findings, Caveats: row.Caveats, CheckedAt: observedTime(row.CheckedAt)}
	}
	return results, nil
}
