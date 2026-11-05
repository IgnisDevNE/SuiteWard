package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

const libTests = "package m\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n\nfunc TestB(t *testing.T) {}\n"

// aJob is a canonical job with two declared tests, and the runtime that passes them.
func aJob(id int64) (*served, *fakeRuntime) {
	s := canonicalScenario()
	s.head = repo{"go.mod": goMod, "lib.go": "package m\n"}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": libTests}
	return s.serve(id), newRuntime(func(c call) (sandbox.Outcome, error) {
		return goTest(c, func(string, string) string { return "pass" }), nil
	})
}

func TestAnEmptyLeaseIsFollowedByAWaitOfFiveSeconds(t *testing.T) {
	svc := newService(t)
	d := start(t, svc, newRuntime(nil), func(_ *Config, d *driver) { d.sleeps.immediate, d.sleeps.reached = 3, make(chan struct{}) })
	wait(d, d.sleeps.reached)
	if got := d.sleeps.durations(); !slices.Equal(got[:3], []time.Duration{5 * time.Second, 5 * time.Second, 5 * time.Second}) {
		t.Errorf("the waits after a 204 were %v; want 5 s each", got)
	}
	if n := svc.leaseRequests(); n < 4 {
		t.Errorf("%d lease requests; the executor asks again after each wait", n)
	}
}

// 502 is the answer when GitHub could not be read for the job; a refused token, a service that is not ready, a failure and an answer that
// breaks the contract are errors too. None of them is a reason to ask faster.
func TestAfterAnErrorTheLeaseWaitDoublesUpToTwentySecondsAndStartsAgain(t *testing.T) {
	broken := &served{job: client.Job{JobID: 99}} // a job that fails the contract's checks: it has no lease
	svc := newService(t, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusUnauthorized, http.StatusInternalServerError,
		http.StatusBadGateway, broken, http.StatusBadGateway, nil, http.StatusBadGateway, nil)
	d := start(t, svc, newRuntime(nil), func(_ *Config, d *driver) { d.sleeps.immediate, d.sleeps.reached = 10, make(chan struct{}) })
	wait(d, d.sleeps.reached)
	want := []time.Duration{5, 10, 20, 20, 20, 20, 20, 5, 5, 5} // seconds: the 204s wait 5 and an answer starts the backoff again
	for i := range want {
		want[i] *= time.Second
	}
	if got := d.sleeps.durations(); !slices.Equal(got[:10], want) {
		t.Errorf("the waits were %v; want %v", got[:10], want)
	}
}

func TestAHeartbeatGoesAboutEveryTenSecondsWhileTheJobRuns(t *testing.T) {
	sv, rt := aJob(1)
	beats := make(chan int, 8)
	sv.beat = func(n int) int { beats <- n; return http.StatusOK }
	rt.gate, rt.entered = make(chan struct{}), make(chan struct{}, 4)
	d := start(t, newService(t, sv), rt)
	wait(d, rt.entered)
	if every := wait(d, d.tickFor); every != 10*time.Second {
		t.Errorf("the heartbeat ticks every %v; want 10 s", every)
	}
	d.tick()
	d.tick()
	if a, b := wait(d, beats), wait(d, beats); a != 1 || b != 2 {
		t.Errorf("heartbeats %d and %d; want 1 and 2", a, b)
	}
	rt.gate <- struct{}{}
	if p := d.result(); p.lease != sv.job.LeaseID {
		t.Errorf("the result carries the lease %q; want %q", p.lease, sv.job.LeaseID)
	}
}

func TestAConflictOnAHeartbeatStopsTheWorkAndPostsNothing(t *testing.T) {
	for name, status := range map[string]int{"the lease was lost": http.StatusConflict, "the job is unknown": http.StatusNotFound} {
		sv, rt := aJob(1)
		sv.beat = func(int) int { return status }
		rt.gate, rt.entered = make(chan struct{}), make(chan struct{}, 4)
		svc := newService(t, sv)
		d := start(t, svc, rt, func(_ *Config, d *driver) { d.sleeps.reached = make(chan struct{}) })
		wait(d, rt.entered)
		d.tick()
		wait(d, d.sleeps.reached) // the work was dropped, and the executor asked for the next job, which was a 204
		d.noResult()
		if svc.leaseRequests() < 2 {
			t.Errorf("%s: the executor did not ask for another job", name)
		}
	}
}

