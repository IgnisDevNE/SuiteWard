package github_test

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

const (
	repo        = "octo-org/octo-repo"
	commitSHA   = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
	pullsURI    = "/repos/octo-org/octo-repo/pulls?state=open&per_page=100&page="
	commentsURI = "/repos/octo-org/octo-repo/issues/42/comments?per_page=100&page="
	treeURI     = "/repos/octo-org/octo-repo/git/trees/" + commitSHA + "?recursive=1"
	blobsURI    = "/repos/octo-org/octo-repo/git/blobs/"

	declarationBlob = "d1e2f3a4b5c6d7e8f9a0b1c2d3e4f5a6b7c8d9e0"
	testFileBlob    = "b82e9a34624d830a4bd24b63ceb1deb0ad14c863"
	readmeBlob      = "aa11bb22cc33dd44ee55ff6600aa11bb22cc33dd"

	declarationContent = "version: 1\ninclude: [/tests/**]\n"
	testFileContent    = "package tests\n\nimport \"testing\"\n\nfunc TestProtectedBehaviour(t *testing.T) {\n\tt.Log(\"covered by the suite\")\n}\n"
)

// reply is what a route answers; a request carrying its etag gets a 304.
type reply struct {
	status int
	body   []byte
	etag   string
	header map[string]string
}

type request struct{ uri, ifNoneMatch, authorization string }

// fakeGitHub serves fixed routes by request URI and fails the test on any
// other request, so an unexpected page, query or blob read is caught.
type fakeGitHub struct {
	t      *testing.T
	routes map[string]reply

	mu   sync.Mutex
	seen []request
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	uri := r.URL.RequestURI()
	g.mu.Lock()
	g.seen = append(g.seen, request{uri: uri, ifNoneMatch: r.Header.Get("If-None-Match"), authorization: r.Header.Get("Authorization")})
	g.mu.Unlock()
	answer, found := g.routes[uri]
	if !found {
		g.t.Errorf("unexpected request %s", uri)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if answer.etag != "" {
		if r.Header.Get("If-None-Match") == answer.etag {
			if date, ok := answer.header["Date"]; ok { // a 304 carries its own Date too
				w.Header().Set("Date", date)
			}
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", answer.etag)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range answer.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(cmp.Or(answer.status, http.StatusOK))
	_, _ = w.Write(answer.body)
}

func (g *fakeGitHub) requests() []request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.seen)
}

func (g *fakeGitHub) uris() []string {
	var uris []string
	for _, r := range g.requests() {
		uris = append(uris, r.uri)
	}
	return uris
}

// newReader returns a reader over a real rest.Client talking to a fake GitHub.
func newReader(t *testing.T, routes map[string]reply) (*github.Reader, *fakeGitHub) {
	t.Helper()
	gh := &fakeGitHub{t: t, routes: routes}
	srv := httptest.NewServer(gh)
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
	reader, err := github.NewReader(client)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return reader, gh
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return body
}

func ok(body []byte) reply { return reply{body: body} }

// items decodes a list fixture into generic objects.
func items(t *testing.T, name string) []map[string]any {
	t.Helper()
	var list []map[string]any
	if err := json.Unmarshal(fixture(t, name), &list); err != nil {
		t.Fatalf("decode fixture %s: %v", name, err)
	}
	return list
}

func encode(t *testing.T, v any) []byte {
	t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return body
}

// generatedPage returns n copies of the first item of a list fixture whose key
// counts up from first: a full page in GitHub's shape.
func generatedPage(t *testing.T, name, key string, first, n int) []byte {
	t.Helper()
	page := make([]map[string]any, n)
	for i := range page {
		item := items(t, name)[0]
		item[key] = first + i
		page[i] = item
	}
	return encode(t, page)
}

// mutated returns a list fixture with its first item changed by change.
func mutated(t *testing.T, name string, change func(item map[string]any)) []byte {
	t.Helper()
	list := items(t, name)
	change(list[0])
	return encode(t, list)
}

func object(item map[string]any, key string) map[string]any {
	return item[key].(map[string]any)
}

var fixturePulls = []github.Pull{
	{
		Number: 7, HeadSHA: "0123456789abcdef0123456789abcdef01234567", HeadRef: "runner", HeadRepo: repo,
		BaseRef: "release/1.x", BaseSHA: "fedcba9876543210fedcba9876543210fedcba98",
		UpdatedAt: time.Date(2026, 9, 30, 9, 45, 12, 0, time.UTC),
	},
	{
		Number: 42, HeadSHA: "1f0e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c", HeadRef: "parser-tests", HeadRepo: repo,
		BaseRef: "main", BaseSHA: "a3b2c1d0e9f8a7b6c5d4e3f2a1b0c9d8e7f6a5b4",
		UpdatedAt: time.Date(2026, 10, 2, 14, 3, 41, 0, time.UTC),
	},
}

var fixtureComments = []github.Comment{
	{
		ID: 2002, AuthorLogin: "octocat", AuthorID: 583231, Body: "Looks right to me.",
		CreatedAt: time.Date(2026, 10, 1, 11, 20, 5, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 1, 11, 20, 5, 0, time.UTC),
	},
	{
		ID: 3001, AuthorLogin: "hubot", AuthorID: 480938, Body: "Second thought: keep the old case too.",
		CreatedAt: time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC),
	},
}

func samePull(a, b github.Pull) bool {
	return a.Number == b.Number && a.HeadSHA == b.HeadSHA && a.HeadRef == b.HeadRef && a.HeadRepo == b.HeadRepo &&
		a.BaseRef == b.BaseRef && a.BaseSHA == b.BaseSHA && a.UpdatedAt.Equal(b.UpdatedAt)
}

