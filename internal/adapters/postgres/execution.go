package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// executionStore is execution.Store over the pool of a Store. The lifecycle of a job is applied by the pure functions of the execution
// package, which the in-memory store applies too; this type loads a job under a lock, applies one and writes the result.
type executionStore struct{ pool *pgxpool.Pool }

var _ execution.Store = executionStore{}

// Execution returns the execution ledger of this database.
func (s *Store) Execution() execution.Store { return executionStore{pool: s.pool} }

const (
	codeCheckViolation   = "23514"
	codeNotNullViolation = "23502"
)

// executionWriteError wraps a failed write. A value that breaks a constraint of the schema, or names a row that does not exist, is
// execution.ErrInvalidRequest: the checks of the execution package are the same constraints, so this is a request that they did not catch.
func executionWriteError(what string, err error) error {
	var failure *pgconn.PgError
	if errors.As(err, &failure) && (failure.Code == codeCheckViolation || failure.Code == codeNotNullViolation || failure.Code == codeForeignKeyViolation) {
		return fmt.Errorf("%w: %s: %w", execution.ErrInvalidRequest, what, err)
	}
	return fmt.Errorf("%s: %w", what, err)
}

func jobFromRow(row dbgen.ExecutionJob) (execution.Job, error) {
	governing, err := artifact.ParseDigest(row.GoverningDigest)
	if err != nil {
		return execution.Job{}, fmt.Errorf("read job %d: stored governing digest: %w", row.JobID, err)
	}
	declaration, err := artifact.ParseDigest(row.DeclarationDigest)
	if err != nil {
		return execution.Job{}, fmt.Errorf("read job %d: stored declaration digest: %w", row.JobID, err)
	}
	canonical, err := domain.ParseCanonicalLevel(row.CanonicalLevel)
	if err != nil {
		return execution.Job{}, fmt.Errorf("read job %d: %w", row.JobID, err)
	}
	receipt, err := domain.ParseReceiptLevel(row.ReceiptLevel)
	if err != nil {
		return execution.Job{}, fmt.Errorf("read job %d: %w", row.JobID, err)
	}
	return execution.Job{
		Spec: execution.Spec{RepositoryID: row.RepositoryID, PullNumber: int(row.PullNumber), Plan: execution.Plan(row.Plan), Profile: row.Profile, HeadSHA: row.HeadSha,
			BaseSHA: row.BaseSha.String, GoverningDigest: governing, GoverningSource: domain.ManifestSource(row.GoverningSource), DeclarationDigest: declaration,
			CanonicalLevel: canonical, ReceiptLevel: receipt, UnapprovedPaths: row.UnapprovedPaths, ChangedPaths: row.ChangedPaths, ChangedTotal: int(row.ChangedTotal)},
		ID: row.JobID, Key: row.JobKey, State: execution.State(row.State), Reason: execution.Reason(row.Reason.String), Attempts: int(row.Attempts), Failures: int(row.Failures),
		GitHubSince: observedTime(row.GithubSince), NotBefore: observedTime(row.NotBefore), LeaseID: row.LeaseID.String, LeaseExpiresAt: observedTime(row.LeaseExpiresAt),
		LeasedAt: observedTime(row.LeasedAt), DeadlineAt: observedTime(row.DeadlineAt), CreatedAt: observedTime(row.CreatedAt), FinishedAt: observedTime(row.FinishedAt),
	}, nil
}

func jobsFromRows(rows []dbgen.ExecutionJob) ([]execution.Job, error) {
	jobs := make([]execution.Job, len(rows))
	for i, row := range rows {
		job, err := jobFromRow(row)
		if err != nil {
			return nil, err
		}
		jobs[i] = job
	}
	return jobs, nil
}