func TestAHeartbeatThatFailsOtherwiseDoesNotStopTheWork(t *testing.T) {
	sv, rt := aJob(1)
	sv.beat = func(int) int { return http.StatusInternalServerError }
	rt.gate, rt.entered = make(chan struct{}), make(chan struct{}, 4)
	d := start(t, newService(t, sv), rt)
	wait(d, rt.entered)
	d.tick()
	rt.gate <- struct{}{}
	d.result()
}

// A lease that is already past its deadline less the margin never runs: it is dropped before the attempt starts, with the deadline as the
// cause, nothing is posted, and the executor goes on to the next job. The test waits for the drop, or for a post, and then for the executor to
// ask again, so that a result that is still on its way cannot slip past a check.
func TestAJobPastItsDeadlineIsDroppedAtOnceAndNeverRuns(t *testing.T) {
	s := withTests()
	s.deadline = time.Now().Add(-time.Minute)
	sv, rt := s.serve(1), newRuntime(passing)
	logs := newLogRecorder()
	d := start(t, newService(t, sv), rt, func(cfg *Config, d *driver) {
		cfg.Log = slog.New(logs)
		d.sleeps.reached, d.patience = make(chan struct{}), 10*time.Second
	})
	rec := d.droppedOrPosted(logs)
	if cause, _ := rec.attrs["cause"].(error); !errors.Is(cause, context.DeadlineExceeded) {
		t.Errorf("the work was dropped with the cause %v; want the deadline", rec.attrs["cause"])
	}
	wait(d, d.sleeps.reached) // the executor handled the job and asked for the next one, which was a 204
	d.noResult()
	if n := sv.results.Load(); n != 0 {
		t.Errorf("%d results were posted for a lease past its deadline", n)
	}
	if n := rt.probeCount() + len(rt.invocations()); n != 0 {
		t.Errorf("%d containers ran for a lease past its deadline", n)
	}
}

func TestTheResultIsPostedOnceWhateverTheServiceAnswers(t *testing.T) {
	for name, answer := range map[string]func(int) (int, string){
		"pending":  func(int) (int, string) { return http.StatusOK, client.StatePending },
		"conflict": func(int) (int, string) { return http.StatusConflict, "" },
		"failure":  func(int) (int, string) { return http.StatusInternalServerError, "" },
		"refusal":  func(int) (int, string) { return http.StatusBadRequest, "" },
	} {
		sv, rt := aJob(1)
		sv.answer = answer
		svc := newService(t, sv)
		d := start(t, svc, rt, func(_ *Config, d *driver) { d.sleeps.reached = make(chan struct{}) })
		d.result()
		wait(d, d.sleeps.reached) // the executor went on to ask for a job
		if n := sv.results.Load(); n != 1 {
			t.Errorf("%s: the result was posted %d times; want once", name, n)
		}
	}
}

func TestAfterAPendingAnswerTheExecutorLeasesTheNextJob(t *testing.T) {
	first, rt := aJob(1)
	first.answer = func(int) (int, string) { return http.StatusOK, client.StatePending }
	second, _ := aJob(2)
	d := start(t, newService(t, first, second), rt)
	if got := d.result().id; got != 1 {
		t.Errorf("the first result is of job %d", got)
	}
	if got := d.result().id; got != 2 {
		t.Errorf("the second result is of job %d", got)
	}
}

func TestOnShutdownTheExecutorLeasesNoMoreAndFinishesTheJobInFlight(t *testing.T) {
	sv, rt := aJob(1)
	rt.gate, rt.entered = make(chan struct{}), make(chan struct{}, 4)
	svc := newService(t, sv, nil, nil)
	d := start(t, svc, rt)
	wait(d, rt.entered)
	d.cancel()
	rt.gate <- struct{}{} // the job in flight is still allowed to end, within the grace
	d.result()
	if err := d.stopped(); err != nil {
		t.Errorf("Run = %v after a clean stop; want nil", err)
	}
	if n := svc.leaseRequests(); n != 1 {
		t.Errorf("%d lease requests; the executor stops leasing at the signal", n)
	}
	d.sleeps.assertWaits(t, 1, 1, "the job in flight waits once for its deadline and once, after the signal, for the grace")
}