func sameComment(a, b github.Comment) bool {
	return a.ID == b.ID && a.AuthorLogin == b.AuthorLogin && a.AuthorID == b.AuthorID && a.Body == b.Body &&
		a.CreatedAt.Equal(b.CreatedAt) && a.UpdatedAt.Equal(b.UpdatedAt)
}

func pullNumbers(pulls []github.Pull) []int {
	numbers := make([]int, len(pulls))
	for i, p := range pulls {
		numbers[i] = p.Number
	}
	return numbers
}

func commentIDs(comments []github.Comment) []int64 {
	ids := make([]int64, len(comments))
	for i, c := range comments {
		ids[i] = c.ID
	}
	return ids
}

func TestNewReaderRejectsNilClient(t *testing.T) {
	if _, err := github.NewReader(nil); err == nil {
		t.Fatal("NewReader(nil) succeeded")
	}
}

func TestOpenPullsStopsOnAShortPage(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{pullsURI + "1": ok(fixture(t, "pulls.json"))})

	pulls, _, err := reader.OpenPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("OpenPulls: %v", err)
	}
	if !slices.EqualFunc(pulls, fixturePulls, samePull) {
		t.Errorf("OpenPulls = %+v, want %+v (sorted by number)", pulls, fixturePulls)
	}
	if got := gh.uris(); !slices.Equal(got, []string{pullsURI + "1"}) {
		t.Errorf("requests = %v, want only the first page", got)
	}
}

func TestOpenPullsRequestsTheNextPageAfterAFullOne(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{
		pullsURI + "1": ok(generatedPage(t, "pulls.json", "number", 101, 100)),
		pullsURI + "2": ok(fixture(t, "pulls.json")),
	})

	pulls, _, err := reader.OpenPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("OpenPulls: %v", err)
	}
	want := []int{7, 42}
	for n := 101; n <= 200; n++ {
		want = append(want, n)
	}
	if got := pullNumbers(pulls); !slices.Equal(got, want) {
		t.Errorf("numbers = %v, want %v", got, want)
	}
	if got := gh.uris(); !slices.Equal(got, []string{pullsURI + "1", pullsURI + "2"}) {
		t.Errorf("requests = %v, want pages 1 and 2", got)
	}
}

func TestOpenPullsEmptyFirstPageIsAnEmptySlice(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{pullsURI + "1": ok([]byte("[]\n"))})

	pulls, _, err := reader.OpenPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("OpenPulls: %v", err)
	}
	if pulls == nil || len(pulls) != 0 {
		t.Errorf("OpenPulls = %#v, want an empty, non-nil slice", pulls)
	}
}

func TestOpenPullsNotModifiedReturnsTheSameValues(t *testing.T) {
	const etag = `W/"pulls-page-1"`
	reader, gh := newReader(t, map[string]reply{pullsURI + "1": {body: fixture(t, "pulls.json"), etag: etag}})

	first, _, err := reader.OpenPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("first OpenPulls: %v", err)
	}
	second, _, err := reader.OpenPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("second OpenPulls: %v", err)
	}
	if !slices.EqualFunc(second, first, samePull) || !slices.EqualFunc(second, fixturePulls, samePull) {
		t.Errorf("after a 304 OpenPulls = %+v, want %+v", second, first)
	}
	if got := gh.requests(); len(got) != 2 || got[1].ifNoneMatch != etag {
		t.Errorf("requests = %+v, want a second, conditional request answered with 304", got)
	}
}

func TestOpenPullsRejectsAnIncompletePull(t *testing.T) {
	cases := map[string]func(item map[string]any){
		"zero number":        func(item map[string]any) { item["number"] = 0 },
		"missing number":     func(item map[string]any) { delete(item, "number") },
		"short head SHA":     func(item map[string]any) { object(item, "head")["sha"] = "1f0e2d3c" },
		"uppercase base SHA": func(item map[string]any) { object(item, "base")["sha"] = "A3B2C1D0E9F8A7B6C5D4E3F2A1B0C9D8E7F6A5B4" },
		"empty head ref":     func(item map[string]any) { object(item, "head")["ref"] = "" },
		"missing base ref":   func(item map[string]any) { delete(object(item, "base"), "ref") },
		"missing head":       func(item map[string]any) { delete(item, "head") },
		"null base":          func(item map[string]any) { item["base"] = nil },
		"missing updated_at": func(item map[string]any) { delete(item, "updated_at") },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			reader, _ := newReader(t, map[string]reply{pullsURI + "1": ok(mutated(t, "pulls.json", change))})
			pulls, _, err := reader.OpenPulls(t.Context(), repo)
			if !errors.Is(err, github.ErrInvalidResponse) {
				t.Fatalf("OpenPulls = %+v, %v; want ErrInvalidResponse", pulls, err)
			}
		})
	}
}

func TestPullCommentsWithoutSinceSendsNoSince(t *testing.T) {
	// The route matches the exact URI: a since parameter would be an unexpected request.
	reader, gh := newReader(t, map[string]reply{commentsURI + "1": ok(fixture(t, "comments.json"))})

	comments, _, err := reader.PullComments(t.Context(), repo, 42, time.Time{})
	if err != nil {
		t.Fatalf("PullComments: %v", err)
	}
	if !slices.EqualFunc(comments, fixtureComments, sameComment) {
		t.Errorf("PullComments = %+v, want %+v (sorted by ID)", comments, fixtureComments)
	}
	if got := gh.uris(); !slices.Equal(got, []string{commentsURI + "1"}) {
		t.Errorf("requests = %v, want only the first page without since", got)
	}
}

