//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

func TestServeMigratesAFreshDatabaseAndReports(t *testing.T) {
	databaseURL := newDatabase(t)
	s := startRun(t, serveVars(t, databaseURL))
	s.waitReady()

	_, body := s.get("/readyz")
	if want := `{"schemaVersion":` + strconv.FormatInt(migrations.SupportedVersion, 10) + `,"status":"ready"}`; body != want {
		t.Fatalf("GET /readyz = %s; want %s", body, want)
	}
	if code, body := s.get("/healthz"); code != http.StatusOK || body != `{"status":"ok"}` {
		t.Fatalf("GET /healthz = %d %s", code, body)
	}
	status := s.status()
	if status["version"] != Version || status["schemaVersion"] != float64(migrations.SupportedVersion) {
		t.Fatalf("GET /status = %v; want version %q and schema version %d", status, Version, migrations.SupportedVersion)
	}
	zeroJobs := map[string]any{"available": 0.0, "running": 0.0, "retryable": 0.0, "scheduled": 0.0, "completed": 0.0, "discarded": 0.0}
	zeroOutbox := map[string]any{"pending": 0.0, "delivered": 0.0, "failed": 0.0}
	if !reflect.DeepEqual(status["jobs"], zeroJobs) || !reflect.DeepEqual(status["outbox"], zeroOutbox) {
		t.Fatalf("GET /status counts = %v and %v; want all zero (the relay job is not counted)", status["jobs"], status["outbox"])
	}
	_, raw := s.get("/status")
	if strings.Contains(raw, password(t, databaseURL)) || strings.Contains(raw, "postgres") {
		t.Fatalf("GET /status exposes database settings: %s", raw)
	}
	if code, _ := s.get("/metrics"); code != http.StatusNotFound {
		t.Fatalf("GET /metrics = %d; want 404", code)
	}

	started := time.Now()
	if code := s.stop(); code != 0 {
		t.Fatalf("a clean stop exited %d; want 0:\n%s", code, s.logs.String())
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("an idle service took %v to stop", elapsed)
	}
	// The shutdown steps run in the order of the runtime contract.
	last := -1
	for _, step := range []string{"shutting down", "http server stopped", "job runtime stopped", "database pool closed"} {
		entry, position := s.logs.find(step)
		if entry == nil || position <= last {
			t.Fatalf("shutdown step %q is missing or out of order (position %d after %d):\n%s", step, position, last, s.logs.String())
		}
		last = position
	}
}

func TestServeWarnsAboutUnrecognizedSettings(t *testing.T) {
	vars := serveVars(t, newDatabase(t))
	vars["SUITEWARD_HTTP_ADRESS"] = "typo"
	vars["SUITEWARD_TEST_ANYTHING"] = "set by the development tooling"
	s := startRun(t, vars)
	s.waitReady()
	entry, _ := s.logs.find("unrecognized setting")
	if entry == nil || entry["level"] != "WARN" || entry["name"] != "SUITEWARD_HTTP_ADRESS" {
		t.Fatalf("the typo was not logged as a warning: %v\n%s", entry, s.logs.String())
	}
	if strings.Contains(s.logs.String(), "SUITEWARD_TEST_ANYTHING") {
		t.Fatalf("a development-tooling variable was reported as unrecognized:\n%s", s.logs.String())
	}
}

