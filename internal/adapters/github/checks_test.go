package github_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

const checkRunsURI = "/repos/octo-org/octo-repo/check-runs"

func validRun() github.CheckRun {
	return github.CheckRun{
		Name: "SuiteWard / Contract", HeadSHA: commitSHA, Status: "completed", Conclusion: "success",
		ExternalID: "gen-1", Title: "Approved", Summary: "all protected behavior is covered",
	}
}

func newChecks(t *testing.T, h http.Handler) *github.Checks {
	t.Helper()
	_, client := serve(t, h)
	c, err := github.NewChecks(client)
	if err != nil {
		t.Fatalf("NewChecks: %v", err)
	}
	return c
}

func TestNewChecksRejectsNilClient(t *testing.T) {
	if _, err := github.NewChecks(nil); err == nil {
		t.Fatal("NewChecks(nil) succeeded")
	}
}

func TestChecksPostSendsTheRunAndReturnsItsID(t *testing.T) {
	p := &poster{status: http.StatusCreated, answer: `{"id": 9001, "name": "ignored"}`}
	c := newChecks(t, p)

	id, err := c.Post(t.Context(), repo, validRun())
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if id != 9001 {
		t.Errorf("id = %d, want 9001", id)
	}
	reqs := p.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodPost || reqs[0].uri != checkRunsURI || reqs[0].contentType != "application/json" {
		t.Fatalf("requests = %+v, want one JSON POST to %s", reqs, checkRunsURI)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(reqs[0].body), &sent); err != nil {
		t.Fatalf("sent body %q: %v", reqs[0].body, err)
	}
	want := map[string]any{
		"name": "SuiteWard / Contract", "head_sha": commitSHA, "status": "completed", "conclusion": "success",
		"external_id": "gen-1", "output": map[string]any{"title": "Approved", "summary": "all protected behavior is covered"},
	}
	if fmt.Sprint(sent) != fmt.Sprint(want) { // fmt prints maps with sorted keys
		t.Errorf("sent = %v, want %v", sent, want)
	}
}

func TestChecksPostOmitsTheConclusionOfAnInProgressRun(t *testing.T) {
	p := &poster{status: http.StatusCreated, answer: `{"id": 9002}`}
	run := validRun()
	run.Status, run.Conclusion = "in_progress", ""

	if _, err := newChecks(t, p).Post(t.Context(), repo, run); err != nil {
		t.Fatalf("Post: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(p.requests()[0].body), &sent); err != nil {
		t.Fatal(err)
	}
	if _, has := sent["conclusion"]; has || sent["status"] != "in_progress" {
		t.Errorf("sent = %v, want in_progress without a conclusion", sent)
	}
}

func TestChecksPostRejectsInvalidRunsBeforeAnyRequest(t *testing.T) {
	cases := map[string]func(repo *string, r *github.CheckRun){
		"empty name":              func(_ *string, r *github.CheckRun) { r.Name = "" },
		"short head sha":          func(_ *string, r *github.CheckRun) { r.HeadSHA = "c0ffee" },
		"uppercase head sha":      func(_ *string, r *github.CheckRun) { r.HeadSHA = strings.ToUpper(commitSHA) },
		"unknown status":          func(_ *string, r *github.CheckRun) { r.Status = "queued" },
		"empty status":            func(_ *string, r *github.CheckRun) { r.Status = "" },
		"completed without":       func(_ *string, r *github.CheckRun) { r.Conclusion = "" },
		"neutral conclusion":      func(_ *string, r *github.CheckRun) { r.Conclusion = "neutral" },
		"cancelled conclusion":    func(_ *string, r *github.CheckRun) { r.Conclusion = "cancelled" },
		"in progress with result": func(_ *string, r *github.CheckRun) { r.Status = "in_progress" },
		"empty external id":       func(_ *string, r *github.CheckRun) { r.ExternalID = "" },
		"empty title":             func(_ *string, r *github.CheckRun) { r.Title = "" },
		"empty summary":           func(_ *string, r *github.CheckRun) { r.Summary = "" },
		"oversized summary":       func(_ *string, r *github.CheckRun) { r.Summary = strings.Repeat("x", 70<<10) },
		"bad repository":          func(repo *string, _ *github.CheckRun) { *repo = "octo-org/.." },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := &poster{status: http.StatusCreated, answer: `{"id":1}`}
			run, r := validRun(), repo
			mutate(&r, &run)
			if _, err := newChecks(t, p).Post(t.Context(), r, run); !errors.Is(err, github.ErrInvalidArgument) {
				t.Errorf("err = %v, want ErrInvalidArgument", err)
			}
			if n := len(p.requests()); n != 0 {
				t.Errorf("%d requests, want none", n)
			}
		})
	}
}

func TestChecksPostRejectsAnAnswerWithoutAnID(t *testing.T) {
	for _, answer := range []string{`{}`, `{"id":0}`, `[]`, `not json`} {
		c := newChecks(t, &poster{status: http.StatusCreated, answer: answer})
		if id, err := c.Post(t.Context(), repo, validRun()); !errors.Is(err, github.ErrInvalidResponse) {
			t.Errorf("answer %q: id %d, err %v; want ErrInvalidResponse", answer, id, err)
		}
	}
}

func TestChecksPostErrorsStayTypedAndCarryNoText(t *testing.T) {
	const answer = "distinctive-answer-text-3141"
	run := validRun()
	run.Summary = "distinctive-summary-text-2718"
	cases := []struct {
		status int
		header map[string]string
		want   error
	}{
		{http.StatusUnprocessableEntity, nil, rest.ErrUnprocessable},
		{http.StatusForbidden, nil, rest.ErrForbidden},
		{http.StatusTooManyRequests, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": "4102444800"}, rest.ErrRateLimited},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			c := newChecks(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for k, v := range tc.header {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"message":"`+answer+`"}`)
			}))
			_, err := c.Post(t.Context(), repo, run)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), answer) || strings.Contains(err.Error(), run.Summary) {
				t.Errorf("error %q carries the answer or the summary", err)
			}
		})
	}
}

