package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
)

// The executor is seen when a lease request or a result post arrives and when it is answered, and a result post also when its body has been read
// (docs/contracts/runtime.md, "The executor is seen, not asked", amended M2.1b): a request that waits for the tree reads of a job, or a store
// that is slow to take a large result, is no silence of a live executor.

// momentStore is a store whose calls take time on the clock of the test and report what the executor looked like when each was reached.
type momentStore struct {
	execution.Store
	clock    *execClock
	liveness *execution.Liveness

	mu                       sync.Mutex
	seenAtJob, seenAtLease   time.Time // the last request the service had recorded when the handler reached the store
	leaseTakes, completeTake time.Duration
}

func (s *momentStore) seen() time.Time {
	at, _ := s.liveness.LastRequest()
	return at
}

func (s *momentStore) Job(ctx context.Context, id int64) (execution.Job, bool, error) {
	s.mu.Lock()
	s.seenAtJob = s.seen()
	s.mu.Unlock()
	return s.Store.Job(ctx, id)
}

func (s *momentStore) Lease(ctx context.Context, req execution.LeaseRequest, prepare execution.Prepare) (execution.Job, bool, error) {
	s.mu.Lock()
	s.seenAtLease = s.seen()
	s.mu.Unlock()
	s.clock.Advance(s.leaseTakes)
	return s.Store.Lease(ctx, req, prepare)
}

func (s *momentStore) Complete(ctx context.Context, id int64, r execution.Result, now time.Time, githubWait time.Duration) (execution.Outcome, error) {
	s.clock.Advance(s.completeTake)
	return s.Store.Complete(ctx, id, r, now, githubWait)
}

// timedBody is a request body that takes time to be read: it reports what the service had recorded when the handler first asked for it, and moves the
// clock when it is read to its end.
type timedBody struct {
	io.Reader
	e       *execTestEnv
	takes   time.Duration
	started bool
	atStart time.Time
	atEnd   time.Time
}

func (b *timedBody) Read(p []byte) (int, error) {
	if !b.started {
		b.started = true
		b.atStart, _ = b.e.liveness.LastRequest()
	}
	n, err := b.Reader.Read(p)
	if err == io.EOF {
		b.e.clock.Advance(b.takes)
		b.atEnd = b.e.clock.Now()
	}
	return n, err
}

// momentEnv is an API whose requests the test serves in-process, over a store whose calls take time.
func momentEnv(t *testing.T) (*execTestEnv, *momentStore, http.Handler) {
	t.Helper()
	var deps executorDeps
	var store *momentStore
	e := newExecEnv(t, func(d *executorDeps) {
		store = &momentStore{Store: d.store, liveness: d.liveness}
		d.store = store
		deps = *d
	})
	store.clock = e.clock
	handler, err := newExecutorAPI(deps)
	if err != nil {
		t.Fatal(err)
	}
	return e, store, handler
}

func serveDirect(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	req.Header.Set("Authorization", "Bearer "+execTestToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestLeaseIsRecordedWhenItArrivesAndAgainWhenItIsAnswered(t *testing.T) {
	e, store, handler := momentEnv(t)
	store.leaseTakes = 15 * time.Second // the tree reads of a job
	arrived := e.clock.Now()

	got := serveDirect(handler, httptest.NewRequest(http.MethodPost, "/lease", nil))

	if got.Code != http.StatusNoContent {
		t.Fatalf("lease = %d; want 204", got.Code)
	}
	if !store.seenAtLease.Equal(arrived) {
		t.Errorf("when the lease reached the store the executor was last seen at %v; want %v, the arrival", store.seenAtLease, arrived)
	}
	if at, _ := e.liveness.LastRequest(); !at.Equal(arrived.Add(15 * time.Second)) {
		t.Errorf("after the answer the executor was last seen at %v; want %v: the answer is recorded too", at, arrived.Add(15*time.Second))
	}
}

func TestResultIsRecordedWhenItArrivesWhenItsBodyIsReadAndWhenItIsAnswered(t *testing.T) {
	e, store, handler := momentEnv(t)
	store.completeTake = 10 * time.Second // the store of a large result
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	arrived := e.clock.Now()
	body := &timedBody{Reader: bytes.NewReader(execJSON(t, e.complete(leased))), e: e, takes: 10 * time.Second}
	req := httptest.NewRequest(http.MethodPost, e.jobPath(leased, "/result"), body)
	req.Header.Set("Suiteward-Lease", leased.LeaseID)

	got := serveDirect(handler, req)

	if got.Code != http.StatusOK {
		t.Fatalf("result = %d %s; want 200", got.Code, got.Body)
	}
	if !body.atStart.Equal(arrived) {
		t.Errorf("when its body was first read the executor was last seen at %v; want %v, the arrival", body.atStart, arrived)
	}
	if !store.seenAtJob.Equal(body.atEnd) || !body.atEnd.Equal(arrived.Add(10*time.Second)) {
		t.Errorf("when the job was read the executor was last seen at %v; want %v, when the body had been read", store.seenAtJob, arrived.Add(10*time.Second))
	}
	if at, _ := e.liveness.LastRequest(); !at.Equal(arrived.Add(20 * time.Second)) {
		t.Errorf("after the answer the executor was last seen at %v; want %v: the answer is recorded too", at, arrived.Add(20*time.Second))
	}
}

func TestAResultThatIsRefusedBeforeItsBodyIsReadCountsAsSeenToo(t *testing.T) {
	e, _, handler := momentEnv(t)
	arrived := e.clock.Now().Add(time.Minute)
	e.clock.Set(arrived)
	req := httptest.NewRequest(http.MethodPost, "/jobs/7/result", nil)
	req.ContentLength = 4<<20 + 1

	got := serveDirect(handler, req)

	if got.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("result = %d; want 413", got.Code)
	}
	if at, requested := e.liveness.LastRequest(); !requested || !at.Equal(arrived) {
		t.Errorf("the executor was last seen at %v (%v); want %v: a result post that is refused is still the executor", at, requested, arrived)
	}
}

func TestAResultWithAWrongTokenIsNotTheExecutor(t *testing.T) {
	e, _, handler := momentEnv(t)
	req := httptest.NewRequest(http.MethodPost, "/jobs/7/result", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if _, requested := e.liveness.LastRequest(); requested || rec.Code != http.StatusUnauthorized {
		t.Errorf("a result post with a wrong token: status %d, executor seen %v; want 401 and not seen", rec.Code, requested)
	}
}
