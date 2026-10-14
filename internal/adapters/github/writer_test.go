package github_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
)

const (
	hubotID   = 480938
	octocatID = 583231

	branchURI = "/repos/octo-org/octo-repo/branches/"
	postURI   = "/repos/octo-org/octo-repo/issues/42/comments"
	branchSHA = "0123456789abcdef0123456789abcdef01234567"
)

// serve starts a fake GitHub and returns it with a rest.Client over it.
func serve(t *testing.T, h http.Handler) (*httptest.Server, *rest.Client) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	budget, err := rest.NewBudget(0.5, time.Now)
	if err != nil {
		t.Fatalf("NewBudget: %v", err)
	}
	token := func(context.Context) (string, string, error) { return "fake-token", "fake-identity", nil }
	client, err := rest.NewClient(token, srv.Client(), srv.URL, budget)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return srv, client
}

func newWriter(t *testing.T, h http.Handler) *github.Writer {
	t.Helper()
	_, client := serve(t, h)
	w, err := github.NewWriter(client)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	return w
}

// poster records every request and answers with the same status and body.
type poster struct {
	status int
	answer string

	mu   sync.Mutex
	seen []postedRequest
}

type postedRequest struct{ method, uri, contentType, body string }

func (p *poster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body) // a short read shows up as a body mismatch in the assertions
	p.mu.Lock()
	p.seen = append(p.seen, postedRequest{r.Method, r.URL.RequestURI(), r.Header.Get("Content-Type"), string(body)})
	p.mu.Unlock()
	w.WriteHeader(p.status)
	_, _ = io.WriteString(w, p.answer)
}

func (p *poster) requests() []postedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.seen)
}

func TestNewWriterRejectsNilClient(t *testing.T) {
	if _, err := github.NewWriter(nil); err == nil {
		t.Fatal("NewWriter(nil) succeeded")
	}
}

func TestPostCommentPostsTheBodyAndReturnsTheNewID(t *testing.T) {
	for _, text := range []string{"hello", "a \"quoted\" <b>line</b>\nsecond line & more", "ünïcode ✓"} {
		p := &poster{status: http.StatusCreated, answer: `{"id": 4711, "body": "ignored"}`}
		w := newWriter(t, p)

		id, err := w.PostComment(t.Context(), repo, 42, text)
		if err != nil {
			t.Fatalf("PostComment(%q): %v", text, err)
		}
		if id != 4711 {
			t.Errorf("id = %d, want 4711", id)
		}
		reqs := p.requests()
		if len(reqs) != 1 || reqs[0].method != http.MethodPost || reqs[0].uri != postURI || reqs[0].contentType != "application/json" {
			t.Fatalf("requests = %+v, want one JSON POST to %s", reqs, postURI)
		}
		var sent map[string]string
		if err := json.Unmarshal([]byte(reqs[0].body), &sent); err != nil || len(sent) != 1 || sent["body"] != text {
			t.Errorf("sent body = %q (%v), want exactly {\"body\": %q}", reqs[0].body, err, text)
		}
	}
}

func TestPostCommentRejectsInvalidArgumentsBeforeAnyRequest(t *testing.T) {
	p := &poster{status: http.StatusCreated, answer: `{"id":1}`}
	w := newWriter(t, p)
	cases := []struct {
		name   string
		repo   string
		number int
		body   string
	}{
		{"repository without owner", "octo-repo", 42, "x"},
		{"repository with a path", "octo-org/octo-repo/extra", 42, "x"},
		{"repository with a dot-dot", "octo-org/..", 42, "x"},
		{"zero number", repo, 0, "x"},
		{"negative number", repo, -1, "x"},
		{"empty body", repo, 42, ""},
	}
	for _, c := range cases {
		if _, err := w.PostComment(t.Context(), c.repo, c.number, c.body); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("%s: err = %v, want ErrInvalidArgument", c.name, err)
		}
	}
	if n := len(p.requests()); n != 0 {
		t.Errorf("%d requests, want none", n)
	}
}