// The grace is 30 s from the signal. When it is over the work is dropped (the sandbox kills the container with its context), nothing is
// posted, the executor exits 0, and the lease expires, so that the job is leased again. The test ends the wait for the grace at once.
func TestAJobInFlightIsAbandonedWhenTheGraceAfterTheSignalIsOver(t *testing.T) {
	sv, rt := aJob(1)
	rt.gate, rt.entered = make(chan struct{}), make(chan struct{}, 4) // the gate is never opened: the job would run for ever
	svc := newService(t, sv, nil, nil)
	d := start(t, svc, rt, gate(graceWait, closed()), func(_ *Config, d *driver) { d.patience = 5 * time.Second })
	wait(d, rt.entered)
	d.cancel()
	if err := d.stopped(); err != nil {
		t.Errorf("Run = %v after the grace; want a clean stop", err)
	}
	d.noResult()
	if n := sv.results.Load(); n != 0 {
		t.Errorf("%d results were posted by a job that was abandoned", n)
	}
	d.sleeps.assertWaits(t, 1, 1, "the job in flight waits once for its deadline and once, after the signal, for the grace")
	if n := svc.leaseRequests(); n != 1 {
		t.Errorf("%d lease requests; the executor leases nothing after the signal", n)
	}
}

func TestShutdownWhileWaitingStopsAtOnce(t *testing.T) {
	d := start(t, newService(t), newRuntime(nil), func(_ *Config, d *driver) { d.sleeps.reached = make(chan struct{}) })
	wait(d, d.sleeps.reached)
	d.cancel()
	if err := d.stopped(); err != nil {
		t.Errorf("Run = %v after a clean stop; want nil", err)
	}
}

// infoRuntime is a runtime whose Info fails the first fail times, as a container program that is not running does, and then answers with
// name and version. The other methods are the fake runtime's.
type infoRuntime struct {
	*fakeRuntime
	fail          int
	name, version string

	mu    sync.Mutex
	asked int
}

func (r *infoRuntime) Info(context.Context) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked++
	if r.asked <= r.fail {
		return "", "", fmt.Errorf("Cannot connect to Podman (the machine is not running, attempt %d)", r.asked)
	}
	return r.name, r.version, nil
}

func (r *infoRuntime) timesAsked() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.asked
}

// The plain words of the two state changes: the executor says each once, however many times it asks.
const (
	waitingLine = "The container program does not answer. The test runner waits for it and tries again."
	answersLine = "The container program answers again."
)

// logged is the messages that the recorder holds so far, in order.
func logged(logs *logRecorder) []string {
	var msgs []string
	for {
		select {
		case rec := <-logs.records:
			msgs = append(msgs, rec.msg)
		default:
			return msgs
		}
	}
}

func TestWhileTheContainerProgramDoesNotAnswerTheExecutorWaitsLongerEachTimeAndThenGoesOn(t *testing.T) {
	rt := &infoRuntime{fakeRuntime: newRuntime(nil), fail: 4, name: "podman", version: "5.8.2"}
	svc := newService(t)
	// The four waits after a failed Info return at once; the fifth wait, the 5 s after the first lease that finds no job, is where the executor rests.
	d := start(t, svc, rt, func(_ *Config, d *driver) { d.sleeps.immediate, d.sleeps.reached = 4, make(chan struct{}) })
	wait(d, d.sleeps.reached)
	want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 20 * time.Second, 5 * time.Second}
	if got := d.sleeps.durations(); !slices.Equal(got, want) {
		t.Errorf("the waits were %v; want %v: 5 s, doubling to at most 20 s while the program is down, then the 5 s of an empty lease", got, want)
	}
	if n := rt.timesAsked(); n != 5 {
		t.Errorf("the program was asked %d times; want 5, once after each wait", n)
	}
	if n := d.swept.Load(); n != 1 {
		t.Errorf("the sweep ran %d times; want once, when the program answered", n)
	}
	if n := svc.leaseRequests(); n < 1 {
		t.Errorf("%d lease requests; the executor leases once the program answers", n)
	}
}

func TestTheExecutorSaysOnceThatItWaitsForTheContainerProgramAndOnceThatItAnswers(t *testing.T) {
	logs := newLogRecorder()
	rt := &infoRuntime{fakeRuntime: newRuntime(nil), fail: 3, name: "podman", version: "5.8.2"}
	d := start(t, newService(t), rt, func(cfg *Config, d *driver) {
		cfg.Log = slog.New(logs)
		d.sleeps.immediate, d.sleeps.reached = 3, make(chan struct{})
	})
	wait(d, d.sleeps.reached)
	msgs := logged(logs)
	index := func(msg string) int { return slices.Index(msgs, msg) }
	waiting, answers, runs := index(waitingLine), index(answersLine), index("the executor runs")
	if waiting < 0 || answers < waiting || runs < answers {
		t.Errorf("the lines were %q; want the waiting line, then the answering line, then the one that says the executor runs", msgs)
	}
	for _, msg := range []string{waitingLine, answersLine} {
		if n := strings.Count(strings.Join(msgs, "\n"), msg); n != 1 {
			t.Errorf("%q was logged %d times over 3 failed attempts; want once", msg, n)
		}
	}
}

