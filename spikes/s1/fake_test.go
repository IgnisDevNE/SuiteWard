package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeComment struct {
	id                     int64
	login, body, updatedAt string
}

// fakeGH answers the endpoints the program uses with recorded-shape JSON. Every
// GET carries an ETag derived from the body, so unchanged answers become 304s.
type fakeGH struct {
	open     bool
	head     string
	comments []fakeComment
	trees    map[string]string // tree sha -> JSON array of tree entries
	commits  map[string]string // commit sha -> JSON of GET /git/commits/{sha}
	prJSON   string            // GET /pulls/5
	checks   []string          // "<METHOD> <body>" of every check run request
}

func (g *fakeGH) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	reply := func(b string) {
		sum := sha256.Sum256([]byte(b))
		etag := fmt.Sprintf(`"%x"`, sum[:6])
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		_, _ = w.Write([]byte(b))
	}
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/access_tokens"):
		_, _ = w.Write([]byte(`{"token":"t","expires_at":"2999-01-01T00:00:00Z"}`))
	case strings.Contains(p, "/check-runs"):
		g.checks = append(g.checks, r.Method+" "+string(body))
		_, _ = w.Write([]byte(`{"id":99}`))
	case strings.HasSuffix(p, "/issues/5/comments"):
		var cs []string
		for _, c := range g.comments {
			cs = append(cs, fmt.Sprintf(`{"id":%d,"body":%q,"user":{"login":%q},"created_at":"2026-01-01T00:00:00Z","updated_at":%q}`, c.id, c.body, c.login, c.updatedAt))
		}
		reply("[" + strings.Join(cs, ",") + "]")
	case strings.Contains(p, "/git/trees/"):
		t, ok := g.trees[p[strings.LastIndex(p, "/")+1:]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		reply(`{"tree":` + t + `}`)
	case strings.Contains(p, "/git/commits/"):
		c, ok := g.commits[p[strings.LastIndex(p, "/")+1:]]
		if !ok {
			http.NotFound(w, r)
			return
		}
		reply(c)
	case strings.HasSuffix(p, "/pulls/5"):
		reply(g.prJSON)
	case strings.HasSuffix(p, "/pulls"):
		if g.open {
			reply(`[{"number":5,"head":{"sha":"` + g.head + `"}}]`)
		} else {
			reply(`[]`)
		}
	default:
		http.NotFound(w, r)
	}
}

type harness struct {
	t   *testing.T
	gh  *fakeGH
	a   *app
	out *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, gh: &fakeGH{open: true, head: "aaaa", trees: map[string]string{}, commits: map[string]string{}}, out: &bytes.Buffer{}}
	srv := httptest.NewServer(http.HandlerFunc(h.gh.handler))
	t.Cleanup(srv.Close)
	h.a = &app{
		cfg:       config{repo: "o/r", ownerLogin: "magalz", protectedPrefix: "tests/", checkName: "n", stateFile: filepath.Join(t.TempDir(), "state.json")},
		log:       &logger{w: h.out, start: time.Now()},
		st:        &state{Etags: map[string]string{}, Pulls: map[int]*pullState{}, Promotions: []json.RawMessage{}},
		published: map[int]string{}, started: time.Now(),
	}
	h.a.c = testClient(t, srv.URL, h.out)
	h.a.c.etags = h.a.st.Etags
	return h
}

func (h *harness) cycle() {
	h.t.Helper()
	if err := h.a.cycle(context.Background()); err != nil {
		h.t.Fatal(err)
	}
}

// lastCheck returns the most recent check run request body.
func (h *harness) lastCheck() string {
	h.t.Helper()
	if len(h.gh.checks) == 0 {
		h.t.Fatal("no check run request was made")
	}
	return h.gh.checks[len(h.gh.checks)-1]
}

func treeJSON(entries ...treeEntry) string {
	b, _ := json.Marshal(entries)
	return string(b)
}

func refOf(entries ...treeEntry) string {
	d, _ := protectedDigest(entries, "tests/")
	return d[:12]
}

func digestOf(entries ...treeEntry) string {
	d, _ := protectedDigest(entries, "tests/")
	return d
}