func TestProbeCompletesThroughTheRunningService(t *testing.T) {
	databaseURL := newDatabase(t)
	s := startRun(t, serveVars(t, databaseURL))
	s.waitReady()

	code, stdout, stderr := run([]string{"probe", "--timeout", "30s"}, s.env())
	if code != 0 {
		t.Fatalf("probe exited %d; stdout %q stderr %q", code, stdout, stderr)
	}
	lines := jsonLines(t, stdout)
	if len(lines) != 2 || lines[0]["probeId"] == "" || lines[0]["probeId"] != lines[1]["probeId"] {
		t.Fatalf("probe printed %q; want two JSON lines with the same probeId", stdout)
	}
	if lines[1]["job"] != "completed" || lines[1]["outbox"] != "delivered" || lines[1]["elapsedMs"] == nil {
		t.Fatalf("final probe line = %v; want a completed job and a delivered message", lines[1])
	}
	status := s.status()
	if jobs := status["jobs"].(map[string]any); jobs["completed"] != 1.0 {
		t.Fatalf("GET /status jobs = %v; want the probe completed", jobs)
	}
	if outbox := status["outbox"].(map[string]any); outbox["delivered"] != 1.0 {
		t.Fatalf("GET /status outbox = %v; want the probe message delivered", outbox)
	}
	for _, event := range []string{"probe job ran", "probe message published"} {
		if entry, _ := s.logs.find(event); entry == nil {
			t.Errorf("the service did not log %q", event)
		}
	}
}

func TestPendingWorkSurvivesARestart(t *testing.T) {
	databaseURL := newDatabase(t)
	vars := serveVars(t, databaseURL)
	first := startRun(t, vars)
	first.waitReady()
	code, stdout, stderr := run([]string{"probe", "--delay", "10s", "--no-wait"}, first.env())
	if code != 0 {
		t.Fatalf("probe --no-wait exited %d: %q %q", code, stdout, stderr)
	}
	lines := jsonLines(t, stdout)
	if len(lines) != 1 {
		t.Fatalf("probe --no-wait printed %q; want exactly one JSON line", stdout)
	}
	id, _ := lines[0]["probeId"].(string)
	if code := first.stop(); code != 0 {
		t.Fatalf("the first service exited %d", code)
	}
	jobID, _, _ := strings.Cut(id, ":")
	if state := scalar(t, databaseURL, "SELECT state::text FROM river_job WHERE id = $1::bigint", jobID); state != "scheduled" {
		t.Fatalf("the delayed probe is %q after the stop; want it still scheduled", state)
	}

	second := startRun(t, vars)
	second.waitReady()
	code, stdout, stderr = run([]string{"probe", "--wait", id, "--timeout", "90s"}, second.env())
	if code != 0 {
		t.Fatalf("the pending probe did not complete after the restart: exit %d, %q %q", code, stdout, stderr)
	}
}

func TestShutdownLetsRunningJobsFinish(t *testing.T) {
	databaseURL := newDatabase(t)
	started, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	s := startServe(t, serveVars(t, databaseURL), map[string]river.Handler{"slow": func(ctx context.Context, _ json.RawMessage) error {
		close(started)
		<-release
		finished <- ctx.Err() // a soft stop leaves the context of a running job alone
		return nil
	}})
	s.waitReady()
	jobID := enqueue(t, databaseURL, "slow", 0)
	waitChan(t, started, "the job to start")

	stopped := make(chan int, 1)
	go func() { stopped <- s.stop() }()
	waitFor(t, "the shutdown to begin", 10*time.Second, func() bool { entry, _ := s.logs.find("shutting down"); return entry != nil })
	select {
	case code := <-stopped:
		t.Fatalf("the service stopped (%d) while a job still ran", code)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("the running job saw its context canceled by a soft stop: %v", err)
	}
	if code := <-stopped; code != 0 {
		t.Fatalf("a graceful stop exited %d; want 0:\n%s", code, s.logs.String())
	}
	if state := scalar(t, databaseURL, "SELECT state::text FROM river_job WHERE id = $1::bigint", strconv.FormatInt(jobID, 10)); state != "completed" {
		t.Fatalf("the job is %q after the stop; want completed", state)
	}
}