func TestPostCommentRejectsAnAnswerWithoutAnID(t *testing.T) {
	for _, answer := range []string{`{}`, `{"id":0}`, `{"id":-3}`, `[]`, `not json`} {
		w := newWriter(t, &poster{status: http.StatusCreated, answer: answer})
		if id, err := w.PostComment(t.Context(), repo, 42, "x"); !errors.Is(err, github.ErrInvalidResponse) {
			t.Errorf("answer %q: id %d, err %v; want ErrInvalidResponse", answer, id, err)
		}
	}
}

func TestPostCommentErrorsStayTypedAndCarryNoText(t *testing.T) {
	const text = "distinctive-comment-text-2718"
	const answer = "distinctive-answer-text-3141"
	w := newWriter(t, &poster{status: http.StatusUnprocessableEntity, answer: `{"message":"` + answer + `"}`})

	_, err := w.PostComment(t.Context(), repo, 42, text)
	if !errors.Is(err, rest.ErrUnprocessable) {
		t.Fatalf("err = %v, want rest.ErrUnprocessable", err)
	}
	if strings.Contains(err.Error(), text) || strings.Contains(err.Error(), answer) {
		t.Errorf("error %q carries the comment or the answer text", err)
	}
}

func newWriterOnRoutes(t *testing.T, routes map[string]reply) (*github.Writer, *fakeGitHub) {
	t.Helper()
	gh := &fakeGitHub{t: t, routes: routes}
	return newWriter(t, gh), gh
}

func TestCommentsByAuthorKeepsOnlyThatAuthorsComments(t *testing.T) {
	w, _ := newWriterOnRoutes(t, map[string]reply{commentsURI + "1": ok(fixture(t, "comments.json"))})

	got, err := w.CommentsByAuthor(t.Context(), repo, 42, octocatID)
	if err != nil {
		t.Fatalf("CommentsByAuthor: %v", err)
	}
	if want := fixtureComments[:1]; !slices.EqualFunc(got, want, sameComment) {
		t.Errorf("comments = %+v, want %+v (with ID, author and body)", got, want)
	}

	got, err = w.CommentsByAuthor(t.Context(), repo, 42, 999)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("unknown author: %v, %v; want an empty non-nil slice", got, err)
	}
}

func TestCommentsByAuthorPagesThroughTheListing(t *testing.T) {
	w, gh := newWriterOnRoutes(t, map[string]reply{
		commentsURI + "1": ok(generatedPage(t, "comments.json", "id", 5001, 100)), // hubot, ids 5001..5100
		commentsURI + "2": ok(fixture(t, "comments.json")),                        // 2002 octocat, 3001 hubot
	})

	got, err := w.CommentsByAuthor(t.Context(), repo, 42, hubotID)
	if err != nil {
		t.Fatalf("CommentsByAuthor: %v", err)
	}
	if len(got) != 101 || got[0].ID != 3001 || got[100].ID != 5100 {
		t.Errorf("got %d comments (first %d), want 101 sorted by ID from 3001", len(got), got[0].ID)
	}
	if uris := gh.uris(); !slices.Equal(uris, []string{commentsURI + "1", commentsURI + "2"}) {
		t.Errorf("requests = %v, want pages 1 and 2", uris)
	}
}

func TestCommentsByAuthorRejectsInvalidArgumentsBeforeAnyRequest(t *testing.T) {
	w, gh := newWriterOnRoutes(t, map[string]reply{})
	for name, call := range map[string]func() error{
		"repo":   func() error { _, err := w.CommentsByAuthor(t.Context(), "octo-repo", 42, hubotID); return err },
		"number": func() error { _, err := w.CommentsByAuthor(t.Context(), repo, 0, hubotID); return err },
		"author": func() error { _, err := w.CommentsByAuthor(t.Context(), repo, 42, 0); return err },
	} {
		if err := call(); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("bad %s: err = %v, want ErrInvalidArgument", name, err)
		}
	}
	if uris := gh.uris(); len(uris) != 0 {
		t.Errorf("requests = %v, want none", uris)
	}
}