func TestPullCommentsSendsSinceInUTC(t *testing.T) {
	since := time.Date(2026, 10, 1, 14, 30, 0, 0, time.FixedZone("CEST", 2*60*60))
	reader, _ := newReader(t, map[string]reply{commentsURI + "1&since=2026-10-01T12:30:00Z": ok(fixture(t, "comments.json"))})

	comments, _, err := reader.PullComments(t.Context(), repo, 42, since)
	if err != nil {
		t.Fatalf("PullComments: %v", err)
	}
	if !slices.EqualFunc(comments, fixtureComments, sameComment) {
		t.Errorf("PullComments = %+v, want %+v", comments, fixtureComments)
	}
}

func TestPullCommentsRequestsTheNextPageAfterAFullOne(t *testing.T) {
	const since = "&since=2026-10-01T00:00:00Z"
	reader, gh := newReader(t, map[string]reply{
		commentsURI + "1" + since: ok(generatedPage(t, "comments.json", "id", 5001, 100)),
		commentsURI + "2" + since: ok(fixture(t, "comments.json")),
	})

	comments, _, err := reader.PullComments(t.Context(), repo, 42, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("PullComments: %v", err)
	}
	want := []int64{2002, 3001}
	for id := int64(5001); id <= 5100; id++ {
		want = append(want, id)
	}
	if got := commentIDs(comments); !slices.Equal(got, want) {
		t.Errorf("IDs = %v, want %v", got, want)
	}
	if got := gh.uris(); !slices.Equal(got, []string{commentsURI + "1" + since, commentsURI + "2" + since}) {
		t.Errorf("requests = %v, want pages 1 and 2 with since", got)
	}
}

func TestPullCommentsEmptyFirstPageIsAnEmptySlice(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{commentsURI + "1": ok([]byte("[]\n"))})

	comments, _, err := reader.PullComments(t.Context(), repo, 42, time.Time{})
	if err != nil {
		t.Fatalf("PullComments: %v", err)
	}
	if comments == nil || len(comments) != 0 {
		t.Errorf("PullComments = %#v, want an empty, non-nil slice", comments)
	}
}

func TestPullCommentsNotModifiedReturnsTheSameValues(t *testing.T) {
	const etag = `W/"comments-page-1"`
	reader, gh := newReader(t, map[string]reply{commentsURI + "1": {body: fixture(t, "comments.json"), etag: etag}})

	first, _, err := reader.PullComments(t.Context(), repo, 42, time.Time{})
	if err != nil {
		t.Fatalf("first PullComments: %v", err)
	}
	second, _, err := reader.PullComments(t.Context(), repo, 42, time.Time{})
	if err != nil {
		t.Fatalf("second PullComments: %v", err)
	}
	if !slices.EqualFunc(second, first, sameComment) || !slices.EqualFunc(second, fixtureComments, sameComment) {
		t.Errorf("after a 304 PullComments = %+v, want %+v", second, first)
	}
	if got := gh.requests(); len(got) != 2 || got[1].ifNoneMatch != etag {
		t.Errorf("requests = %+v, want a second, conditional request answered with 304", got)
	}
}

func TestPullCommentsRejectsAnIncompleteComment(t *testing.T) {
	cases := map[string]func(item map[string]any){
		"zero id":            func(item map[string]any) { item["id"] = 0 },
		"missing id":         func(item map[string]any) { delete(item, "id") },
		"null user":          func(item map[string]any) { item["user"] = nil },
		"missing user":       func(item map[string]any) { delete(item, "user") },
		"empty login":        func(item map[string]any) { object(item, "user")["login"] = "" },
		"zero user id":       func(item map[string]any) { object(item, "user")["id"] = 0 },
		"missing created_at": func(item map[string]any) { delete(item, "created_at") },
		"missing updated_at": func(item map[string]any) { delete(item, "updated_at") },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			reader, _ := newReader(t, map[string]reply{commentsURI + "1": ok(mutated(t, "comments.json", change))})
			comments, _, err := reader.PullComments(t.Context(), repo, 42, time.Time{})
			if !errors.Is(err, github.ErrInvalidResponse) {
				t.Fatalf("PullComments = %+v, %v; want ErrInvalidResponse", comments, err)
			}
		})
	}
}

var fixtureEntries = []inventory.Entry{
	{Path: ".suiteward.yml", Kind: inventory.File},
	{Path: "README.md", Kind: inventory.File},
	{Path: "docs/link", Kind: inventory.Symlink},
	{Path: "scripts/run.sh", Kind: inventory.File},
	{Path: "tests/a_test.go", Kind: inventory.File},
	{Path: "vendor/lib", Kind: inventory.Submodule},
}

func TestTreeMapsModesAndSkipsDirectories(t *testing.T) {
	// The fake offers an ETag: an uncached read must still never send If-None-Match.
	reader, gh := newReader(t, map[string]reply{treeURI: {body: fixture(t, "tree.json"), etag: `"tree"`}})

	for range 2 {
		tree, err := reader.Tree(t.Context(), repo, commitSHA)
		if err != nil {
			t.Fatalf("Tree: %v", err)
		}
		entries, err := tree.Entries(t.Context())
		if err != nil {
			t.Fatalf("Entries: %v", err)
		}
		if !slices.Equal(entries, fixtureEntries) {
			t.Errorf("Entries = %+v, want %+v", entries, fixtureEntries)
		}
	}
	got := gh.requests()
	if len(got) != 2 {
		t.Fatalf("requests = %+v, want two tree reads", got)
	}
	for _, r := range got {
		if r.ifNoneMatch != "" {
			t.Errorf("tree request carried If-None-Match %q", r.ifNoneMatch)
		}
	}
}

