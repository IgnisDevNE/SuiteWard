package executiontest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
)

// Memory is an in-memory execution.Store with the semantics of the persistence contract. It records the ledger entries of RecordApproval
// and RecordUnblock in the commands.Store it is given, as the PostgreSQL store writes them to pr_commands. One mutex serializes every
// call, and each operation checks everything before its first write, so a failed call changes nothing. The lifecycle rules are the
// pure functions of the execution package, which the PostgreSQL store applies too.
type Memory struct {
	ledger commands.Store

	mu       sync.Mutex
	nextID   int64
	jobs     map[int64]execution.Job
	results  map[int64]execution.StoredResult
	tests    map[int64][]execution.Test
	gates    map[comment]execution.ApprovalGate
	unblocks map[comment]unblock
}

// comment is the key of a ledger entry.
type comment struct{ repository, comment int64 }

// unblock holds the rows of one unblock command: the jobs it lifted and when its ledger entry was created.
type unblock struct {
	jobs      []int64
	createdAt time.Time
}

var _ execution.Store = (*Memory)(nil)

// NewMemory returns an empty store that records its entries in ledger.
func NewMemory(ledger commands.Store) *Memory {
	return &Memory{ledger: ledger, jobs: map[int64]execution.Job{}, results: map[int64]execution.StoredResult{}, tests: map[int64][]execution.Test{},
		gates: map[comment]execution.ApprovalGate{}, unblocks: map[comment]unblock{}}
}

func clone(j execution.Job) execution.Job {
	j.UnapprovedPaths, j.ChangedPaths = slices.Clone(j.UnapprovedPaths), slices.Clone(j.ChangedPaths)
	return j
}

// Enqueue implements execution.Store.
func (m *Memory) Enqueue(ctx context.Context, n execution.NewJob) (execution.Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return execution.Job{}, false, err
	}
	if err := n.Validate(); err != nil {
		return execution.Job{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job := n.Job()
	for _, existing := range m.jobs {
		if existing.Key == job.Key && existing.Current() {
			return clone(existing), false, nil
		}
	}
	m.nextID++
	job.ID = m.nextID
	job.UnapprovedPaths, job.ChangedPaths = slices.Clone(job.UnapprovedPaths), slices.Clone(job.ChangedPaths)
	m.jobs[job.ID] = job
	return clone(job), true, nil
}

// oldestReady is the oldest job, by creation time and then id, that a lease may take or must first expire.
func (m *Memory) oldestReady(now time.Time) (execution.Job, bool) {
	var oldest execution.Job
	found := false
	for _, j := range m.jobs {
		if !j.Ready(now) && !j.LeaseExpired(now) {
			continue
		}
		if !found || cmp.Or(j.CreatedAt.Compare(oldest.CreatedAt), cmp.Compare(j.ID, oldest.ID)) < 0 {
			oldest, found = j, true
		}
	}
	return oldest, found
}

// Lease implements execution.Store.
func (m *Memory) Lease(ctx context.Context, req execution.LeaseRequest, prepare execution.Prepare) (execution.Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return execution.Job{}, false, err
	}
	if err := req.Validate(prepare); err != nil {
		return execution.Job{}, false, err
	}
	now := execution.Stored(req.Now)
	m.mu.Lock()
	defer m.mu.Unlock()
	before := maps.Clone(m.jobs) // a failure of prepare writes nothing, the expiry of earlier candidates included
	for {
		candidate, found := m.oldestReady(now)
		if !found {
			return execution.Job{}, false, nil
		}
		if candidate.State == execution.StateLeased { // an expired lease: a failure, then the next candidate
			m.jobs[candidate.ID] = candidate.LeaseLapsed(now)
			continue
		}
		switch err := prepare(ctx, clone(candidate)); {
		case err == nil:
			granted := candidate.Granted(req)
			m.jobs[granted.ID] = granted
			return clone(granted), true, nil
		case errors.Is(err, execution.ErrGitHubUnavailable):
			next, serviceRow := candidate.AfterGitHubFailure(now, req.GitHubWait)
			m.jobs[next.ID] = next
			if serviceRow {
				m.results[next.ID] = execution.ServiceResult(next, now)
			}
			return execution.Job{}, false, fmt.Errorf("%w: job %d: %w", execution.ErrGitHubUnavailable, next.ID, err)
		case errors.Is(err, execution.ErrTooLarge):
			m.jobs[candidate.ID] = candidate.TooLarge(now)
		default:
			m.jobs = before
			return execution.Job{}, false, err
		}
	}
}