func TestBranchHeadReturnsTheCommitSHA(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{branchURI + "release%2F1.x": ok(fixture(t, "branch.json"))})

	sha, err := reader.BranchHead(t.Context(), repo, "release/1.x")
	if err != nil {
		t.Fatalf("BranchHead: %v", err)
	}
	if sha != branchSHA {
		t.Errorf("sha = %q, want %q", sha, branchSHA)
	}
	if got := gh.uris(); !slices.Equal(got, []string{branchURI + "release%2F1.x"}) {
		t.Errorf("requests = %v, want the branch with its slash escaped", got)
	}
}

func TestBranchHeadEscapesTheBranchInThePath(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{branchURI + "fix%2Fa%20b%3Fc%23d%25": ok(fixture(t, "branch.json"))})
	if _, err := reader.BranchHead(t.Context(), repo, "fix/a b?c#d%"); err != nil {
		t.Fatalf("BranchHead: %v", err)
	}
	if n := len(gh.requests()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestBranchHeadRejectsAnInvalidSHA(t *testing.T) {
	for name, sha := range map[string]string{
		"empty": "", "short": "c0ffee", "upper case": strings.ToUpper(branchSHA),
		"too long": branchSHA + "0", "not hex": "g" + branchSHA[1:],
	} {
		answer := encode(t, map[string]any{"name": "main", "commit": map[string]any{"sha": sha}})
		reader, _ := newReader(t, map[string]reply{branchURI + "main": ok(answer)})
		if _, err := reader.BranchHead(t.Context(), repo, "main"); !errors.Is(err, github.ErrInvalidResponse) {
			t.Errorf("%s: err = %v, want ErrInvalidResponse", name, err)
		}
	}
	for name, answer := range map[string]string{"no commit": `{"name":"main"}`, "not json": `<html>`} {
		reader, _ := newReader(t, map[string]reply{branchURI + "main": ok([]byte(answer))})
		if _, err := reader.BranchHead(t.Context(), repo, "main"); !errors.Is(err, github.ErrInvalidResponse) {
			t.Errorf("%s: err = %v, want ErrInvalidResponse", name, err)
		}
	}
}

func TestBranchHeadRejectsInvalidArgumentsBeforeAnyRequest(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{})
	if _, err := reader.BranchHead(t.Context(), "octo-repo", "main"); !errors.Is(err, github.ErrInvalidArgument) {
		t.Errorf("bad repo: err = %v, want ErrInvalidArgument", err)
	}
	if _, err := reader.BranchHead(t.Context(), repo, ""); !errors.Is(err, github.ErrInvalidArgument) {
		t.Errorf("empty branch: err = %v, want ErrInvalidArgument", err)
	}
	if uris := gh.uris(); len(uris) != 0 {
		t.Errorf("requests = %v, want none", uris)
	}
}

func TestBranchHeadKeepsNotFoundMatchable(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{branchURI + "gone": {status: http.StatusNotFound, body: fixture(t, "not_found.json")}})
	if _, err := reader.BranchHead(t.Context(), repo, "gone"); !errors.Is(err, rest.ErrNotFound) {
		t.Errorf("err = %v, want rest.ErrNotFound", err)
	}
}

// botKey is generated once per test binary; nothing is read from disk.
var botKey = sync.OnceValue(func() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
})

func newBotApp(t *testing.T) appauth.App {
	t.Helper()
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(botKey())})
	app, err := appauth.NewApp(3792011, pemKey, time.Now)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return app
}

const (
	appURI     = "/app"
	botUserURI = "/users/suitewardq-spike%5Bbot%5D"
)