func TestTreeReadDecodesABlobOnlyWhenCalled(t *testing.T) {
	// Only the tree and one blob are routed: reading any other blob fails the test.
	reader, gh := newReader(t, map[string]reply{
		treeURI:                 ok(fixture(t, "tree.json")),
		blobsURI + testFileBlob: {body: fixture(t, "blob_a_test_go.json"), etag: `"blob"`},
	})

	tree, err := reader.Tree(t.Context(), repo, commitSHA)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if got := gh.uris(); !slices.Equal(got, []string{treeURI}) {
		t.Fatalf("requests after Tree = %v, want only the tree", got)
	}
	for range 2 {
		content, err := tree.Read(t.Context(), "tests/a_test.go")
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if string(content) != testFileContent {
			t.Errorf("Read = %q, want %q", content, testFileContent)
		}
	}
	got := gh.requests()
	if uris := gh.uris(); !slices.Equal(uris, []string{treeURI, blobsURI + testFileBlob, blobsURI + testFileBlob}) {
		t.Fatalf("requests = %v, want the tree and the blob twice", uris)
	}
	for _, r := range got[1:] {
		if r.ifNoneMatch != "" {
			t.Errorf("blob request carried If-None-Match %q", r.ifNoneMatch)
		}
	}
}

func TestTreeReadRejectsPathsWithoutAFileBlob(t *testing.T) {
	for _, path := range []string{"docs/link", "vendor/lib", "tests", "missing.txt"} {
		t.Run(path, func(t *testing.T) {
			reader, gh := newReader(t, map[string]reply{treeURI: ok(fixture(t, "tree.json"))})
			tree, err := reader.Tree(t.Context(), repo, commitSHA)
			if err != nil {
				t.Fatalf("Tree: %v", err)
			}
			if content, err := tree.Read(t.Context(), path); err == nil {
				t.Fatalf("Read(%q) = %q, want an error", path, content)
			}
			if got := gh.uris(); !slices.Equal(got, []string{treeURI}) {
				t.Errorf("requests = %v, want no blob read", got)
			}
		})
	}
}

func TestTreeReadRejectsABlobItCannotDecode(t *testing.T) {
	cases := map[string]string{
		"utf-8 encoding": `{"sha":"` + readmeBlob + `","size":5,"content":"hello","encoding":"utf-8"}`,
		"no encoding":    `{"sha":"` + readmeBlob + `","size":5,"content":"aGVsbG8="}`,
		"invalid base64": `{"sha":"` + readmeBlob + `","size":5,"content":"!!not base64!!","encoding":"base64"}`,
	}
	for name, blob := range cases {
		t.Run(name, func(t *testing.T) {
			reader, _ := newReader(t, map[string]reply{
				treeURI:               ok(fixture(t, "tree.json")),
				blobsURI + readmeBlob: ok([]byte(blob)),
			})
			tree, err := reader.Tree(t.Context(), repo, commitSHA)
			if err != nil {
				t.Fatalf("Tree: %v", err)
			}
			if content, err := tree.Read(t.Context(), "README.md"); !errors.Is(err, github.ErrInvalidResponse) {
				t.Fatalf("Read = %q, %v; want ErrInvalidResponse", content, err)
			}
		})
	}
}

// treeWith returns a tree answer listing one entry.
func treeWith(path, mode, kind, sha string) []byte {
	return []byte(`{"sha":"9c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d","tree":[{"path":"` + path +
		`","mode":"` + mode + `","type":"` + kind + `","sha":"` + sha + `"}],"truncated":false}`)
}

func TestTreeRejectsAnEntryItCannotType(t *testing.T) {
	const sha = "77aa77aa77aa77aa77aa77aa77aa77aa77aa77aa"
	cases := []struct{ name, mode, kind, sha string }{
		{"unknown mode", "100664", "blob", sha},
		{"symlink mode as a commit", "120000", "commit", sha},
		{"submodule mode as a blob", "160000", "blob", sha},
		{"file mode as a tree", "100644", "tree", sha},
		{"directory mode as a blob", "040000", "blob", sha},
		{"unknown type", "100644", "tag", sha},
		{"blob SHA that is not a SHA", "100644", "blob", "../../../pulls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reader, _ := newReader(t, map[string]reply{treeURI: ok(treeWith("tests/odd_entry", tc.mode, tc.kind, tc.sha))})
			tree, err := reader.Tree(t.Context(), repo, commitSHA)
			if !errors.Is(err, inventory.ErrInvalidTree) {
				t.Fatalf("Tree = %v, %v; want ErrInvalidTree", tree, err)
			}
			if !strings.Contains(err.Error(), "tests/odd_entry") {
				t.Errorf("error %q does not name the entry", err)
			}
		})
	}
}

func TestTreeRejectsATruncatedTree(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{treeURI: ok(fixture(t, "tree_truncated.json"))})

	tree, err := reader.Tree(t.Context(), repo, commitSHA)
	if !errors.Is(err, inventory.ErrInvalidTree) || !errors.Is(err, github.ErrTreeTruncated) {
		t.Fatalf("Tree = %v, %v; want ErrInvalidTree and ErrTreeTruncated", tree, err)
	}
}

func TestInvalidTreesAreNotTruncatedTrees(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{treeURI: ok(treeWith("tests/odd_entry", "100664", "blob", readmeBlob))})

	_, err := reader.Tree(t.Context(), repo, commitSHA)
	if !errors.Is(err, inventory.ErrInvalidTree) || errors.Is(err, github.ErrTreeTruncated) {
		t.Fatalf("Tree err = %v; want ErrInvalidTree only", err)
	}
}