func TestTheWaitingLineCarriesTheError(t *testing.T) {
	logs := newLogRecorder()
	rt := &infoRuntime{fakeRuntime: newRuntime(nil), fail: 1 << 30}
	d := start(t, newService(t), rt, func(cfg *Config, d *driver) { cfg.Log = slog.New(logs) })
	for {
		select {
		case rec := <-logs.records:
			if rec.msg != waitingLine {
				continue
			}
			if err, _ := rec.attrs["error"].(error); err == nil || !strings.Contains(err.Error(), "Cannot connect to Podman") {
				t.Errorf("the waiting line carries the error %v; want the program's own", rec.attrs["error"])
			}
			return
		case err := <-d.done:
			d.done <- err
			t.Fatalf("the executor stopped (%v) before it said that it waits", err)
		case <-time.After(d.patience):
			t.Fatal("the executor never said that it waits")
		}
	}
}

func TestWithoutAContainerProgramTheExecutorWaitsAndEndsWithNilAtOnceAtTheSignal(t *testing.T) {
	rt := newRuntime(nil)
	rt.infoErr = errors.New("the engine does not answer")
	svc := newService(t, nil)
	d := start(t, svc, rt, func(_ *Config, d *driver) { d.sleeps.reached = make(chan struct{}) }) // the first wait blocks until the context ends
	wait(d, d.sleeps.reached)
	d.cancel()
	if err := d.stopped(); err != nil {
		t.Errorf("Run = %v after a signal while it waited for the container program; want a clean stop", err)
	}
	if n := svc.leaseRequests(); n != 0 {
		t.Errorf("%d lease requests without a runtime", n)
	}
	if n := d.swept.Load(); n != 0 {
		t.Errorf("the sweep ran %d times without a runtime", n)
	}
	if got := d.sleeps.durations(); !slices.Equal(got, []time.Duration{5 * time.Second}) {
		t.Errorf("the waits were %v; want the one of 5 s, which the signal ended", got)
	}
}

// askingRuntime's Info tells that it was asked and then waits for the end of its context, as the CLI's command is killed with its context.
type askingRuntime struct {
	*fakeRuntime
	asking chan struct{}
}

func (r askingRuntime) Info(ctx context.Context) (string, string, error) {
	r.asking <- struct{}{}
	<-ctx.Done()
	return "", "", ctx.Err()
}

func TestASignalWhileTheContainerProgramIsAskedEndsRunWithoutAWaitingLine(t *testing.T) {
	logs := newLogRecorder()
	rt := askingRuntime{fakeRuntime: newRuntime(nil), asking: make(chan struct{}, 1)}
	d := start(t, newService(t), rt, func(cfg *Config, _ *driver) { cfg.Log = slog.New(logs) })
	wait(d, rt.asking)
	d.cancel()
	if err := d.stopped(); err != nil {
		t.Errorf("Run = %v after a signal; want a clean stop", err)
	}
	if slices.Contains(logged(logs), waitingLine) {
		t.Error("the executor said that it waits for the container program, when the signal had come")
	}
}

// The signal that ends a wait ends Run there: the executor does not ask the program once more, and does not go on to sweep and lease, even with
// a program that would answer.
func TestASignalDuringTheWaitEndsRunBeforeTheProgramIsAskedAgain(t *testing.T) {
	logs := newLogRecorder()
	rt := &infoRuntime{fakeRuntime: newRuntime(nil), fail: 1, name: "podman", version: "5.8.2"}
	svc := newService(t, nil)
	d := start(t, svc, rt, func(cfg *Config, d *driver) {
		cfg.Log = slog.New(logs)
		d.sleeps.reached = make(chan struct{})
	})
	wait(d, d.sleeps.reached)
	d.cancel()
	if err := d.stopped(); err != nil {
		t.Errorf("Run = %v after a signal during the wait; want a clean stop", err)
	}
	if n := rt.timesAsked(); n != 1 {
		t.Errorf("the program was asked %d times; want once, the signal ended the wait before a second ask", n)
	}
	if slices.Contains(logged(logs), answersLine) || d.swept.Load() != 0 || svc.leaseRequests() != 0 {
		t.Error("the executor went on after the signal that ended its wait")
	}
}