func TestShutdownForcesAJobThatOutlastsTheTimeout(t *testing.T) {
	databaseURL := newDatabase(t)
	vars := serveVars(t, databaseURL)
	vars["SUITEWARD_SHUTDOWN_TIMEOUT"] = "1s"
	started := make(chan struct{})
	s := startServe(t, vars, map[string]river.Handler{"stuck": func(ctx context.Context, _ json.RawMessage) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}})
	s.waitReady()
	enqueue(t, databaseURL, "stuck", 0)
	waitChan(t, started, "the job to start")

	begin := time.Now()
	if code := s.stop(); code != 1 {
		t.Fatalf("a forced stop exited %d; want 1:\n%s", code, s.logs.String())
	}
	if elapsed := time.Since(begin); elapsed > 3*time.Second {
		t.Fatalf("the forced stop took %v with a 1s timeout", elapsed)
	}
	if entry, _ := s.logs.find("job runtime did not stop in time; cancelling running jobs"); entry == nil {
		t.Fatalf("the forced stop was not logged:\n%s", s.logs.String())
	}
	if entry, _ := s.logs.find("database pool closed"); entry == nil {
		t.Fatalf("the pool was not closed after the forced stop:\n%s", s.logs.String())
	}
}

func TestReadinessFollowsTheDatabase(t *testing.T) {
	databaseURL := newDatabase(t)
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	proxied := newProxy(t, parsed.Host)
	parsed.Host = proxied.listener.Addr().String()
	s := startRun(t, serveVars(t, parsed.String()))
	s.waitReady()

	proxied.cut()
	waitFor(t, "readiness to fail", 30*time.Second, func() bool { code, _ := s.get("/readyz"); return code == http.StatusServiceUnavailable })
	if _, body := s.get("/readyz"); body != `{"reason":"database unavailable","status":"unavailable"}` {
		t.Fatalf("GET /readyz = %s; want the database unavailable", body)
	}
	if code, _ := s.get("/healthz"); code != http.StatusOK {
		t.Fatalf("GET /healthz = %d while the database is down; liveness must not depend on it", code)
	}
	if code, _ := s.get("/status"); code != http.StatusServiceUnavailable {
		t.Fatalf("GET /status = %d while the database is down; want 503", code)
	}
	s.stop() // the exit code of a stop without a database is not part of this test
}

func TestFatalStartupErrorsExitWithOne(t *testing.T) {
	t.Run("schema newer than supported", func(t *testing.T) {
		databaseURL := newDatabase(t)
		if err := migrations.Up(t.Context(), databaseURL); err != nil {
			t.Fatal(err)
		}
		exec(t, databaseURL, "INSERT INTO goose_db_version (version_id, is_applied) VALUES ($1, true)", migrations.SupportedVersion+1)
		code, stdout, _ := run([]string{"serve"}, testEnv(serveVars(t, databaseURL)))
		if code != 1 || !strings.Contains(stdout, `"level":"ERROR"`) {
			t.Fatalf("serve on a newer schema exited %d, stdout %q; want 1 and a logged error", code, stdout)
		}
	})
	t.Run("address in use", func(t *testing.T) {
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = occupied.Close() }() // a failed close only means the port is already free
		vars := serveVars(t, newDatabase(t))
		vars["SUITEWARD_HTTP_ADDR"] = occupied.Addr().String()
		code, stdout, _ := run([]string{"serve"}, testEnv(vars))
		if code != 1 {
			t.Fatalf("serve on a taken address exited %d; want 1", code)
		}
		// River was already running: the failed start stops it and closes the pool.
		if !strings.Contains(stdout, "job runtime stopped") || !strings.Contains(stdout, "database pool closed") {
			t.Fatalf("a failed listen left River or the pool running:\n%s", stdout)
		}
	})
	t.Run("artifact directory is a file", func(t *testing.T) {
		vars := serveVars(t, newDatabase(t))
		vars["SUITEWARD_ARTIFACT_DIR"] = filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(vars["SUITEWARD_ARTIFACT_DIR"], []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, stdout, _ := run([]string{"serve"}, testEnv(vars)); code != 1 || !strings.Contains(stdout, `"level":"ERROR"`) {
			t.Fatalf("serve with an unusable artifact directory exited %d, stdout %q; want 1 and a logged error", code, stdout)
		}
	})
}