func TestInvalidArgumentsAreRejectedBeforeAnyRequest(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{})
	ctx := t.Context()
	repos := []string{
		"", "octo-org", "octo-org/", "/octo-repo", "/", "octo-org/octo-repo/extra", "octo-org//octo-repo",
		"octo org/octo-repo", "octo-org/octo-repo?page=2", "octo-org/octo-repo#x", "octo-org/octo%2Frepo",
		"../octo-repo", "octo-org/..", "octo-org/.", "ôcto/repo",
	}
	for _, bad := range repos {
		if _, _, err := reader.OpenPulls(ctx, bad); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("OpenPulls(%q) = %v, want ErrInvalidArgument", bad, err)
		}
		if _, _, err := reader.PullComments(ctx, bad, 42, time.Time{}); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("PullComments(%q) = %v, want ErrInvalidArgument", bad, err)
		}
		if _, err := reader.Tree(ctx, bad, commitSHA); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("Tree(%q) = %v, want ErrInvalidArgument", bad, err)
		}
	}
	for _, number := range []int{0, -1} {
		if _, _, err := reader.PullComments(ctx, repo, number, time.Time{}); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("PullComments(number %d) = %v, want ErrInvalidArgument", number, err)
		}
	}
	shas := []string{
		"", "c0ffee", strings.ToUpper(commitSHA), commitSHA + "0", commitSHA[1:],
		"g" + commitSHA[1:], "../" + commitSHA[3:], "HEAD", "main",
	}
	for _, bad := range shas {
		if _, err := reader.Tree(ctx, repo, bad); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("Tree(sha %q) = %v, want ErrInvalidArgument", bad, err)
		}
	}
	if got := gh.uris(); len(got) != 0 {
		t.Errorf("requests = %v, want none", got)
	}
}

func TestNotFoundStaysMatchable(t *testing.T) {
	notFound := reply{status: http.StatusNotFound, body: fixture(t, "not_found.json")}
	reader, _ := newReader(t, map[string]reply{
		pullsURI + "1":    notFound,
		commentsURI + "1": notFound,
		treeURI:           notFound,
	})
	if _, _, err := reader.OpenPulls(t.Context(), repo); !errors.Is(err, rest.ErrNotFound) {
		t.Errorf("OpenPulls error = %v, want rest.ErrNotFound", err)
	}
	if _, _, err := reader.PullComments(t.Context(), repo, 42, time.Time{}); !errors.Is(err, rest.ErrNotFound) {
		t.Errorf("PullComments error = %v, want rest.ErrNotFound", err)
	}
	if _, err := reader.Tree(t.Context(), repo, commitSHA); !errors.Is(err, rest.ErrNotFound) {
		t.Errorf("Tree error = %v, want rest.ErrNotFound", err)
	}

	reader, _ = newReader(t, map[string]reply{treeURI: ok(fixture(t, "tree.json")), blobsURI + readmeBlob: notFound})
	tree, err := reader.Tree(t.Context(), repo, commitSHA)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if _, err := tree.Read(t.Context(), "README.md"); !errors.Is(err, rest.ErrNotFound) {
		t.Errorf("Read error = %v, want rest.ErrNotFound", err)
	}
}

func TestUndecodableAnswerNamesThePathNotTheBody(t *testing.T) {
	const marker = "body-marker-31415"
	garbage := ok([]byte(marker + " <html>proxy error</html>"))
	reader, _ := newReader(t, map[string]reply{
		pullsURI + "1":          garbage,
		commentsURI + "1":       garbage,
		treeURI:                 garbage,
		blobsURI + testFileBlob: garbage,
	})
	check := func(what, path string, err error) {
		t.Helper()
		switch {
		case !errors.Is(err, github.ErrInvalidResponse):
			t.Errorf("%s error = %v, want ErrInvalidResponse", what, err)
		case !strings.Contains(err.Error(), path):
			t.Errorf("%s error %q does not name %s", what, err, path)
		case strings.Contains(err.Error(), marker):
			t.Errorf("%s error %q includes the body", what, err)
		}
	}
	_, _, err := reader.OpenPulls(t.Context(), repo)
	check("OpenPulls", pullsURI+"1", err)
	_, _, err = reader.PullComments(t.Context(), repo, 42, time.Time{})
	check("PullComments", commentsURI+"1", err)
	_, err = reader.Tree(t.Context(), repo, commitSHA)
	check("Tree", treeURI, err)

	reader, _ = newReader(t, map[string]reply{treeURI: ok(fixture(t, "tree.json")), blobsURI + testFileBlob: garbage})
	tree, err := reader.Tree(t.Context(), repo, commitSHA)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	_, err = tree.Read(t.Context(), "tests/a_test.go")
	check("Read", blobsURI+testFileBlob, err)
}

func TestTreeFeedsInventoryBuild(t *testing.T) {
	// README.md and scripts/run.sh are outside the declared scope: their blobs
	// are not routed, so reading them fails the test.
	reader, gh := newReader(t, map[string]reply{
		treeURI:                    ok(fixture(t, "tree.json")),
		blobsURI + declarationBlob: ok(fixture(t, "blob_declaration.json")),
		blobsURI + testFileBlob:    ok(fixture(t, "blob_a_test_go.json")),
	})
	tree, err := reader.Tree(t.Context(), repo, commitSHA)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}

	inv, err := inventory.Build(t.Context(), tree)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	want := []artifact.Entry{
		{Path: ".suiteward.yml", Content: artifact.Hash([]byte(declarationContent))},
		{Path: "tests/a_test.go", Content: artifact.Hash([]byte(testFileContent))},
	}
	if got := inv.Manifest().Entries(); !slices.Equal(got, want) {
		t.Errorf("manifest = %+v, want %+v", got, want)
	}
	if got := len(gh.requests()); got != 3 {
		t.Errorf("%d requests, want the tree and two blobs: %v", got, gh.uris())
	}
}