func insertParams(j execution.Job) (dbgen.InsertExecutionJobParams, error) {
	pull, err := int32Of(j.PullNumber)
	if err != nil {
		return dbgen.InsertExecutionJobParams{}, err
	}
	total, err := int32Of(j.ChangedTotal)
	if err != nil {
		return dbgen.InsertExecutionJobParams{}, err
	}
	return dbgen.InsertExecutionJobParams{JobKey: j.Key, RepositoryID: j.RepositoryID, PullNumber: pull, Plan: string(j.Plan), Profile: j.Profile, HeadSha: j.HeadSHA,
		BaseSha: optionalText(j.BaseSHA), GoverningDigest: j.GoverningDigest.String(), GoverningSource: string(j.GoverningSource), DeclarationDigest: j.DeclarationDigest.String(),
		CanonicalLevel: j.CanonicalLevel.String(), ReceiptLevel: j.ReceiptLevel.String(), UnapprovedPaths: stringList(j.UnapprovedPaths), ChangedPaths: stringList(j.ChangedPaths),
		ChangedTotal: total, State: string(j.State), Reason: optionalText(string(j.Reason)), NotBefore: timestamp(j.NotBefore), CreatedAt: timestamp(j.CreatedAt),
		FinishedAt: timestamp(j.FinishedAt)}, nil
}

// stringList is the list as a value that PostgreSQL stores as an array: a nil slice is NULL, which the NOT NULL columns refuse.
func stringList(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func updateParams(j execution.Job) (dbgen.UpdateExecutionJobParams, error) {
	attempts, err := int32Of(j.Attempts)
	if err != nil {
		return dbgen.UpdateExecutionJobParams{}, err
	}
	failures, err := int32Of(j.Failures)
	if err != nil {
		return dbgen.UpdateExecutionJobParams{}, err
	}
	return dbgen.UpdateExecutionJobParams{JobID: j.ID, State: string(j.State), Reason: optionalText(string(j.Reason)), Attempts: attempts, Failures: failures,
		GithubSince: timestamp(j.GitHubSince), NotBefore: timestamp(j.NotBefore), LeaseID: optionalText(j.LeaseID), LeaseExpiresAt: timestamp(j.LeaseExpiresAt),
		LeasedAt: timestamp(j.LeasedAt), DeadlineAt: timestamp(j.DeadlineAt), FinishedAt: timestamp(j.FinishedAt)}, nil
}

// storeJob writes the columns that the lifecycle changes. The guard trigger refuses a move that the lifecycle does not allow.
func storeJob(ctx context.Context, q *dbgen.Queries, j execution.Job) error {
	params, err := updateParams(j)
	if err != nil {
		return err
	}
	changed, err := q.UpdateExecutionJob(ctx, params)
	if err != nil {
		return executionWriteError(fmt.Sprintf("write job %d", j.ID), err)
	}
	if changed != 1 {
		return fmt.Errorf("write job %d: %d rows changed; want one", j.ID, changed)
	}
	return nil
}

// lockJob reads a job and holds its lock until the transaction ends: execution.ErrNotFound when there is none.
func lockJob(ctx context.Context, q *dbgen.Queries, id int64) (execution.Job, error) {
	row, err := q.LockExecutionJob(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Job{}, fmt.Errorf("%w: job %d", execution.ErrNotFound, id)
	}
	if err != nil {
		return execution.Job{}, fmt.Errorf("lock job %d: %w", id, err)
	}
	return jobFromRow(row)
}

// Enqueue implements execution.Store. A concurrent Enqueue of the same key waits on the insert and then finds the job; the key can be
// freed between the two statements (the job that held it was cancelled), so the pair is tried again.
func (s executionStore) Enqueue(ctx context.Context, n execution.NewJob) (execution.Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return execution.Job{}, false, err
	}
	if err := n.Validate(); err != nil {
		return execution.Job{}, false, err
	}
	job := n.Job()
	params, err := insertParams(job)
	if err != nil {
		return execution.Job{}, false, err
	}
	q := dbgen.New(s.pool)
	for range 3 {
		row, err := q.InsertExecutionJob(ctx, params)
		if err == nil {
			created, err := jobFromRow(row)
			return created, true, err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return execution.Job{}, false, executionWriteError("write job", err)
		}
		existing, err := q.GetCurrentExecutionJob(ctx, job.Key)
		if err == nil {
			current, err := jobFromRow(existing)
			return current, false, err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return execution.Job{}, false, fmt.Errorf("read the current job of the key: %w", err)
		}
	}
	return execution.Job{}, false, errors.New("the job key keeps changing hands")
}