func TestProbeWithoutAServerTimesOut(t *testing.T) {
	databaseURL := newDatabase(t)
	// The schema is migrated by a service that is then stopped; nothing runs the job.
	s := startRun(t, serveVars(t, databaseURL))
	s.waitReady()
	s.stop()

	code, stdout, stderr := run([]string{"probe", "--timeout", "1s"}, s.env())
	if code != 2 || !strings.Contains(stderr, "timed out") {
		t.Fatalf("probe without a server exited %d, stderr %q; want 2 and a timeout message", code, stderr)
	}
	lines := jsonLines(t, stdout)
	if len(lines) != 2 || lines[1]["job"] != "available" || lines[1]["outbox"] != "pending" {
		t.Fatalf("probe printed %q; want the enqueued line and a final line with the states it saw", stdout)
	}
}

func TestProbeFailedTerminalStatesExitWithTwo(t *testing.T) {
	databaseURL := newDatabase(t)
	s := startRun(t, serveVars(t, databaseURL))
	s.waitReady()
	s.stop()
	for name, statement := range map[string]string{
		"discarded job":         "UPDATE river_job SET state = 'discarded', finalized_at = now() WHERE id = $1::bigint",
		"failed outbox message": "UPDATE outbox SET state = 'failed', finished_at = now() WHERE key = $1",
	} {
		t.Run(name, func(t *testing.T) {
			_, stdout, _ := run([]string{"probe", "--no-wait"}, s.env())
			id, _ := jsonLines(t, stdout)[0]["probeId"].(string)
			jobID, key, _ := strings.Cut(id, ":")
			argument := jobID
			if strings.Contains(statement, "outbox") {
				argument = key
			}
			exec(t, databaseURL, statement, argument)
			code, stdout, _ := run([]string{"probe", "--wait", id, "--timeout", "30s"}, s.env())
			if code != 2 {
				t.Fatalf("probe --wait on a %s exited %d; want 2: %s", name, code, stdout)
			}
		})
	}
}

func TestProbeErrorsExitWithOne(t *testing.T) {
	databaseURL := newDatabase(t)
	vars := serveVars(t, databaseURL)
	t.Run("unknown probe", func(t *testing.T) {
		s := startRun(t, vars)
		s.waitReady()
		s.stop()
		if code, _, stderr := run([]string{"probe", "--wait", "999999:probe-unknown"}, s.env()); code != 1 || !strings.Contains(stderr, "suiteward:") {
			t.Fatalf("probe --wait for an unknown probe exited %d, stderr %q; want 1", code, stderr)
		}
	})
	t.Run("schema not migrated", func(t *testing.T) {
		unmigrated := serveVars(t, newDatabase(t))
		if code, _, stderr := run([]string{"probe"}, testEnv(unmigrated)); code != 1 || !strings.Contains(stderr, "suiteward:") {
			t.Fatalf("probe on an unmigrated database exited %d, stderr %q; want 1", code, stderr)
		}
	})
}

func TestProbeDelaySchedulesTheJob(t *testing.T) {
	databaseURL := newDatabase(t)
	s := startRun(t, serveVars(t, databaseURL))
	s.waitReady()
	s.stop()
	_, stdout, _ := run([]string{"probe", "--delay", "1h", "--no-wait"}, s.env())
	jobID, _, _ := strings.Cut(jsonLines(t, stdout)[0]["probeId"].(string), ":")
	if state := scalar(t, databaseURL, "SELECT state::text FROM river_job WHERE id = $1::bigint", jobID); state != "scheduled" {
		t.Fatalf("a probe delayed by an hour is %q; want scheduled", state)
	}
}

// env is the environment the service was started with, for the probe client.
func (s *service) env() Env { return testEnv(s.vars) }