func TestTreeRejectsAnAnswerWithoutItsList(t *testing.T) {
	cases := map[string]string{
		"no tree key": `{"sha":"9c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d","truncated":false}`,
		"null tree":   `{"sha":"9c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d","tree":null,"truncated":false}`,
		"null answer": `null`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			reader, _ := newReader(t, map[string]reply{treeURI: ok([]byte(body))})
			tree, err := reader.Tree(t.Context(), repo, commitSHA)
			if !errors.Is(err, github.ErrInvalidResponse) {
				t.Fatalf("Tree = %v, %v; want ErrInvalidResponse", tree, err)
			}
		})
	}
}

func TestTreeAcceptsAnEmptyList(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{treeURI: ok([]byte(`{"sha":"9c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d","tree":[],"truncated":false}`))})

	tree, err := reader.Tree(t.Context(), repo, commitSHA)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	if entries, err := tree.Entries(t.Context()); err != nil || len(entries) != 0 {
		t.Errorf("Entries = %+v, %v; want none", entries, err)
	}
}

func TestListingsRejectANullPage(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{
		pullsURI + "1":    ok([]byte("null")),
		commentsURI + "1": ok([]byte("null")),
	})
	if pulls, _, err := reader.OpenPulls(t.Context(), repo); !errors.Is(err, github.ErrInvalidResponse) {
		t.Errorf("OpenPulls = %+v, %v; want ErrInvalidResponse", pulls, err)
	}
	if comments, _, err := reader.PullComments(t.Context(), repo, 42, time.Time{}); !errors.Is(err, github.ErrInvalidResponse) {
		t.Errorf("PullComments = %+v, %v; want ErrInvalidResponse", comments, err)
	}
}

// fullPages routes pages 1 through last of a listing to the same full page.
func fullPages(uri string, page []byte, last int) map[string]reply {
	routes := map[string]reply{}
	for p := 1; p <= last; p++ {
		routes[uri+strconv.Itoa(p)] = ok(page)
	}
	return routes
}

func TestListingsStopAfterTheirPageLimit(t *testing.T) {
	// Pages 1 through 100 are routed; a 101st request fails the fake.
	reader, gh := newReader(t, map[string]reply{})
	for uri, reply := range fullPages(pullsURI, generatedPage(t, "pulls.json", "number", 1, 100), 100) {
		gh.routes[uri] = reply
	}
	for uri, reply := range fullPages(commentsURI, generatedPage(t, "comments.json", "id", 1, 100), 100) {
		gh.routes[uri] = reply
	}

	pulls, _, err := reader.OpenPulls(t.Context(), repo)
	if !errors.Is(err, github.ErrInvalidResponse) {
		t.Errorf("OpenPulls = %d pulls, %v; want ErrInvalidResponse", len(pulls), err)
	} else if !strings.Contains(err.Error(), "/pulls") {
		t.Errorf("error %q does not name the path", err)
	}
	if got := len(gh.requests()); got != 100 {
		t.Errorf("OpenPulls sent %d requests, want 100", got)
	}

	comments, _, err := reader.PullComments(t.Context(), repo, 42, time.Time{})
	if !errors.Is(err, github.ErrInvalidResponse) {
		t.Errorf("PullComments = %d comments, %v; want ErrInvalidResponse", len(comments), err)
	} else if !strings.Contains(err.Error(), "/comments") {
		t.Errorf("error %q does not name the path", err)
	}
	if got := len(gh.requests()); got != 200 {
		t.Errorf("after PullComments %d requests in all, want 200", got)
	}
}

func TestOpenPullsReturnsEachNumberOnceTheLaterWinning(t *testing.T) {
	// Pull 100 moved onto page 2 while listing and changed in between.
	later := items(t, "pulls.json")[0]
	later["number"] = 100
	object(later, "head")["ref"] = "later"
	next := items(t, "pulls.json")[0]
	next["number"] = 101
	reader, _ := newReader(t, map[string]reply{
		pullsURI + "1": ok(generatedPage(t, "pulls.json", "number", 1, 100)),
		pullsURI + "2": ok(encode(t, []map[string]any{later, next})),
	})

	pulls, _, err := reader.OpenPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("OpenPulls: %v", err)
	}
	var want []int
	for n := 1; n <= 101; n++ {
		want = append(want, n)
	}
	if got := pullNumbers(pulls); !slices.Equal(got, want) {
		t.Errorf("numbers = %v, want 1 to 101 once each", got)
	}
	if got := pulls[99].HeadRef; got != "later" {
		t.Errorf("pull 100 head ref = %q, want the later occurrence", got)
	}
}

func TestPullCommentsReturnsEachIDOnceTheLaterWinning(t *testing.T) {
	later := items(t, "comments.json")[0]
	later["id"] = 100
	later["body"] = "later"
	next := items(t, "comments.json")[0]
	next["id"] = 101
	reader, _ := newReader(t, map[string]reply{
		commentsURI + "1": ok(generatedPage(t, "comments.json", "id", 1, 100)),
		commentsURI + "2": ok(encode(t, []map[string]any{later, next})),
	})

	comments, _, err := reader.PullComments(t.Context(), repo, 42, time.Time{})
	if err != nil {
		t.Fatalf("PullComments: %v", err)
	}
	var want []int64
	for id := int64(1); id <= 101; id++ {
		want = append(want, id)
	}
	if got := commentIDs(comments); !slices.Equal(got, want) {
		t.Errorf("IDs = %v, want 1 to 101 once each", got)
	}
	if got := comments[99].Body; got != "later" {
		t.Errorf("comment 100 body = %q, want the later occurrence", got)
	}
}