// Lease implements execution.Store in one transaction: the oldest ready job, with SKIP LOCKED so that two leases never take one. prepare
// runs with the job locked, so the transaction waits on GitHub for it: a failure of GitHub is recorded and committed, and returned once the
// transaction is over; any other failure of prepare rolls the whole call back.
func (s executionStore) Lease(ctx context.Context, req execution.LeaseRequest, prepare execution.Prepare) (execution.Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return execution.Job{}, false, err
	}
	if err := req.Validate(prepare); err != nil {
		return execution.Job{}, false, err
	}
	now := execution.Stored(req.Now)
	var granted execution.Job
	found := false
	var unreadable error
	err := inTx(ctx, s.pool, "execution lease", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		for {
			row, err := q.LeaseCandidate(ctx, timestamp(now))
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("read the next job to lease: %w", err)
			}
			job, err := jobFromRow(row)
			if err != nil {
				return err
			}
			if job.State == execution.StateLeased { // an expired lease: a failure, then the next candidate
				if err := storeJob(ctx, q, job.LeaseLapsed(now)); err != nil {
					return err
				}
				continue
			}
			switch err := prepare(ctx, job); {
			case err == nil:
				granted, found = job.Granted(req), true
				return storeJob(ctx, q, granted)
			case errors.Is(err, execution.ErrGitHubUnavailable):
				unreadable = fmt.Errorf("%w: job %d: %w", execution.ErrGitHubUnavailable, job.ID, err)
				_, err := recordGitHubFailure(ctx, q, job, now, req.GitHubWait)
				return err
			case errors.Is(err, execution.ErrTooLarge):
				if err := storeJob(ctx, q, job.TooLarge(now)); err != nil {
					return err
				}
			default:
				return err
			}
		}
	})
	if err != nil {
		return execution.Job{}, false, err
	}
	if unreadable != nil {
		return execution.Job{}, false, unreadable
	}
	return granted, found, nil
}

// recordGitHubFailure writes a failed read for a pending job, and the results row of the service when the failure ends the job on the
// GitHub bound. It returns the state of the job afterwards.
func recordGitHubFailure(ctx context.Context, q *dbgen.Queries, job execution.Job, now time.Time, githubWait time.Duration) (execution.State, error) {
	next, serviceRow := job.AfterGitHubFailure(now, githubWait)
	if err := storeJob(ctx, q, next); err != nil {
		return "", err
	}
	if serviceRow {
		if err := writeServiceResult(ctx, q, next, now); err != nil {
			return "", err
		}
	}
	return next.State, nil
}

// Heartbeat implements execution.Store.
func (s executionStore) Heartbeat(ctx context.Context, jobID int64, leaseID string, now time.Time) (time.Time, error) {
	var expires time.Time
	err := inTx(ctx, s.pool, "execution heartbeat", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		job, err := lockJob(ctx, q, jobID)
		if err != nil {
			return err
		}
		next, err := job.Heartbeated(leaseID, execution.Stored(now))
		if err != nil {
			return fmt.Errorf("%w: job %d", err, jobID)
		}
		expires = next.LeaseExpiresAt
		return storeJob(ctx, q, next)
	})
	return expires, err
}

// markerJSON is a marker as the markers column holds it.
type markerJSON struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

func insertResultParams(jobID int64, origin execution.Origin, receivedAt time.Time, r execution.Report) (dbgen.InsertExecutionResultParams, error) {
	markers := make([]markerJSON, len(r.Markers))
	for i, m := range r.Markers {
		markers[i] = markerJSON(m)
	}
	encoded, err := json.Marshal(markers)
	if err != nil {
		return dbgen.InsertExecutionResultParams{}, fmt.Errorf("encode the markers: %w", err)
	}
	return dbgen.InsertExecutionResultParams{JobID: jobID, Status: string(r.Status), Origin: string(origin), Reason: optionalText(string(r.Reason)),
		ExecutorVersion: optionalText(r.ExecutorVersion), ExecutorSha256: optionalText(strings.TrimPrefix(r.ExecutorSHA256.String(), "sha256:")),
		ImageDigest: optionalText(r.ImageDigest.String()), Toolchain: optionalText(r.Toolchain), Runtime: optionalText(r.Runtime), StartedAt: timestamp(r.StartedAt),
		EndedAt: timestamp(r.EndedAt), ReceivedAt: timestamp(receivedAt), BodyDigest: optionalText(r.BodyDigest.String()), Markers: encoded, Submodules: stringList(r.Submodules), Unparsed: stringList(r.Unparsed)}, nil
}

