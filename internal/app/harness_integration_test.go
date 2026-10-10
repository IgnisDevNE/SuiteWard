//go:build integration

package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// newDatabase creates a schema of its own in the test database and returns a
// connection URL that uses it. The schema is empty: serve migrates it.
func newDatabase(t *testing.T) string {
	t.Helper()
	base := os.Getenv("SUITEWARD_TEST_DATABASE_URL")
	if base == "" {
		t.Fatal("SUITEWARD_TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	admin, err := pgx.Connect(t.Context(), base)
	if err != nil {
		t.Fatalf("connect PostgreSQL schema fixture: %v", err)
	}
	schema := "sw_app_" + strings.ToLower(rand.Text())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("remove owned schema: %v", err)
		}
		_ = admin.Close(context.Background()) // cleanup path: a failed close only means the connection is gone
	})
	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// password is the database password of a connection URL: the secret the logs and endpoints must never show.
func password(t *testing.T, databaseURL string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	secret, ok := parsed.User.Password()
	if !ok || secret == "" {
		t.Fatal("the test database URL has no password to protect")
	}
	return secret
}

// scalar runs a query that returns one text value.
func scalar(t *testing.T, databaseURL, query string, args ...any) string {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }() // a failed close only means the connection is gone
	var value string
	if err := conn.QueryRow(t.Context(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func exec(t *testing.T, databaseURL, statement string, args ...any) {
	t.Helper()
	conn, err := pgx.Connect(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }() // a failed close only means the connection is gone
	if _, err := conn.Exec(t.Context(), statement, args...); err != nil {
		t.Fatal(err)
	}
}

// enqueue writes a system job of kind, scheduled after delay, and its outbox message.
func enqueue(t *testing.T, databaseURL, kind string, delay time.Duration) int64 {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	artifacts, err := filesystem.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := river.NewInserter(3)
	if err != nil {
		t.Fatal(err)
	}
	store, err := postgres.NewStore(pool, artifacts, inserter)
	if err != nil {
		t.Fatal(err)
	}
	job := governance.Job{Kind: kind, Args: []byte(`{}`)}
	if delay > 0 {
		job.ScheduledAt = time.Now().Add(delay)
	}
	id, err := store.EnqueueSystem(t.Context(), job, governance.OutboxMessage{Key: "test-" + rand.Text(), Kind: "probe", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// logBuffer collects what a service logs.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// find returns the first log entry whose message is msg, and its position among the entries with a message.
func (l *logBuffer) find(msg string) (entry map[string]any, position int) {
	for position, line := range strings.Split(l.String(), "\n") {
		var candidate map[string]any
		if json.Unmarshal([]byte(line), &candidate) == nil && candidate["msg"] == msg {
			return candidate, position
		}
	}
	return nil, -1
}

// waitFor polls cond until it holds.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// service is a running serve command.
type service struct {
	t      *testing.T
	vars   map[string]string
	logs   *logBuffer
	cancel context.CancelFunc
	done   chan int
	addr   string
	code   *int
}

// serveVars are the settings of a test service over databaseURL.
func serveVars(t *testing.T, databaseURL string) map[string]string {
	return map[string]string{
		"SUITEWARD_DATABASE_URL":         databaseURL,
		"SUITEWARD_ARTIFACT_DIR":         t.TempDir(),
		"SUITEWARD_HTTP_ADDR":            "127.0.0.1:0",
		"SUITEWARD_SHUTDOWN_TIMEOUT":     "5s",
		"SUITEWARD_OUTBOX_POLL_INTERVAL": "1s",
		"SUITEWARD_LOG_LEVEL":            "debug",
	}
}

// startRun serves through Run, as the binary does.
func startRun(t *testing.T, vars map[string]string) *service {
	t.Helper()
	return launch(t, vars, func(ctx context.Context, env Env, logs io.Writer) int {
		return Run(ctx, []string{"serve"}, env, logs, io.Discard)
	})
}

// startServe serves with extra job handlers.
func startServe(t *testing.T, vars map[string]string, handlers map[string]river.Handler) *service {
	t.Helper()
	return launch(t, vars, func(ctx context.Context, env Env, logs io.Writer) int {
		cfg, _, err := config.Load(env.Lookup, env.Environ, env.ReadFile)
		if err != nil {
			t.Error(err)
			return -1
		}
		return serve(ctx, cfg, newLogger(logs, cfg.LogLevel), handlers)
	})
}

func launch(t *testing.T, vars map[string]string, run func(context.Context, Env, io.Writer) int) *service {
	t.Helper()
	logs := &logBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	s := &service{t: t, vars: vars, logs: logs, cancel: cancel, done: make(chan int, 1)}
	go func() { s.done <- run(ctx, testEnv(vars), logs) }()
	t.Cleanup(func() {
		if s.code == nil {
			s.stop()
		}
		secret := password(t, vars["SUITEWARD_DATABASE_URL"])
		if strings.Contains(logs.String(), secret) || strings.Contains(logs.String(), vars["SUITEWARD_DATABASE_URL"]) {
			t.Errorf("the database credentials were logged:\n%s", logs.String())
		}
	})
	waitFor(t, "the service to listen", 30*time.Second, func() bool {
		select {
		case code := <-s.done:
			s.code = &code
			t.Fatalf("the service exited with %d before listening:\n%s", code, logs.String())
		default:
		}
		entry, _ := logs.find("listening")
		if entry != nil {
			s.addr, _ = entry["addr"].(string)
		}
		return entry != nil
	})
	return s
}

// stop cancels the service context, as a signal does, and returns the exit code.
func (s *service) stop() int {
	s.t.Helper()
	s.cancel()
	select {
	case code := <-s.done:
		s.code = &code
		return code
	case <-time.After(60 * time.Second):
		s.t.Fatalf("the service did not stop:\n%s", s.logs.String())
		return -1
	}
}

// get requests a path of the service.
func (s *service) get(path string) (int, string) {
	s.t.Helper()
	client := http.Client{Timeout: 10 * time.Second}
	response, err := client.Get("http://" + s.addr + path)
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }() // the body was read; a failed close changes nothing
	body, err := io.ReadAll(response.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return response.StatusCode, strings.TrimSpace(string(body))
}

// waitReady waits until /readyz answers 200.
func (s *service) waitReady() {
	s.t.Helper()
	waitFor(s.t, "readiness", 30*time.Second, func() bool {
		code, _ := s.get("/readyz")
		return code == http.StatusOK
	})
}

// status decodes /status.
func (s *service) status() map[string]any {
	s.t.Helper()
	code, body := s.get("/status")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil || code != http.StatusOK {
		s.t.Fatalf("GET /status = %d %s", code, body)
	}
	return decoded
}

// proxy forwards TCP connections to a target until cut.
type proxy struct {
	listener net.Listener
	mu       sync.Mutex
	conns    []net.Conn
}

func newProxy(t *testing.T, target string) *proxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &proxy{listener: listener}
	t.Cleanup(p.cut)
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", target)
			if err != nil {
				_ = client.Close() // the client sees a closed connection, which is the failure to simulate
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, client, upstream)
			p.mu.Unlock()
			go func() { _, _ = io.Copy(upstream, client) }()
			go func() { _, _ = io.Copy(client, upstream) }()
		}
	}()
	return p
}

// cut stops accepting and drops every connection: the database becomes unreachable.
func (p *proxy) cut() {
	_ = p.listener.Close() // closing twice is harmless
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, conn := range p.conns {
		_ = conn.Close() // closing twice is harmless
	}
}