func TestTreeWithADuplicatePathIsRejectedByInventory(t *testing.T) {
	const sha = "77aa77aa77aa77aa77aa77aa77aa77aa77aa77aa"
	entry := `{"path":"tests/a_test.go","mode":"100644","type":"blob","sha":"` + sha + `"}`
	reader, _ := newReader(t, map[string]reply{treeURI: ok([]byte(`{"tree":[` + entry + `,` + entry + `],"truncated":false}`))})
	tree, err := reader.Tree(t.Context(), repo, commitSHA)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}

	if inv, err := inventory.Build(t.Context(), tree); !errors.Is(err, inventory.ErrInvalidTree) {
		t.Errorf("Build = %+v, %v; want ErrInvalidTree", inv, err)
	}
}

func httpDate(t time.Time) string { return t.UTC().Format(http.TimeFormat) }

// A listing reports the Date of its first page, how many requests it sent and whether any page changed.
func TestOpenPullsReportsTheFirstPagesDateTheRequestsAndWhetherItChanged(t *testing.T) {
	first, second := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 16, 12, 0, 3, 0, time.UTC)
	reader, _ := newReader(t, map[string]reply{
		pullsURI + "1": {body: generatedPage(t, "pulls.json", "number", 101, 100), header: map[string]string{"Date": httpDate(first)}},
		pullsURI + "2": {body: fixture(t, "pulls.json"), header: map[string]string{"Date": httpDate(second)}},
	})

	_, info, err := reader.OpenPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("OpenPulls: %v", err)
	}
	if want := (github.ReadInfo{Date: first, Requests: 2, Changed: true}); !info.Date.Equal(want.Date) || info.Requests != want.Requests || info.Changed != want.Changed {
		t.Errorf("info = %+v, want %+v (the first page's Date)", info, want)
	}
}

func TestOpenPullsServedEntirelyByNotModifiedDidNotChange(t *testing.T) {
	date := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	reader, _ := newReader(t, map[string]reply{
		pullsURI + "1": {body: generatedPage(t, "pulls.json", "number", 101, 100), etag: `W/"p1"`, header: map[string]string{"Date": httpDate(date)}},
		pullsURI + "2": {body: fixture(t, "pulls.json"), etag: `W/"p2"`, header: map[string]string{"Date": httpDate(date)}},
	})
	_, cold, err := reader.OpenPulls(t.Context(), repo)
	if err != nil || !cold.Changed || cold.Requests != 2 {
		t.Fatalf("first listing: info %+v, error %v; want 2 requests that changed", cold, err)
	}

	pulls, warm, err := reader.OpenPulls(t.Context(), repo)
	if err != nil {
		t.Fatalf("second OpenPulls: %v", err)
	}
	if len(pulls) != 102 || warm.Changed || warm.Requests != 2 || !warm.Date.Equal(date) {
		t.Errorf("after 304s: %d pulls, info %+v; want 102 pulls, 2 requests, unchanged, Date %v", len(pulls), warm, date)
	}
}

func TestPullCommentsReportsTheFirstPagesDateTheRequestsAndWhetherItChanged(t *testing.T) {
	date := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	reader, _ := newReader(t, map[string]reply{commentsURI + "1": {body: fixture(t, "comments.json"), etag: `W/"c1"`, header: map[string]string{"Date": httpDate(date)}}})

	_, cold, err := reader.PullComments(t.Context(), repo, 42, time.Time{})
	if err != nil || !cold.Date.Equal(date) || cold.Requests != 1 || !cold.Changed {
		t.Fatalf("first listing: info %+v, error %v; want Date %v, 1 request, changed", cold, err, date)
	}
	_, warm, err := reader.PullComments(t.Context(), repo, 42, time.Time{})
	if err != nil || !warm.Date.Equal(date) || warm.Requests != 1 || warm.Changed {
		t.Fatalf("second listing: info %+v, error %v; want Date %v, 1 request, unchanged", warm, err, date)
	}
}

// A listing that fails still reports the requests it sent, so a cut cycle can be paced by what it cost.
func TestAFailedListingReportsTheRequestsItSent(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{
		pullsURI + "1": ok(generatedPage(t, "pulls.json", "number", 101, 100)),
		pullsURI + "2": {status: http.StatusBadGateway, body: []byte("down")},
	})

	_, info, err := reader.OpenPulls(t.Context(), repo)

	if err == nil || info.Requests != 2 {
		t.Errorf("OpenPulls = info %+v, error %v; want the failure with the 2 requests sent", info, err)
	}
}

// A token failure happens before any request is sent, so the listing does not count it.
func TestAListingDoesNotCountARequestThatFailedToGetAToken(t *testing.T) {
	budget, err := rest.NewBudget(0.5, time.Now)
	if err != nil {
		t.Fatalf("NewBudget: %v", err)
	}
	token := func(context.Context) (string, string, error) { return "", "", errors.New("the token endpoint is down") }
	client, err := rest.NewClient(token, http.DefaultClient, "http://127.0.0.1:1", budget)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	reader, err := github.NewReader(client)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	_, info, err := reader.OpenPulls(t.Context(), repo)

	if err == nil || info.Requests != 0 {
		t.Fatalf("OpenPulls = %+v, %v; want an error and no request counted", info, err)
	}
}

const filesURI = "/repos/octo-org/octo-repo/pulls/42/files?per_page=100&page="

func TestPullFilesReturnsPathStatusAndPatch(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{filesURI + "1": ok(fixture(t, "pull_files.json"))})

	got, err := reader.PullFiles(t.Context(), repo, 42)
	if err != nil {
		t.Fatalf("PullFiles: %v", err)
	}
	want := []github.PullFile{
		{Path: "internal/core/engine.go", Status: "modified", Patch: "@@ -10,6 +10,7 @@ func Run() {\n a\n b\n+c\n d\n@@ -40 +41,2 @@ func Stop() {\n-x\n+y\n+z"},
		{Path: "docs/old.md", Status: "removed", Patch: "@@ -1,2 +0,0 @@\n-one\n-two"},
		{Path: "assets/logo.png", Status: "renamed"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("PullFiles = %+v, want %+v (a file without a patch keeps an empty Patch)", got, want)
	}
	if uris := gh.uris(); !slices.Equal(uris, []string{filesURI + "1"}) {
		t.Errorf("requests = %v, want the first page only", uris)
	}
}