func (m *Memory) open(id int64) (execution.Job, error) {
	job, ok := m.jobs[id]
	if !ok {
		return execution.Job{}, fmt.Errorf("%w: job %d", execution.ErrNotFound, id)
	}
	return job, nil
}

// Heartbeat implements execution.Store.
func (m *Memory) Heartbeat(ctx context.Context, jobID int64, leaseID string, now time.Time) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.open(jobID)
	if err != nil {
		return time.Time{}, err
	}
	next, err := job.Heartbeated(leaseID, execution.Stored(now))
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: job %d", err, jobID)
	}
	m.jobs[jobID] = next
	return next.LeaseExpiresAt, nil
}

// Complete implements execution.Store.
func (m *Memory) Complete(ctx context.Context, jobID int64, r execution.Result, now time.Time, githubWait time.Duration) (execution.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return execution.Outcome{}, err
	}
	if r.BodyDigest.IsZero() {
		return execution.Outcome{}, fmt.Errorf("%w: the body digest is required", execution.ErrInvalidRequest)
	}
	if err := execution.ValidateGitHubWait(githubWait); err != nil {
		return execution.Outcome{}, err
	}
	now = execution.Stored(now)
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.open(jobID)
	if err != nil {
		return execution.Outcome{}, err
	}
	if job.State.Final() { // a result that was sent again, or one that arrives for a job that ended
		if stored, ok := m.results[jobID]; ok && stored.BodyDigest == r.BodyDigest {
			return execution.Outcome{State: job.State, Replayed: true}, nil
		}
		return execution.Outcome{}, fmt.Errorf("%w: job %d is %s", execution.ErrConflict, jobID, job.State)
	}
	if err := job.CheckLease(r.LeaseID, now); err != nil {
		return execution.Outcome{}, fmt.Errorf("%w: job %d", err, jobID)
	}
	if err := r.Validate(job.Plan); err != nil {
		return execution.Outcome{}, err
	}
	settled := job.Settle(r, now, githubWait)
	m.jobs[jobID] = settled.Job
	if settled.Store {
		report := cloneReport(r.Report)
		report.StartedAt, report.EndedAt = execution.Stored(report.StartedAt), execution.Stored(report.EndedAt) // the microseconds PostgreSQL keeps
		m.results[jobID] = execution.StoredResult{JobID: jobID, Origin: execution.OriginExecutor, ReceivedAt: now, Report: report}
		if r.Status == execution.StateComplete {
			m.tests[jobID] = sortTests(r.Tests)
		}
	}
	return execution.Outcome{State: settled.Job.State}, nil
}

func cloneReport(r execution.Report) execution.Report {
	r.Markers, r.Submodules, r.Unparsed = slices.Clone(r.Markers), slices.Clone(r.Submodules), slices.Clone(r.Unparsed)
	return r
}

// sortTests copies the tests in the order they are read back in: by package, then name.
func sortTests(tests []execution.Test) []execution.Test {
	sorted := make([]execution.Test, len(tests))
	for i, t := range tests {
		t.HeadRuns, t.BaseRuns = slices.Clone(t.HeadRuns), slices.Clone(t.BaseRuns)
		sorted[i] = t
	}
	slices.SortFunc(sorted, func(a, b execution.Test) int {
		return cmp.Or(strings.Compare(a.Package, b.Package), strings.Compare(a.Name, b.Name))
	})
	return sorted
}

// GitHubFailure implements execution.Store.
func (m *Memory) GitHubFailure(ctx context.Context, jobID int64, now time.Time, githubWait time.Duration) (execution.State, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := execution.ValidateGitHubWait(githubWait); err != nil {
		return "", err
	}
	now = execution.Stored(now)
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.open(jobID)
	if err != nil {
		return "", err
	}
	if job.State != execution.StatePending {
		return "", fmt.Errorf("%w: job %d is %s, not pending", execution.ErrInvalidRequest, jobID, job.State)
	}
	next, serviceRow := job.AfterGitHubFailure(now, githubWait)
	m.jobs[jobID] = next
	if serviceRow {
		m.results[jobID] = execution.ServiceResult(next, now)
	}
	return next.State, nil
}

// Cancel implements execution.Store.
func (m *Memory) Cancel(ctx context.Context, jobID int64, reason execution.Reason, now time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := execution.ValidateCancel(reason); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.open(jobID)
	if err != nil {
		return false, err
	}
	if job.State.Final() {
		return false, nil
	}
	m.jobs[jobID] = job.Cancelled(reason, execution.Stored(now))
	return true, nil
}