func insertTestsParams(jobID int64, tests []execution.Test) []dbgen.InsertExecutionTestsParams {
	rows := make([]dbgen.InsertExecutionTestsParams, len(tests))
	for i, t := range tests {
		var base []string // NULL unless the test has base runs: a canonical test and an excluded one have none
		if len(t.BaseRuns) > 0 {
			base = t.BaseRuns
		}
		rows[i] = dbgen.InsertExecutionTestsParams{JobID: jobID, Package: t.Package, Name: t.Name, File: t.File, Kind: t.Kind, ExcludedReason: optionalText(t.ExcludedReason),
			HeadRuns: stringList(t.HeadRuns), BaseRuns: base, State: t.State}
	}
	return rows
}

// writeResult writes the results row and, for a complete result, its tests, through the transaction of the caller.
func writeResult(ctx context.Context, q *dbgen.Queries, jobID int64, origin execution.Origin, receivedAt time.Time, r execution.Report, tests []execution.Test) error {
	params, err := insertResultParams(jobID, origin, receivedAt, r)
	if err != nil {
		return err
	}
	if err := q.InsertExecutionResult(ctx, params); err != nil {
		return executionWriteError(fmt.Sprintf("write the result of job %d", jobID), err)
	}
	if len(tests) == 0 {
		return nil
	}
	if _, err := q.InsertExecutionTests(ctx, insertTestsParams(jobID, tests)); err != nil {
		return executionWriteError(fmt.Sprintf("write the tests of job %d", jobID), err)
	}
	return nil
}

// Complete implements execution.Store in one transaction: the job is locked, so that a result and a sweep never settle it twice, and
// the results row, the tests and the state are written together or not at all.
func (s executionStore) Complete(ctx context.Context, jobID int64, r execution.Result, now time.Time, githubWait time.Duration) (execution.Outcome, error) {
	if r.BodyDigest.IsZero() {
		return execution.Outcome{}, fmt.Errorf("%w: the body digest is required", execution.ErrInvalidRequest)
	}
	if err := execution.ValidateGitHubWait(githubWait); err != nil {
		return execution.Outcome{}, err
	}
	now = execution.Stored(now)
	var outcome execution.Outcome
	err := inTx(ctx, s.pool, "execution result", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		job, err := lockJob(ctx, q, jobID)
		if err != nil {
			return err
		}
		if job.State.Final() { // a result that was sent again, or one that arrives for a job that ended
			stored, err := q.GetExecutionResult(ctx, jobID)
			if err == nil && stored.BodyDigest.String == r.BodyDigest.String() {
				outcome = execution.Outcome{State: job.State, Replayed: true}
				return nil
			}
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("read the result of job %d: %w", jobID, err)
			}
			return fmt.Errorf("%w: job %d is %s", execution.ErrConflict, jobID, job.State)
		}
		if err := job.CheckLease(r.LeaseID, now); err != nil {
			return fmt.Errorf("%w: job %d", err, jobID)
		}
		if err := r.Validate(job.Plan); err != nil {
			return err
		}
		settled := job.Settle(r, now, githubWait)
		if err := storeJob(ctx, q, settled.Job); err != nil {
			return err
		}
		outcome = execution.Outcome{State: settled.Job.State}
		if !settled.Store {
			return nil
		}
		return writeResult(ctx, q, jobID, execution.OriginExecutor, now, r.Report, r.Tests)
	})
	return outcome, err
}

// GitHubFailure implements execution.Store.
func (s executionStore) GitHubFailure(ctx context.Context, jobID int64, now time.Time, githubWait time.Duration) (execution.State, error) {
	if err := execution.ValidateGitHubWait(githubWait); err != nil {
		return "", err
	}
	now = execution.Stored(now)
	var state execution.State
	err := inTx(ctx, s.pool, "execution GitHub failure", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		job, err := lockJob(ctx, q, jobID)
		if err != nil {
			return err
		}
		if job.State != execution.StatePending {
			return fmt.Errorf("%w: job %d is %s, not pending", execution.ErrInvalidRequest, jobID, job.State)
		}
		state, err = recordGitHubFailure(ctx, q, job, now, githubWait)
		return err
	})
	return state, err
}