func TestPullFilesPagesThroughTheListing(t *testing.T) {
	page := make([]map[string]any, 100)
	for i := range page {
		page[i] = map[string]any{"filename": "f" + strconv.Itoa(i), "status": "added"}
	}
	reader, gh := newReader(t, map[string]reply{
		filesURI + "1": ok(encode(t, page)),
		filesURI + "2": ok(fixture(t, "pull_files.json")),
	})

	got, err := reader.PullFiles(t.Context(), repo, 42)
	if err != nil {
		t.Fatalf("PullFiles: %v", err)
	}
	if len(got) != 103 || got[0].Path != "f0" || got[100].Path != "internal/core/engine.go" {
		t.Errorf("got %d files, want the 100 of page 1 then the 3 of page 2 in listing order", len(got))
	}
	if uris := gh.uris(); !slices.Equal(uris, []string{filesURI + "1", filesURI + "2"}) {
		t.Errorf("requests = %v, want pages 1 and 2", uris)
	}
}

func TestPullFilesEmptyListingIsAnEmptySlice(t *testing.T) {
	reader, _ := newReader(t, map[string]reply{filesURI + "1": ok([]byte("[]"))})
	if got, err := reader.PullFiles(t.Context(), repo, 42); err != nil || got == nil || len(got) != 0 {
		t.Errorf("PullFiles = %#v, %v; want an empty, non-nil slice", got, err)
	}
}

func TestPullFilesRejectsInvalidArgumentsBeforeAnyRequest(t *testing.T) {
	reader, gh := newReader(t, map[string]reply{})
	for name, call := range map[string]func() error{
		"repo":     func() error { _, err := reader.PullFiles(t.Context(), "octo-repo", 42); return err },
		"zero":     func() error { _, err := reader.PullFiles(t.Context(), repo, 0); return err },
		"negative": func() error { _, err := reader.PullFiles(t.Context(), repo, -1); return err },
	} {
		if err := call(); !errors.Is(err, github.ErrInvalidArgument) {
			t.Errorf("bad %s: err = %v, want ErrInvalidArgument", name, err)
		}
	}
	if uris := gh.uris(); len(uris) != 0 {
		t.Errorf("requests = %v, want none", uris)
	}
}

func TestPullFilesRejectsAnInvalidAnswer(t *testing.T) {
	for name, body := range map[string][]byte{
		"missing filename": mutated(t, "pull_files.json", func(f map[string]any) { delete(f, "filename") }),
		"empty filename":   mutated(t, "pull_files.json", func(f map[string]any) { f["filename"] = "" }),
		"missing status":   mutated(t, "pull_files.json", func(f map[string]any) { delete(f, "status") }),
		"not a list":       []byte(`{"message":"x"}`),
	} {
		t.Run(name, func(t *testing.T) {
			reader, _ := newReader(t, map[string]reply{filesURI + "1": ok(body)})
			if got, err := reader.PullFiles(t.Context(), repo, 42); !errors.Is(err, github.ErrInvalidResponse) {
				t.Fatalf("PullFiles = %+v, %v; want ErrInvalidResponse", got, err)
			}
		})
	}
}

func TestNewLineRanges(t *testing.T) {
	cases := []struct {
		name  string
		patch string
		want  []github.LineRange
	}{
		{"no patch", "", nil},
		{"one hunk", "@@ -10,6 +10,7 @@ func Run() {\n a\n+b", []github.LineRange{{Start: 10, End: 16}}},
		{"count defaults to one", "@@ -40 +41 @@\n-x\n+y", []github.LineRange{{Start: 41, End: 41}}},
		{"multiple hunks", "@@ -1,2 +1,3 @@\n a\n@@ -40 +41,2 @@ func Stop() {\n-x\n+y", []github.LineRange{{Start: 1, End: 3}, {Start: 41, End: 42}}},
		{"pure deletion has no new-side range", "@@ -1,2 +0,0 @@\n-one\n-two", nil},
		{"deletion hunk between additions", "@@ -5,2 +5,0 @@\n-a\n-b\n@@ -20 +18,2 @@\n+c", []github.LineRange{{Start: 18, End: 19}}},
		{"content that looks like a header is not one", "@@ -1 +1 @@\n+@@ not a header", []github.LineRange{{Start: 1, End: 1}}},
	}
	for _, c := range cases {
		got, err := github.PullFile{Patch: c.patch}.NewLineRanges()
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("%s: NewLineRanges = %v, %v; want %v", c.name, got, err, c.want)
		}
	}
}

func TestNewLineRangesRejectsAMalformedHeader(t *testing.T) {
	for name, patch := range map[string]string{
		"no ranges":       "@@ @@\n+x",
		"missing new":     "@@ -1,2 @@\n+x",
		"not numbers":     "@@ -a,b +c,d @@\n+x",
		"unterminated":    "@@ -1,2 +1,2\n+x",
		"negative count":  "@@ -1,2 +1,-2 @@\n+x",
		"overflowing":     "@@ -1 +99999999999999999999 @@\n+x",
		"bad second hunk": "@@ -1 +1 @@\n+x\n@@ -2 +b @@\n+y",
	} {
		if got, err := (github.PullFile{Patch: patch}).NewLineRanges(); err == nil {
			t.Errorf("%s: NewLineRanges = %v, want an error", name, got)
		}
	}
}
