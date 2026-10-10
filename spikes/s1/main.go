// Command s1 is the throwaway S1 spike: it polls one repository as a GitHub App,
// computes the protected-set digest of each open PR and publishes a check run.
// See CONTRACT.md for the frozen behavior and README.md for how to read the log.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

type config struct {
	appID, installationID, repo, ownerLogin string
	keyFile, stateFile, logFile             string
	pollInterval                            time.Duration
	apiBase, permissions, protectedPrefix   string
	checkName, forceConclusion, statusAddr  string
	once                                    bool
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	c := config{
		appID: os.Getenv("S1_APP_ID"), installationID: os.Getenv("S1_INSTALLATION_ID"),
		repo: os.Getenv("S1_REPO"), ownerLogin: os.Getenv("S1_OWNER_LOGIN"),
		keyFile: env("S1_KEY_FILE", "/run/secrets/app-key"), stateFile: env("S1_STATE_FILE", "/data/state.json"),
		logFile: os.Getenv("S1_LOG_FILE"), apiBase: strings.TrimRight(env("S1_API_BASE", "https://api.github.com"), "/"),
		permissions: os.Getenv("S1_TOKEN_PERMISSIONS"), protectedPrefix: env("S1_PROTECTED_PREFIX", "tests/"),
		checkName: env("S1_CHECK_NAME", "SuiteWard Spike / Contract"), forceConclusion: os.Getenv("S1_FORCE_CONCLUSION"),
		statusAddr: env("S1_STATUS_ADDR", "127.0.0.1:8080"), once: os.Getenv("S1_ONCE") == "1",
	}
	for name, v := range map[string]string{"S1_APP_ID": c.appID, "S1_INSTALLATION_ID": c.installationID, "S1_REPO": c.repo, "S1_OWNER_LOGIN": c.ownerLogin} {
		if v == "" {
			return c, fmt.Errorf("%s is required", name)
		}
	}
	if strings.Count(c.repo, "/") != 1 {
		return c, errors.New("S1_REPO must be owner/name")
	}
	switch c.forceConclusion {
	case "", "in_progress", "success", "failure", "neutral", "action_required":
	default:
		return c, fmt.Errorf("S1_FORCE_CONCLUSION %q is not a check state", c.forceConclusion)
	}
	var err error
	if c.pollInterval, err = time.ParseDuration(env("S1_POLL_INTERVAL", "60s")); err != nil || c.pollInterval <= 0 {
		return c, errors.New("S1_POLL_INTERVAL must be a positive Go duration")
	}
	return c, nil
}