// A runtime that answers with a name the results cannot carry is not a program that is down: waiting does not help, so the executor stops, whether
// it asked once or had to wait first.
func TestARuntimeNameThatIsNotPrintableIsStillFatal(t *testing.T) {
	for fail := range 2 {
		rt := &infoRuntime{fakeRuntime: newRuntime(nil), fail: fail, name: "pod\x00man", version: "5.8.2"}
		svc := newService(t, nil)
		d := start(t, svc, rt, func(_ *Config, d *driver) { d.sleeps.immediate = 1 })
		if err := d.stopped(); err == nil || !strings.Contains(err.Error(), "container runtime") || !strings.Contains(err.Error(), "printable") {
			t.Errorf("after %d failed attempts Run = %v; want a fatal error that says the name is not printable", fail, err)
		}
		if n := svc.leaseRequests(); n != 0 {
			t.Errorf("after %d failed attempts: %d lease requests with a name that cannot be reported", fail, n)
		}
	}
}

func TestLeftoverContainersAreSweptOnceBeforeTheFirstLease(t *testing.T) {
	svc := newService(t)
	d := start(t, svc, newRuntime(nil), func(_ *Config, d *driver) {
		d.sleeps.reached = make(chan struct{})
	})
	wait(d, d.sleeps.reached)
	if n := d.swept.Load(); n != 1 {
		t.Errorf("the sweep ran %d times; want once, before the first lease", n)
	}
}

func TestAFailedSweepIsLoggedAndTheExecutorGoesOn(t *testing.T) {
	d := start(t, newService(t), newRuntime(nil), func(cfg *Config, d *driver) {
		d.sleeps.reached = make(chan struct{})
		cfg.Sweep = func(context.Context) error { return errors.New("the engine would not list") }
	})
	wait(d, d.sleeps.reached) // fails the test when the executor stopped after the failed sweep
}

func TestNewChecksWhatItIsGiven(t *testing.T) {
	api, err := client.New("http://127.0.0.1:1", testToken)
	if err != nil {
		t.Fatal(err)
	}
	good := Config{API: api, Runtime: newRuntime(nil), Sweep: func(context.Context) error { return nil }, Version: "1.2.3", BinarySHA256: testBinary, Log: discardLog()}
	if _, err := New(good); err != nil {
		t.Fatalf("New(good) = %v", err)
	}
	for name, change := range map[string]func(*Config){
		"no API":                  func(c *Config) { c.API = nil },
		"no runtime":              func(c *Config) { c.Runtime = nil },
		"no sweep":                func(c *Config) { c.Sweep = nil },
		"no logger":               func(c *Config) { c.Log = nil },
		"a version of no shape":   func(c *Config) { c.Version = "not a version" },
		"no version":              func(c *Config) { c.Version = "" },
		"a binary digest in caps": func(c *Config) { c.BinarySHA256 = "ABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABABAB" },
		"a short binary digest":   func(c *Config) { c.BinarySHA256 = "abab" },
	} {
		cfg := good
		change(&cfg)
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: New accepted the configuration", name)
		}
	}
}

func TestTheDefaultsReadNoProxySettingAndFollowTheContract(t *testing.T) {
	api, err := client.New("http://127.0.0.1:1", testToken)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{API: api, Runtime: newRuntime(nil), Sweep: func(context.Context) error { return nil }, Version: "1.2.3", BinarySHA256: testBinary, Log: discardLog()})
	if err != nil {
		t.Fatal(err)
	}
	if e.cfg.Pacing != DefaultPacing || e.cfg.Proxy.Base != "https://proxy.golang.org" {
		t.Errorf("the defaults are %+v and %q", e.cfg.Pacing, e.cfg.Proxy.Base)
	}
	if e.cfg.Proxy.Client == nil {
		t.Fatal("no default client for the module proxy")
	}
	transport, ok := e.cfg.Proxy.Client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil {
		t.Errorf("the prefetch client is %T and has a proxy function: the egress goes direct, and no environment variable moves it", e.cfg.Proxy.Client.Transport)
	}
	if DefaultPacing != (Pacing{Idle: 5 * time.Second, Backoff: 5 * time.Second, MaxBackoff: 20 * time.Second, Heartbeat: 10 * time.Second, Grace: 30 * time.Second, Margin: 30 * time.Second}) {
		t.Errorf("DefaultPacing = %+v", DefaultPacing)
	}
}