// botReader serves routes to both the App JWT call and the Reader's read token.
func botReader(t *testing.T, routes map[string]reply) (*github.Reader, *fakeGitHub, *httptest.Server) {
	t.Helper()
	gh := &fakeGitHub{t: t, routes: routes}
	srv, client := serve(t, gh)
	reader, err := github.NewReader(client)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return reader, gh, srv
}

func TestBotIdentityResolvesTheSlugThenTheBotAccount(t *testing.T) {
	reader, gh, srv := botReader(t, map[string]reply{appURI: ok(fixture(t, "app.json")), botUserURI: ok(fixture(t, "bot_user.json"))})

	bot, err := reader.BotIdentity(t.Context(), newBotApp(t), srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("BotIdentity: %v", err)
	}
	if bot.Login != "suitewardq-spike[bot]" || bot.ID != 340362958 {
		t.Errorf("bot = %+v, want suitewardq-spike[bot] with id 340362958", bot)
	}
	reqs := gh.requests()
	if len(reqs) != 2 || reqs[0].uri != appURI || reqs[1].uri != botUserURI {
		t.Fatalf("requests = %v, want GET /app then GET the bot user", gh.uris())
	}
	if !strings.HasPrefix(reqs[0].authorization, "Bearer ") || strings.Count(reqs[0].authorization, ".") != 2 {
		t.Error("GET /app did not carry a JWT")
	}
	if reqs[1].authorization != "Bearer fake-token" {
		t.Error("the users read did not use the reader's token")
	}
}

func TestBotIdentityRejectsAnAccountThatIsNotABot(t *testing.T) {
	for name, user := range map[string]string{
		"user type":     `{"login":"suitewardq-spike[bot]","id":340362958,"type":"User"}`,
		"no type":       `{"login":"suitewardq-spike[bot]","id":340362958}`,
		"no id":         `{"login":"suitewardq-spike[bot]","type":"Bot"}`,
		"other login":   `{"login":"someone-else[bot]","id":340362958,"type":"Bot"}`,
		"not json":      `<html>`,
		"missing login": `{"id":340362958,"type":"Bot"}`,
	} {
		reader, _, srv := botReader(t, map[string]reply{appURI: ok(fixture(t, "app.json")), botUserURI: ok([]byte(user))})
		if _, err := reader.BotIdentity(t.Context(), newBotApp(t), srv.Client(), srv.URL); !errors.Is(err, github.ErrInvalidResponse) {
			t.Errorf("%s: err = %v, want ErrInvalidResponse", name, err)
		}
	}
}

func TestBotIdentityAppFailureIsAStatusErrorWithoutTheJWT(t *testing.T) {
	reader, gh, srv := botReader(t, map[string]reply{appURI: {status: http.StatusUnauthorized, body: []byte(`{"message":"A JSON web token could not be decoded"}`)}})

	_, err := reader.BotIdentity(t.Context(), newBotApp(t), srv.Client(), srv.URL)
	var status *appauth.StatusError
	if !errors.As(err, &status) || status.Status != http.StatusUnauthorized {
		t.Fatalf("err = %v, want *appauth.StatusError with status 401", err)
	}
	jwt := strings.TrimPrefix(gh.requests()[0].authorization, "Bearer ")
	if jwt == "" || strings.Contains(err.Error(), jwt) {
		t.Errorf("error %q holds the JWT", err)
	}
	if uris := gh.uris(); len(uris) != 1 {
		t.Errorf("requests = %v, want only GET /app", uris)
	}
}

func TestBotIdentityUserReadFailureIsMatchable(t *testing.T) {
	reader, _, srv := botReader(t, map[string]reply{
		appURI:     ok(fixture(t, "app.json")),
		botUserURI: {status: http.StatusNotFound, body: fixture(t, "not_found.json")},
	})
	if _, err := reader.BotIdentity(t.Context(), newBotApp(t), srv.Client(), srv.URL); !errors.Is(err, rest.ErrNotFound) {
		t.Errorf("err = %v, want rest.ErrNotFound", err)
	}
}