type lastPoll struct {
	At    time.Time `json:"at"`
	Cycle int       `json:"cycle"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
}

type app struct {
	cfg       config
	c         *client
	log       *logger
	st        *state
	published map[int]string // "<check run id>:<state>" last sent per PR; memory only, a restart republishes once
	cycles    int
	started   time.Time

	mu    sync.Mutex // guards the snapshot served by /status
	pulls json.RawMessage
	last  lastPoll
}

// cycle polls the PR list, digests every open PR head and publishes check runs.
// It returns the joined errors of the steps that failed; the others still ran.
func (a *app) cycle(ctx context.Context) error {
	began := time.Now()
	a.cycles++
	a.c.calls = nil
	pulls, err := a.c.openPulls(ctx, a.cfg.repo)
	var errs []error
	if err != nil {
		errs = append(errs, err)
	} else {
		seen := map[string]bool{listPath(a.cfg.repo): true}
		open := map[int]bool{}
		for _, pr := range pulls {
			open[pr.Number] = true
			seen[treePath(a.cfg.repo, pr.Head.SHA)], seen[commentsPath(a.cfg.repo, pr.Number)] = true, true
			if err := a.pull(ctx, pr); err != nil {
				errs = append(errs, fmt.Errorf("PR #%d: %w", pr.Number, err))
			}
		}
		// Closed PRs and old heads would otherwise keep their ETags forever.
		for u := range a.st.Etags {
			if !seen[u] {
				delete(a.st.Etags, u)
				delete(a.c.cache, u)
			}
		}
		// A PR the state knew as open that the list no longer shows was closed or merged.
		for _, n := range slices.Sorted(maps.Keys(a.st.Pulls)) {
			if ps := a.st.Pulls[n]; !open[n] && ps.Closed == "" {
				if err := a.closed(ctx, n, ps); err != nil {
					errs = append(errs, fmt.Errorf("PR #%d: %w", n, err))
				}
			}
		}
	}
	if err := a.st.save(a.cfg.stateFile); err != nil {
		errs = append(errs, err)
	}
	err = errors.Join(errs...)
	f := map[string]any{"cycle": a.cycles, "open_prs": len(pulls), "calls": a.c.calls, "dur_ms": ms(time.Since(began)), "ok": err == nil}
	lp := lastPoll{At: began.UTC(), Cycle: a.cycles, OK: err == nil}
	if err != nil {
		f["error"], lp.Error = err.Error(), err.Error()
	}
	a.log.emit("poll", f)
	a.publishSnapshot(lp)
	return err
}

func (a *app) pull(ctx context.Context, pr pull) error {
	entries, err := a.c.tree(ctx, a.cfg.repo, pr.Head.SHA)
	if err != nil {
		return err
	}
	digest, files := protectedDigest(entries, a.cfg.protectedPrefix)
	ref := digest[:12]

	ps := a.st.Pulls[pr.Number]
	if ps == nil {
		ps = &pullState{}
		a.st.Pulls[pr.Number] = ps
	}
	if ps.HeadSHA != pr.Head.SHA || ps.Digest != digest {
		a.log.emit("digest", map[string]any{"number": pr.Number, "head_sha": pr.Head.SHA, "digest": digest, "ref": ref, "files": files, "prefix": a.cfg.protectedPrefix})
	}
	if ps.HeadSHA != pr.Head.SHA {
		ps.HeadSHA, ps.CheckRunID = pr.Head.SHA, 0 // a check run belongs to one head sha
	}
	ps.Closed = "" // a closed PR that shows up open again is tracked again
	if ps.Ref != ref {
		// An approval covers one ref. The comments are scanned again below, so a ref that comes back regains it.
		ps.ApprovedCommentID, ps.ApprovedAt, ps.ApprovedUpdatedAt, ps.ApprovedDigest = 0, "", "", ""
		ps.ApprovalObserved, ps.ApprovalObservedUpdatedAt, ps.Rejected = "", "", ""
	}
	ps.Digest, ps.Ref = digest, ref

	cs, err := a.c.comments(ctx, a.cfg.repo, pr.Number)
	if err != nil {
		return err
	}
	a.scanApproval(pr.Number, ps, cs)

	// success once an approval covers the ref; failure after an invalid approval attempt under this ref.
	want := "in_progress"
	switch {
	case ps.ApprovedCommentID != 0:
		want = "success"
	case ps.Rejected != "":
		want = "failure"
	}
	if a.cfg.forceConclusion != "" {
		want = a.cfg.forceConclusion
	}
	if a.published[pr.Number] == fmt.Sprintf("%d:%s", ps.CheckRunID, want) {
		return nil
	}
	action := "update"
	if ps.CheckRunID == 0 {
		action = "create"
	}
	summary := fmt.Sprintf("Protected set `%s`: %d files, ref `%s`, digest `%s`.", a.cfg.protectedPrefix, files, ref, digest)
	if want == "failure" {
		summary += " Invalid approval: " + ps.Rejected + "."
	}
	id, err := a.c.putCheck(ctx, a.cfg.repo, a.cfg.checkName, pr.Head.SHA, ps.CheckRunID, want, summary)
	if err != nil {
		return err
	}
	ps.CheckRunID = id
	a.published[pr.Number] = fmt.Sprintf("%d:%s", id, want)
	a.log.emit("check", map[string]any{"number": pr.Number, "head_sha": pr.Head.SHA, "state": want, "check_run_id": id, "action": action})
	return nil
}

func (a *app) publishSnapshot(l lastPoll) {
	b, err := json.Marshal(a.st.Pulls)
	if err != nil {
		b = []byte(`null`)
	}
	a.mu.Lock()
	a.pulls, a.last = b, l
	a.mu.Unlock()
}

func (a *app) status(w http.ResponseWriter, _ *http.Request) {
	a.mu.Lock()
	out := map[string]any{"uptime_s": int(time.Since(a.started).Seconds()), "last_poll": a.last, "pulls": a.pulls}
	a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	var out io.Writer = os.Stdout
	if cfg.logFile != "" {
		f, err := os.OpenFile(cfg.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		defer f.Close()
		out = io.MultiWriter(os.Stdout, f)
	}
	lg := &logger{w: out, start: time.Now()}
	keyPEM, err := os.ReadFile(cfg.keyFile)
	if err != nil {
		return fmt.Errorf("read key file: %w", err)
	}
	key, err := parsePrivateKey(keyPEM)
	if err != nil {
		return err
	}
	perms, err := parsePermissions(cfg.permissions)
	if err != nil {
		return err
	}
	st, err := loadState(cfg.stateFile)
	if err != nil {
		return err
	}
	a := &app{
		cfg: cfg, log: lg, st: st, published: map[int]string{}, started: time.Now(),
		c: &client{
			base: cfg.apiBase, appID: cfg.appID, installationID: cfg.installationID, key: key, permissions: perms,
			http: &http.Client{Timeout: 30 * time.Second}, log: lg, etags: st.Etags, cache: map[string][]byte{},
		},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.once {
		return a.cycle(ctx)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", a.status)
	srv := &http.Server{Addr: cfg.statusAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			lg.emit("status_error", map[string]any{"error": err.Error()})
		}
	}()
	defer srv.Close()
	for {
		_ = a.cycle(ctx) // logged in the poll event; the next cycle retries
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.pollInterval):
		}
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "s1:", err)
		os.Exit(1)
	}
}