// Expire implements execution.Store.
func (m *Memory) Expire(ctx context.Context, p execution.ExpireParams) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := p.Validate(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := 0
	for _, id := range m.sortedIDs() {
		job := m.jobs[id]
		if job.State.Final() {
			continue
		}
		next, did, serviceRow := job.Swept(p)
		if !did {
			continue
		}
		changed++
		m.jobs[id] = next
		if serviceRow {
			m.results[id] = execution.ServiceResult(next, execution.Stored(p.Now))
		}
	}
	return changed, nil
}

func (m *Memory) sortedIDs() []int64 {
	ids := make([]int64, 0, len(m.jobs))
	for id := range m.jobs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// Job implements execution.Store.
func (m *Memory) Job(ctx context.Context, jobID int64) (execution.Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return execution.Job{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[jobID]
	return clone(job), ok, nil
}

// CurrentJob implements execution.Store.
func (m *Memory) CurrentJob(ctx context.Context, key string) (execution.Job, bool, error) {
	if err := ctx.Err(); err != nil {
		return execution.Job{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		if j.Key == key && j.Current() {
			return clone(j), true, nil
		}
	}
	return execution.Job{}, false, nil
}

// HeadJobs implements execution.Store.
func (m *Memory) HeadJobs(ctx context.Context, repositoryID int64, pull int, headSHA string, plan execution.Plan) ([]execution.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := execution.ValidatePull(repositoryID, pull); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	jobs := []execution.Job{}
	for _, id := range m.sortedIDs() {
		if j := m.jobs[id]; j.RepositoryID == repositoryID && j.PullNumber == pull && j.HeadSHA == headSHA && j.Plan == plan {
			jobs = append(jobs, clone(j))
		}
	}
	return jobs, nil
}

// Result implements execution.Store.
func (m *Memory) Result(ctx context.Context, jobID int64) (execution.StoredResult, bool, error) {
	if err := ctx.Err(); err != nil {
		return execution.StoredResult{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.results[jobID]
	r.Report = cloneReport(r.Report)
	return r, ok, nil
}

// Tests implements execution.Store.
func (m *Memory) Tests(ctx context.Context, jobID int64) ([]execution.Test, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return sortTests(m.tests[jobID]), nil
}

// Counts implements execution.Store.
func (m *Memory) Counts(ctx context.Context) (map[execution.State]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := map[execution.State]int{execution.StatePending: 0, execution.StateLeased: 0, execution.StateComplete: 0, execution.StateUnavailable: 0, execution.StateCancelled: 0}
	for _, j := range m.jobs {
		counts[j.State]++
	}
	return counts, nil
}

// Unblocked implements execution.Store.
func (m *Memory) Unblocked(ctx context.Context, jobID int64, since time.Time) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.unblocks {
		if slices.Contains(u.jobs, jobID) && !u.createdAt.Before(since) {
			return true, nil
		}
	}
	return false, nil
}

// RecordApproval implements execution.Store.
func (m *Memory) RecordApproval(ctx context.Context, e commands.Entry, ack *governance.OutboxMessage, gate execution.ApprovalGate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if err := gate.Validate(); err != nil {
		return err
	}
	if e.Disposition != commands.Processed && e.Disposition != commands.ReceiptRefused {
		return fmt.Errorf("%w: the gate of an approve is recorded with processed or receipt-refused, not %s", execution.ErrInvalidRequest, e.Disposition)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, known := m.jobs[gate.JobID]; gate.JobID != 0 && !known {
		return fmt.Errorf("%w: the gate names job %d, which does not exist", execution.ErrInvalidRequest, gate.JobID)
	}
	if err := m.ledger.Record(ctx, e, ack); err != nil {
		return err
	}
	key := comment{e.RepositoryID, e.CommentID}
	if stored, ok := m.gates[key]; ok && stored != gate {
		return fmt.Errorf("%w: comment %d of repository %d has another approval receipt", commands.ErrConflict, e.CommentID, e.RepositoryID)
	}
	m.gates[key] = gate
	return nil
}

// RecordUnblock implements execution.Store.
func (m *Memory) RecordUnblock(ctx context.Context, e commands.Entry, ack *governance.OutboxMessage, headSHA string, jobIDs []int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if err := execution.ValidateUnblock(e, headSHA, jobIDs); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range jobIDs {
		if job, ok := m.jobs[id]; !ok || job.State != execution.StateUnavailable || job.RepositoryID != e.RepositoryID || job.PullNumber != e.PullNumber || job.HeadSHA != headSHA {
			return fmt.Errorf("%w: job %d is not an unavailable job of this head and pull request", execution.ErrInvalidRequest, id)
		}
	}
	if err := m.ledger.Record(ctx, e, ack); err != nil {
		return err
	}
	key := comment{e.RepositoryID, e.CommentID}
	lifted := slices.Sorted(slices.Values(jobIDs))
	if stored, ok := m.unblocks[key]; ok && !slices.Equal(stored.jobs, lifted) {
		return fmt.Errorf("%w: comment %d of repository %d unblocked other jobs", commands.ErrConflict, e.CommentID, e.RepositoryID)
	}
	m.unblocks[key] = unblock{jobs: lifted, createdAt: execution.Stored(e.CreatedAt)}
	return nil
}

// Gate returns the stored approval receipt of a comment.
func (m *Memory) Gate(ctx context.Context, repositoryID, commentID int64) (execution.ApprovalGate, bool, error) {
	if err := ctx.Err(); err != nil {
		return execution.ApprovalGate{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	gate, ok := m.gates[comment{repositoryID, commentID}]
	return gate, ok, nil
}

// Unblocks returns the jobs that the unblock rows of a comment name, sorted.
func (m *Memory) Unblocks(ctx context.Context, repositoryID, commentID int64) ([]int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.unblocks[comment{repositoryID, commentID}].jobs), nil
}

// ReceiptMemory is an in-memory execution.ReceiptLedger. One mutex serializes every call, and Publish checks everything before its first
// write, so a failed call changes nothing.
type ReceiptMemory struct {
	mu      sync.Mutex
	records map[pull]stored
	outbox  map[string]outboxEntry // key -> the message
}

type pull struct {
	repository int64
	number     int
}

// outboxEntry is an outbox message: failed says it ended failed, at finishedAt.
type outboxEntry struct {
	failed     bool
	finishedAt time.Time
}

// stored is a record with the key of its current generation's message.
type stored struct {
	record     execution.ReceiptRecord
	messageKey string
}

var _ execution.ReceiptLedger = (*ReceiptMemory)(nil)

// NewReceiptMemory returns an empty ledger.
func NewReceiptMemory() *ReceiptMemory {
	return &ReceiptMemory{records: map[pull]stored{}, outbox: map[string]outboxEntry{}}
}

// Current implements execution.ReceiptLedger.
func (m *ReceiptMemory) Current(ctx context.Context, repositoryID int64, number int) (execution.ReceiptRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return execution.ReceiptRecord{}, false, err
	}
	if err := execution.ValidatePull(repositoryID, number); err != nil {
		return execution.ReceiptRecord{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, found := m.records[pull{repositoryID, number}]
	return s.record, found, nil
}

// Publish implements execution.ReceiptLedger.
func (m *ReceiptMemory) Publish(ctx context.Context, r execution.ReceiptCheck, at time.Time, message func(generation int64) (governance.OutboxMessage, error)) (execution.ReceiptRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return execution.ReceiptRecord{}, false, err
	}
	if at.IsZero() {
		return execution.ReceiptRecord{}, false, fmt.Errorf("%w: the decision time is required", execution.ErrInvalidRequest)
	}
	if err := r.Validate(); err != nil {
		return execution.ReceiptRecord{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := pull{r.RepositoryID, r.PullNumber}
	current, found := m.records[key]
	var failedAt time.Time // zero: the current message did not fail
	if sent := m.outbox[current.messageKey]; found && sent.failed {
		failedAt = sent.finishedAt
	}
	next, advance := execution.NextReceipt(current.record, found, r, at, failedAt)
	messageKey := current.messageKey
	if advance {
		msg, err := message(next.Generation)
		if err != nil {
			return execution.ReceiptRecord{}, false, fmt.Errorf("build the check message: %w", err)
		}
		if err := msg.Validate(); err != nil {
			return execution.ReceiptRecord{}, false, err
		}
		if _, taken := m.outbox[msg.Key]; taken {
			return execution.ReceiptRecord{}, false, fmt.Errorf("%w: outbox message %q", governance.ErrOperationConflict, msg.Key)
		}
		m.outbox[msg.Key] = outboxEntry{}
		messageKey = msg.Key
	}
	m.records[key] = stored{record: next, messageKey: messageKey}
	return next, advance, nil
}

// OutboxKeys returns the key of every committed message, sorted.
func (m *ReceiptMemory) OutboxKeys(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.outbox))
	for key := range m.outbox {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, nil
}

// FailMessageAt marks a message as failed at finishedAt, as the relay does once its attempts are used up.
func (m *ReceiptMemory) FailMessageAt(ctx context.Context, key string, finishedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.outbox[key]; !ok {
		return fmt.Errorf("%w: outbox message %q", governance.ErrNotFound, key)
	}
	m.outbox[key] = outboxEntry{failed: true, finishedAt: finishedAt}
	return nil
}