// writeServiceResult writes the results row of a job that the service ended on the GitHub bound.
func writeServiceResult(ctx context.Context, q *dbgen.Queries, job execution.Job, now time.Time) error {
	row := execution.ServiceResult(job, now)
	return writeResult(ctx, q, job.ID, row.Origin, row.ReceivedAt, row.Report, nil)
}

// Cancel implements execution.Store.
func (s executionStore) Cancel(ctx context.Context, jobID int64, reason execution.Reason, now time.Time) (bool, error) {
	if err := execution.ValidateCancel(reason); err != nil {
		return false, err
	}
	changed := false
	err := inTx(ctx, s.pool, "execution cancel", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		job, err := lockJob(ctx, q, jobID)
		if err != nil || job.State.Final() {
			return err
		}
		changed = true
		return storeJob(ctx, q, job.Cancelled(reason, execution.Stored(now)))
	})
	return changed, err
}

// Expire implements execution.Store in one transaction over the open jobs that no lease holds.
func (s executionStore) Expire(ctx context.Context, p execution.ExpireParams) (int, error) {
	if err := p.Validate(); err != nil {
		return 0, err
	}
	now := execution.Stored(p.Now)
	changed := 0
	err := inTx(ctx, s.pool, "execution sweep", func(transaction pgx.Tx) error {
		q := dbgen.New(transaction)
		rows, err := q.OpenExecutionJobs(ctx)
		if err != nil {
			return fmt.Errorf("read the open jobs: %w", err)
		}
		jobs, err := jobsFromRows(rows)
		if err != nil {
			return err
		}
		for _, job := range jobs {
			next, did, serviceRow := job.Swept(p)
			if !did {
				continue
			}
			changed++
			if err := storeJob(ctx, q, next); err != nil {
				return err
			}
			if serviceRow {
				if err := writeServiceResult(ctx, q, next, now); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return changed, nil
}

// Job implements execution.Store.
func (s executionStore) Job(ctx context.Context, jobID int64) (execution.Job, bool, error) {
	row, err := dbgen.New(s.pool).GetExecutionJob(ctx, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Job{}, false, nil
	}
	if err != nil {
		return execution.Job{}, false, fmt.Errorf("read job %d: %w", jobID, err)
	}
	job, err := jobFromRow(row)
	return job, err == nil, err
}

// CurrentJob implements execution.Store.
func (s executionStore) CurrentJob(ctx context.Context, key string) (execution.Job, bool, error) {
	row, err := dbgen.New(s.pool).GetCurrentExecutionJob(ctx, key)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.Job{}, false, nil
	}
	if err != nil {
		return execution.Job{}, false, fmt.Errorf("read the current job of a key: %w", err)
	}
	job, err := jobFromRow(row)
	return job, err == nil, err
}

// HeadJobs implements execution.Store.
func (s executionStore) HeadJobs(ctx context.Context, repositoryID int64, pull int, headSHA string, plan execution.Plan) ([]execution.Job, error) {
	if err := execution.ValidatePull(repositoryID, pull); err != nil {
		return nil, err
	}
	number, err := int32Of(pull)
	if err != nil {
		return nil, err
	}
	rows, err := dbgen.New(s.pool).HeadExecutionJobs(ctx, dbgen.HeadExecutionJobsParams{RepositoryID: repositoryID, PullNumber: number, HeadSha: headSHA, Plan: string(plan)})
	if err != nil {
		return nil, fmt.Errorf("read the jobs of a head: %w", err)
	}
	return jobsFromRows(rows)
}

// OpenJobs implements execution.Store.
func (s executionStore) OpenJobs(ctx context.Context, repositoryID int64) ([]execution.Job, error) {
	if err := execution.ValidateRepository(repositoryID); err != nil {
		return nil, err
	}
	rows, err := dbgen.New(s.pool).RepositoryOpenExecutionJobs(ctx, repositoryID)
	if err != nil {
		return nil, fmt.Errorf("read the open jobs of a repository: %w", err)
	}
	return jobsFromRows(rows)
}

// parseHex is a digest column that holds the 64 hexadecimal digits of a SHA-256 without its prefix.
func parseHex(column string, value pgtype.Text) (artifact.Digest, error) {
	if !value.Valid {
		return artifact.Digest{}, nil
	}
	digest, err := artifact.ParseDigest("sha256:" + value.String)
	if err != nil {
		return artifact.Digest{}, fmt.Errorf("stored %s: %w", column, err)
	}
	return digest, nil
}

func parseDigestColumn(column string, value pgtype.Text) (artifact.Digest, error) {
	if !value.Valid {
		return artifact.Digest{}, nil
	}
	digest, err := artifact.ParseDigest(value.String)
	if err != nil {
		return artifact.Digest{}, fmt.Errorf("stored %s: %w", column, err)
	}
	return digest, nil
}

// Result implements execution.Store.
func (s executionStore) Result(ctx context.Context, jobID int64) (execution.StoredResult, bool, error) {
	row, err := dbgen.New(s.pool).GetExecutionResult(ctx, jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return execution.StoredResult{}, false, nil
	}
	if err != nil {
		return execution.StoredResult{}, false, fmt.Errorf("read the result of job %d: %w", jobID, err)
	}
	var markers []markerJSON
	if err := json.Unmarshal(row.Markers, &markers); err != nil {
		return execution.StoredResult{}, false, fmt.Errorf("read the result of job %d: stored markers: %w", jobID, err)
	}
	report := execution.Report{Status: execution.State(row.Status), Reason: execution.Reason(row.Reason.String), ExecutorVersion: row.ExecutorVersion.String, Toolchain: row.Toolchain.String,
		Runtime: row.Runtime.String, StartedAt: observedTime(row.StartedAt), EndedAt: observedTime(row.EndedAt), Submodules: row.Submodules, Unparsed: row.Unparsed}
	for _, m := range markers {
		report.Markers = append(report.Markers, execution.Marker(m))
	}
	if report.ExecutorSHA256, err = parseHex("executor_sha256", row.ExecutorSha256); err != nil {
		return execution.StoredResult{}, false, fmt.Errorf("read the result of job %d: %w", jobID, err)
	}
	if report.ImageDigest, err = parseDigestColumn("image_digest", row.ImageDigest); err != nil {
		return execution.StoredResult{}, false, fmt.Errorf("read the result of job %d: %w", jobID, err)
	}
	if report.BodyDigest, err = parseDigestColumn("body_digest", row.BodyDigest); err != nil {
		return execution.StoredResult{}, false, fmt.Errorf("read the result of job %d: %w", jobID, err)
	}
	return execution.StoredResult{JobID: jobID, Origin: execution.Origin(row.Origin), ReceivedAt: observedTime(row.ReceivedAt), Report: report}, true, nil
}

// Tests implements execution.Store.
func (s executionStore) Tests(ctx context.Context, jobID int64) ([]execution.Test, error) {
	rows, err := dbgen.New(s.pool).ListExecutionTests(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("read the tests of job %d: %w", jobID, err)
	}
	tests := make([]execution.Test, len(rows))
	for i, row := range rows {
		tests[i] = execution.Test{Package: row.Package, Name: row.Name, File: row.File, Kind: row.Kind, ExcludedReason: row.ExcludedReason.String,
			HeadRuns: row.HeadRuns, BaseRuns: row.BaseRuns, State: row.State}
	}
	return tests, nil
}

// Counts implements execution.Store.
func (s executionStore) Counts(ctx context.Context) (map[execution.State]int, error) {
	rows, err := dbgen.New(s.pool).CountExecutionJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("count the jobs: %w", err)
	}
	counts := map[execution.State]int{execution.StatePending: 0, execution.StateLeased: 0, execution.StateComplete: 0, execution.StateUnavailable: 0, execution.StateCancelled: 0}
	for _, row := range rows {
		counts[execution.State(row.State)] = int(row.Jobs)
	}
	return counts, nil
}