// runsServer answers each page number from pages and records the request URLs.
type runsServer struct {
	pages map[string]string
	mu    sync.Mutex
	seen  []url.URL
}

func (s *runsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.seen = append(s.seen, *r.URL)
	s.mu.Unlock()
	body, found := s.pages[r.URL.Query().Get("page")]
	if r.Method != http.MethodGet || !found {
		w.WriteHeader(http.StatusTeapot)
		return
	}
	_, _ = io.WriteString(w, body)
}

func runsPage(first, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = fmt.Sprintf(`{"id":%d,"status":"completed","conclusion":"success","external_id":"gen-%d","name":"x"}`, first+i, first+i)
	}
	return `{"total_count":999,"check_runs":[` + strings.Join(parts, ",") + `]}`
}

func TestChecksRunsQueriesBySHAEscapedNameAndApp(t *testing.T) {
	s := &runsServer{pages: map[string]string{"1": `{"total_count":1,"check_runs":[
		{"id":77,"status":"in_progress","conclusion":null,"external_id":"gen-3"}]}`}}

	got, err := newChecks(t, s).Runs(t.Context(), repo, commitSHA, "SuiteWard / Contract", 4711)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if want := []github.CheckRunSummary{{ID: 77, Status: "in_progress", ExternalID: "gen-3"}}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("runs = %+v, want %+v", got, want)
	}
	if len(s.seen) != 1 {
		t.Fatalf("%d requests, want 1", len(s.seen))
	}
	u := s.seen[0]
	q := u.Query()
	if u.Path != "/repos/octo-org/octo-repo/commits/"+commitSHA+"/check-runs" ||
		q.Get("check_name") != "SuiteWard / Contract" || q.Get("app_id") != "4711" || q.Get("per_page") != "100" {
		t.Errorf("request = %s, want the check-runs of the commit by check_name, app_id and per_page", u.String())
	}
	if strings.Contains(u.RawQuery, " ") || strings.Contains(u.RawQuery, "/") {
		t.Errorf("raw query %q is not escaped", u.RawQuery)
	}
}

func TestChecksRunsPagesThroughTheListing(t *testing.T) {
	s := &runsServer{pages: map[string]string{"1": runsPage(1, 100), "2": runsPage(101, 3)}}

	got, err := newChecks(t, s).Runs(t.Context(), repo, commitSHA, "SuiteWard / Contract", 4711)
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(got) != 103 || got[0].ID != 1 || got[102].ID != 103 || got[0].Conclusion != "success" || got[0].ExternalID != "gen-1" {
		t.Errorf("got %d runs, want 103 sorted by id with conclusion and external id", len(got))
	}
}

func TestChecksRunsOfNoRunIsAnEmptyList(t *testing.T) {
	s := &runsServer{pages: map[string]string{"1": `{"total_count":0,"check_runs":[]}`}}
	got, err := newChecks(t, s).Runs(t.Context(), repo, commitSHA, "n", 1)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("runs = %v, %v; want an empty non-nil list", got, err)
	}
}

func TestChecksRunsRejectsInvalidArgumentsBeforeAnyRequest(t *testing.T) {
	s := &runsServer{pages: map[string]string{"1": runsPage(1, 1)}}
	c := newChecks(t, s)
	cases := map[string]func() error{
		"bad repository": func() error { _, err := c.Runs(t.Context(), "octo-org", commitSHA, "n", 1); return err },
		"bad sha":        func() error { _, err := c.Runs(t.Context(), repo, "main", "n", 1); return err },
		"empty name":     func() error { _, err := c.Runs(t.Context(), repo, commitSHA, "", 1); return err },
		"zero app id":    func() error { _, err := c.Runs(t.Context(), repo, commitSHA, "n", 0); return err },
	}
	for name, call := range cases {
		if err := call(); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want ErrInvalidArgument", name, err)
		}
	}
	if len(s.seen) != 0 {
		t.Errorf("%d requests, want none", len(s.seen))
	}
}

func TestChecksRunsRejectsAnInvalidAnswer(t *testing.T) {
	cases := map[string]string{
		"not json":       `nope`,
		"a list":         `[]`,
		"no check_runs":  `{"total_count":0}`,
		"null runs":      `{"check_runs":null}`,
		"zero id":        `{"check_runs":[{"id":0,"status":"completed","conclusion":"success"}]}`,
		"missing id":     `{"check_runs":[{"status":"completed","conclusion":"success"}]}`,
		"unknown status": `{"check_runs":[{"id":5,"status":"exploded"}]}`,
		"missing status": `{"check_runs":[{"id":5}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s := &runsServer{pages: map[string]string{"1": body}}
			if got, err := newChecks(t, s).Runs(t.Context(), repo, commitSHA, "n", 1); !errors.Is(err, github.ErrInvalidResponse) {
				t.Errorf("runs = %v, err = %v; want ErrInvalidResponse", got, err)
			}
		})
	}
}

func TestChecksRunsKeepsRestErrorsTyped(t *testing.T) {
	c := newChecks(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnprocessableEntity) }))
	if _, err := c.Runs(t.Context(), repo, commitSHA, "n", 1); !errors.Is(err, rest.ErrUnprocessable) {
		t.Errorf("err = %v, want rest.ErrUnprocessable", err)
	}
}