func waitChan(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// jsonLines decodes the output of probe, one JSON object per line.
func jsonLines(t *testing.T, output string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var decoded map[string]any
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("not a JSON line: %q in %q", line, output)
		}
		lines = append(lines, decoded)
	}
	return lines
}

func TestShutdownReportsAServerThatDoesNotDrain(t *testing.T) {
	vars := serveVars(t, newDatabase(t))
	vars["SUITEWARD_SHUTDOWN_TIMEOUT"] = "1s"
	s := startRun(t, vars)
	s.waitReady()
	// A request that never completes keeps its connection busy.
	conn, err := net.Dial("tcp", s.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }() // a failed close only means the server already dropped it
	if _, err := conn.Write([]byte("GET /status HTTP/1.1\r\nHost: suiteward\r\n")); err != nil {
		t.Fatal(err)
	}
	if code := s.stop(); code != 1 {
		t.Fatalf("a stop with an undrained server exited %d; want 1:\n%s", code, s.logs.String())
	}
	if entry, _ := s.logs.find("http server stopped"); entry != nil {
		t.Fatalf("an undrained server was reported as stopped:\n%s", s.logs.String())
	}
	if entry, _ := s.logs.find("database pool closed"); entry == nil {
		t.Fatalf("River and the pool must still stop after a failed drain:\n%s", s.logs.String())
	}
}

func TestShutdownGivesUpOnAJobThatIgnoresCancellation(t *testing.T) {
	databaseURL := newDatabase(t)
	vars := serveVars(t, databaseURL)
	vars["SUITEWARD_SHUTDOWN_TIMEOUT"] = "1s"
	started, release := make(chan struct{}), make(chan struct{})
	s := startServe(t, vars, map[string]river.Handler{"deaf": func(context.Context, json.RawMessage) error {
		close(started)
		<-release
		return nil
	}})
	s.waitReady()
	enqueue(t, databaseURL, "deaf", 0)
	waitChan(t, started, "the job to start")

	code := s.stop()
	close(release)
	if code != 1 {
		t.Fatalf("a stop that could not cancel a job exited %d; want 1:\n%s", code, s.logs.String())
	}
	if entry, _ := s.logs.find("database pool closed"); entry != nil {
		t.Fatalf("the pool was closed under a running job:\n%s", s.logs.String())
	}
}

func TestAReservedHandlerKindIsFatal(t *testing.T) {
	handlers := map[string]river.Handler{river.ProbeKind: func(context.Context, json.RawMessage) error { return nil }}
	vars := serveVars(t, newDatabase(t))
	cfg, _, err := config.Load(testEnv(vars).Lookup, testEnv(vars).Environ, testEnv(vars).ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	var logs logBuffer
	if code := serve(t.Context(), cfg, newLogger(&logs, cfg.LogLevel), handlers); code != 1 {
		t.Fatalf("serve with a handler on the reserved probe kind exited %d; want 1:\n%s", code, logs.String())
	}
}

type brokenOutput struct{}

func (brokenOutput) Write([]byte) (int, error) { return 0, errors.New("stdout closed") }

func TestProbeReportsAnOutputFailure(t *testing.T) {
	databaseURL := newDatabase(t)
	s := startRun(t, serveVars(t, databaseURL))
	s.waitReady()
	s.stop()
	var stderr bytes.Buffer
	if code := Run(t.Context(), []string{"probe", "--no-wait"}, s.env(), brokenOutput{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "stdout closed") {
		t.Fatalf("probe with a broken stdout exited %d, stderr %q; want 1 and the cause", code, stderr.String())
	}
}

func TestProbeFailsWhenItCannotEnqueue(t *testing.T) {
	databaseURL := newDatabase(t)
	s := startRun(t, serveVars(t, databaseURL))
	s.waitReady()
	s.stop()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var stderr bytes.Buffer
	if code := Run(ctx, []string{"probe"}, s.env(), io.Discard, &stderr); code != 1 {
		t.Fatalf("probe with a canceled context exited %d, stderr %q; want 1", code, stderr.String())
	}
}
