package prefetch

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// passesTheBound reports whether err refuses a fetch for passing the byte bound: deterministic and install-failed (runtime contract, reasons),
// and neither the too-large of an unpacked size nor a counted fetch failure.
func passesTheBound(err error) bool {
	return errors.Is(err, ErrInstallFailed) && !errors.Is(err, ErrTooLarge) && !errors.Is(err, ErrFetchFailed)
}

// recorded is one entry the sink took, read to the end as a tar writer would.
type recorded struct {
	name string
	size int64
	data []byte
}

type memSink struct {
	entries []recorded
	fail    error
}

func (s *memSink) Add(name string, size int64, r io.Reader) error {
	if s.fail != nil {
		return s.fail
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, size))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("entry %s got %d of its %d bytes", name, n, size)
	}
	s.entries = append(s.entries, recorded{name, size, buf.Bytes()})
	return nil
}

type sinkFunc func(name string, size int64, r io.Reader) error

func (f sinkFunc) Add(name string, size int64, r io.Reader) error { return f(name, size, r) }

func planOf(names ...string) Plan {
	p := Plan{}
	for _, n := range names {
		p.Files = append(p.Files, File{Name: n})
	}
	return p
}

func newTLS(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// fakeProxy serves its files with a Content-Length and remembers every request line.
type fakeProxy struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
}

func newFakeProxy(t *testing.T, files map[string][]byte) *fakeProxy {
	t.Helper()
	p := &fakeProxy{}
	p.Server = newTLS(t, func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests = append(p.requests, r.Method+" "+r.URL.Path)
		p.mu.Unlock()
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	})
	return p
}

func (p *fakeProxy) asked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.requests)
}

// clientFor is the real Client of the package, trusting the test server's certificate. A redirect host replaces storage.googleapis.com, because the
// test servers have no such name.
func clientFor(t *testing.T, srv *httptest.Server, redirectHost string) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	c := Client(10 * time.Second)
	c.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	if redirectHost != "" {
		c.CheckRedirect = allowRedirectTo(redirectHost)
	}
	return c
}

func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

// zipOf builds a zip of stored files of the given sizes; the size of the files once extracted is their sum.
func zipOf(t *testing.T, sizes ...int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, n := range sizes {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("example.com/m@v1.0.0/file%03d.txt", i), Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(bytes.Repeat([]byte("x"), n)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const (
	modName = "a.example/m/@v/v1.0.0.mod"
	zipName = "a.example/m/@v/v1.0.0.zip"
)

var modBody = []byte("module a.example/m\n")

func TestFetchStreamsEachFileIntoTheSinkWithItsLengthAndSizesTheZips(t *testing.T) {
	zipBody := zipOf(t, 100, 250)
	proxy := newFakeProxy(t, map[string][]byte{modName: modBody, zipName: zipBody})
	sink := &memSink{}
	stats, err := Fetch(context.Background(), clientFor(t, proxy.Server, ""), proxy.URL, planOf(modName, zipName), sink, Limits{Bytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	want := []recorded{{modName, int64(len(modBody)), modBody}, {zipName, int64(len(zipBody)), zipBody}}
	if len(sink.entries) != 2 {
		t.Fatalf("the sink took %d entries, want 2", len(sink.entries))
	}
	for i, w := range want {
		g := sink.entries[i]
		if g.name != w.name || g.size != w.size || !bytes.Equal(g.data, w.data) {
			t.Errorf("entry %d is %q (%d bytes), want %q (%d bytes) with the proxy's bytes", i, g.name, g.size, w.name, w.size)
		}
	}
	if wantStats := (Stats{Files: 2, Bytes: int64(len(modBody) + len(zipBody)), Unpacked: 350}); stats != wantStats {
		t.Errorf("stats = %+v, want %+v", stats, wantStats)
	}
	// Only the configured base is asked, for each planned file and nothing else.
	if got, want := proxy.asked(), []string{"GET /" + modName, "GET /" + zipName}; !slices.Equal(got, want) {
		t.Errorf("requests = %q, want %q", got, want)
	}
}

func TestFetchStreamsABodyIntoTheSinkBeforeItEnds(t *testing.T) {
	body := bytes.Repeat([]byte("z"), 2000)
	half := len(body) / 2
	firstHalfSeen := make(chan struct{})
	var seenBeforeTheRest atomic.Bool
	srv := newTLS(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body[:half])
		w.(http.Flusher).Flush()
		select {
		case <-firstHalfSeen:
			seenBeforeTheRest.Store(true)
		case <-time.After(5 * time.Second): // a hang guard: a buffering Fetch never reaches the sink before the end of the body
		}
		_, _ = w.Write(body[half:])
	})
	sink := sinkFunc(func(name string, size int64, r io.Reader) error {
		if size != int64(len(body)) {
			t.Errorf("size = %d, want %d: the length is known before the first byte", size, len(body))
		}
		if _, err := io.ReadFull(r, make([]byte, half)); err != nil {
			return err
		}
		close(firstHalfSeen)
		_, err := io.Copy(io.Discard, r)
		return err
	})
	if _, err := Fetch(context.Background(), clientFor(t, srv, ""), srv.URL, planOf(modName), sink, Limits{Bytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if !seenBeforeTheRest.Load() {
		t.Error("the sink did not get the first half before the proxy sent the second: the body was buffered although its length was known")
	}
}

func TestFetchBuffersAFileOnlyWhenTheProxyGivesNoLength(t *testing.T) {
	body := bytes.Repeat([]byte("c"), 3000)
	var declared atomic.Int64
	srv := newTLS(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body[:1000])
		w.(http.Flusher).Flush() // no Content-Length, so the answer is chunked
		_, _ = w.Write(body[1000:])
	})
	sink := sinkFunc(func(name string, size int64, r io.Reader) error {
		declared.Store(size)
		_, err := io.Copy(io.Discard, io.LimitReader(r, size))
		return err
	})
	stats, err := Fetch(context.Background(), clientFor(t, srv, ""), srv.URL, planOf(modName), sink, Limits{Bytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if declared.Load() != int64(len(body)) || stats.Bytes != int64(len(body)) {
		t.Errorf("the sink was told %d bytes and the stats say %d, want %d: the buffered length", declared.Load(), stats.Bytes, len(body))
	}
}

func TestFetchEnforcesTheByteBoundOnTheTotal(t *testing.T) {
	sixty := bytes.Repeat([]byte("a"), 60)
	files := map[string][]byte{"x.example/a/@v/v1.0.0.mod": sixty, "x.example/b/@v/v1.0.0.mod": sixty}
	plan := planOf("x.example/a/@v/v1.0.0.mod", "x.example/b/@v/v1.0.0.mod")
	proxy := newFakeProxy(t, files)
	cases := []struct {
		name      string
		bound     int64
		wantErr   bool
		wantFiles int
	}{
		{"both fit exactly", 120, false, 2},
		{"the second passes the total", 119, true, 1},
		{"the first alone passes it", 59, true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sink := &memSink{}
			stats, err := Fetch(context.Background(), clientFor(t, proxy.Server, ""), proxy.URL, plan, sink, Limits{Bytes: c.bound})
			if c.wantErr != passesTheBound(err) || (err != nil) != c.wantErr {
				t.Errorf("err = %v, want a passed bound = %v", err, c.wantErr)
			}
			if len(sink.entries) != c.wantFiles {
				t.Errorf("the sink took %d entries, want %d", len(sink.entries), c.wantFiles)
			}
			if err == nil && stats.Bytes != 120 {
				t.Errorf("Bytes = %d, want 120", stats.Bytes)
			}
		})
	}
}

func TestFetchEnforcesTheByteBoundWhileBufferingABodyWithNoLength(t *testing.T) {
	srv := newTLS(t, func(w http.ResponseWriter, r *http.Request) {
		chunk := bytes.Repeat([]byte("c"), 1000)
		for range 5 {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	})
	sink := &memSink{}
	_, err := Fetch(context.Background(), clientFor(t, srv, ""), srv.URL, planOf(modName), sink, Limits{Bytes: 4000})
	if !passesTheBound(err) {
		t.Errorf("err = %v, want a passed bound", err)
	}
	if len(sink.entries) != 0 {
		t.Errorf("the sink took %d entries of a body that passed the bound, want none", len(sink.entries))
	}
}

// A length that passes the bound is refused before a byte of the body is read.
func TestFetchRefusesADeclaredLengthPastTheBoundWithoutReadingTheBody(t *testing.T) {
	var bodyWritten atomic.Bool
	srv := newTLS(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100000")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done(): // the client hung up
		case <-time.After(5 * time.Second):
			bodyWritten.Store(true)
		}
	})
	sink := &memSink{}
	_, err := Fetch(context.Background(), clientFor(t, srv, ""), srv.URL, planOf(modName), sink, Limits{Bytes: 100})
	if !passesTheBound(err) {
		t.Errorf("err = %v, want a passed bound", err)
	}
	if len(sink.entries) != 0 || bodyWritten.Load() {
		t.Errorf("the sink took %d entries", len(sink.entries))
	}
}

func TestFetchReadsTheUnpackedSizeFromTheDirectoryAtTheEndOfAZipBiggerThanTheTail(t *testing.T) {
	zipBody := zipOf(t, 100000, 5, 7) // 100 KB of data, a directory of 3 entries
	proxy := newFakeProxy(t, map[string][]byte{zipName: zipBody})
	stats, err := fetch(context.Background(), clientFor(t, proxy.Server, ""), proxy.URL, planOf(zipName), &memSink{}, Limits{Bytes: 1 << 20}, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Unpacked != 100012 || stats.Bytes != int64(len(zipBody)) {
		t.Errorf("stats = %+v, want Unpacked 100012 and Bytes %d", stats, len(zipBody))
	}
}

func TestFetchRefusesAZipWhoseDirectoryDoesNotFitTheTail(t *testing.T) {
	zipBody := zipOf(t, make([]int, 80)...) // 80 entries: a directory of about 5 KB
	proxy := newFakeProxy(t, map[string][]byte{zipName: zipBody})
	_, err := fetch(context.Background(), clientFor(t, proxy.Server, ""), proxy.URL, planOf(zipName), &memSink{}, Limits{Bytes: 1 << 20}, 2048)
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v, want ErrTooLarge", err)
	}
	if errors.Is(err, ErrFetchFailed) {
		t.Error("a directory that does not fit is deterministic, not an infrastructure failure")
	}
}

// rawZip is a zip whose directory claims the given unpacked sizes; nothing is behind them.
func rawZip(t *testing.T, sizes ...uint64) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i, n := range sizes {
		if _, err := zw.CreateRaw(&zip.FileHeader{Name: fmt.Sprintf("example.com/m@v1.0.0/f%d", i), Method: zip.Store, UncompressedSize64: n}); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestFetchRefusesZipsWhoseUnpackedSizeOverflows(t *testing.T) {
	const half = 1 << 62 // a size that fits an int64, twice of it does not
	proxy := newFakeProxy(t, map[string][]byte{
		"x.example/one/@v/v1.0.0.zip":  rawZip(t, math.MaxInt64+1),
		"x.example/two/@v/v1.0.0.zip":  rawZip(t, half, half),
		"x.example/half/@v/v1.0.0.zip": rawZip(t, half),
		"x.example/more/@v/v1.0.0.zip": rawZip(t, half),
	})
	for name, files := range map[string][]string{
		"one entry past an int64":           {"x.example/one/@v/v1.0.0.zip"},
		"entries whose sum overflows":       {"x.example/two/@v/v1.0.0.zip"},
		"zips whose sum overflows":          {"x.example/half/@v/v1.0.0.zip", "x.example/more/@v/v1.0.0.zip"},
		"the second of two such zips again": {"x.example/half/@v/v1.0.0.zip", "x.example/more/@v/v1.0.0.zip", "x.example/half/@v/v1.0.0.zip"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Fetch(context.Background(), clientFor(t, proxy.Server, ""), proxy.URL, planOf(files...), &memSink{}, Limits{Bytes: 1 << 20})
			if !errors.Is(err, ErrTooLarge) {
				t.Errorf("err = %v, want ErrTooLarge", err)
			}
		})
	}
}

func TestFetchCountsAProxyOrNetworkFailureAsAFetchFailure(t *testing.T) {
	handlers := map[string]http.HandlerFunc{
		"not found":         func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
		"gone":              func(w http.ResponseWriter, r *http.Request) { http.Error(w, "gone", http.StatusGone) },
		"server error":      func(w http.ResponseWriter, r *http.Request) { http.Error(w, "oops", http.StatusInternalServerError) },
		"too many requests": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "slow", http.StatusTooManyRequests) },
		"not a zip": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>this is a redirect page, not a zip</html>"))
		},
		"a body cut short": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write(bytes.Repeat([]byte("s"), 10))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		},
		"a chunked body cut short": func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write(bytes.Repeat([]byte("s"), 10))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		},
	}
	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			srv := newTLS(t, h)
			_, err := Fetch(context.Background(), clientFor(t, srv, ""), srv.URL, planOf(zipName), &memSink{}, Limits{Bytes: 1 << 20})
			if !errors.Is(err, ErrFetchFailed) {
				t.Errorf("err = %v, want ErrFetchFailed", err)
			}
			if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrInstallFailed) {
				t.Errorf("err = %v is also a deterministic failure", err)
			}
		})
	}
	t.Run("a timeout", func(t *testing.T) {
		srv := newTLS(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		c := clientFor(t, srv, "")
		c.Timeout = 100 * time.Millisecond
		_, err := Fetch(context.Background(), c, srv.URL, planOf(modName), &memSink{}, Limits{Bytes: 1 << 20})
		if !errors.Is(err, ErrFetchFailed) {
			t.Errorf("err = %v, want ErrFetchFailed", err)
		}
	})
	t.Run("a refused connection", func(t *testing.T) {
		srv := newTLS(t, func(w http.ResponseWriter, r *http.Request) {})
		c := clientFor(t, srv, "")
		base := srv.URL
		srv.Close()
		_, err := Fetch(context.Background(), c, base, planOf(modName), &memSink{}, Limits{Bytes: 1 << 20})
		if !errors.Is(err, ErrFetchFailed) {
			t.Errorf("err = %v, want ErrFetchFailed", err)
		}
	})
}

func TestFetchDoesNotCallASinkFailureAFetchFailure(t *testing.T) {
	proxy := newFakeProxy(t, map[string][]byte{modName: modBody})
	boom := errors.New("the tar is closed")
	_, err := Fetch(context.Background(), clientFor(t, proxy.Server, ""), proxy.URL, planOf(modName), &memSink{fail: boom}, Limits{Bytes: 1 << 20})
	if !errors.Is(err, boom) || errors.Is(err, ErrFetchFailed) {
		t.Errorf("err = %v, want the sink's error and not ErrFetchFailed", err)
	}
	// The same failure after the body has begun to arrive: the sink's error, not the read error that followed it.
	srv := newTLS(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1000")
		_, _ = w.Write(bytes.Repeat([]byte("s"), 10))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	failAfterARead := sinkFunc(func(name string, size int64, r io.Reader) error {
		if _, err := io.ReadFull(r, make([]byte, 10)); err != nil {
			return err
		}
		return boom
	})
	_, err = Fetch(context.Background(), clientFor(t, srv, ""), srv.URL, planOf(modName), failAfterARead, Limits{Bytes: 1 << 20})
	if !errors.Is(err, boom) || errors.Is(err, ErrFetchFailed) {
		t.Errorf("err = %v, want the sink's error and not ErrFetchFailed", err)
	}
}

// A shutdown or the end of the job is not an infrastructure failure to count.
func TestFetchReturnsTheContextErrorWhenTheContextEnds(t *testing.T) {
	proxy := newFakeProxy(t, map[string][]byte{modName: modBody})
	t.Run("cancelled before", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Fetch(ctx, clientFor(t, proxy.Server, ""), proxy.URL, planOf(modName), &memSink{}, Limits{Bytes: 1 << 20})
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrFetchFailed) {
			t.Errorf("err = %v, want context.Canceled and not ErrFetchFailed", err)
		}
	})
	t.Run("cancelled while a body arrives", func(t *testing.T) {
		srv := newTLS(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = w.Write(bytes.Repeat([]byte("s"), 10))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		sink := sinkFunc(func(name string, size int64, r io.Reader) error {
			if _, err := io.ReadFull(r, make([]byte, 10)); err != nil {
				return err
			}
			cancel()
			_, err := io.Copy(io.Discard, r)
			return err
		})
		_, err := Fetch(ctx, clientFor(t, srv, ""), srv.URL, planOf(modName), sink, Limits{Bytes: 1 << 20})
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrFetchFailed) {
			t.Errorf("err = %v, want context.Canceled and not ErrFetchFailed", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		srv := newTLS(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := Fetch(ctx, clientFor(t, srv, ""), srv.URL, planOf(modName), &memSink{}, Limits{Bytes: 1 << 20})
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrFetchFailed) {
			t.Errorf("err = %v, want context.DeadlineExceeded and not ErrFetchFailed", err)
		}
	})
}

func TestFetchRequestsOnlyOverHTTPS(t *testing.T) {
	var hits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer plain.Close()
	tlsSrv := newTLS(t, func(w http.ResponseWriter, r *http.Request) {})
	for name, base := range map[string]string{
		"http":       plain.URL,
		"ftp":        "ftp://proxy.example",
		"no scheme":  "proxy.golang.org",
		"empty":      "",
		"no host":    "https://",
		"a query":    tlsSrv.URL + "?x=1",
		"a fragment": tlsSrv.URL + "#x",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Fetch(context.Background(), clientFor(t, tlsSrv, ""), base, planOf(modName), &memSink{}, Limits{Bytes: 1 << 20})
			if err == nil || errors.Is(err, ErrFetchFailed) {
				t.Errorf("err = %v, want a refusal that is not a counted fetch failure", err)
			}
		})
	}
	if hits.Load() != 0 {
		t.Errorf("the plain http server got %d requests", hits.Load())
	}
}

func TestFetchOfAVendoredOrEmptyPlanAsksNothing(t *testing.T) {
	proxy := newFakeProxy(t, nil)
	for name, plan := range map[string]Plan{"vendor": {Vendor: true}, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			stats, err := Fetch(context.Background(), clientFor(t, proxy.Server, ""), proxy.URL, plan, &memSink{}, Limits{Bytes: 1})
			if err != nil || stats != (Stats{}) {
				t.Errorf("stats = %+v, err = %v, want zero and nil", stats, err)
			}
		})
	}
	if got := proxy.asked(); len(got) != 0 {
		t.Errorf("requests = %q, want none", got)
	}
}

func TestFetchWritesNoFileToDisk(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{"TMPDIR", "TEMP", "TMP"} {
		t.Setenv(k, dir)
	}
	zipBody := zipOf(t, 100, 250)
	proxy := newFakeProxy(t, map[string][]byte{modName: modBody, zipName: zipBody})
	if _, err := Fetch(context.Background(), clientFor(t, proxy.Server, ""), proxy.URL, planOf(modName, zipName), &memSink{}, Limits{Bytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Errorf("the temporary directory holds %d entries after a fetch (%v), want none", len(entries), err)
	}
}

// redirecting returns a server that sends every request to target.
func redirecting(t *testing.T, target string) *httptest.Server {
	t.Helper()
	return newTLS(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target, http.StatusFound) })
}

func TestFetchFollowsARedirectOnlyToTheAllowedHost(t *testing.T) {
	zipBody := zipOf(t, 10)
	storage := newFakeProxy(t, map[string][]byte{"blob": zipBody})
	storageHost := hostOf(t, storage.URL)
	plainStorage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(zipBody) }))
	defer plainStorage.Close()

	t.Run("the allowed host is followed", func(t *testing.T) {
		front := redirecting(t, storage.URL+"/blob")
		sink := &memSink{}
		stats, err := Fetch(context.Background(), clientFor(t, front, storageHost), front.URL, planOf(zipName), sink, Limits{Bytes: 1 << 20})
		if err != nil || stats.Files != 1 || len(sink.entries) != 1 || !bytes.Equal(sink.entries[0].data, zipBody) {
			t.Fatalf("stats = %+v, err = %v, entries = %d: the redirect to the allowed host was not followed", stats, err, len(sink.entries))
		}
		if got := storage.asked(); !slices.Equal(got, []string{"GET /blob"}) {
			t.Errorf("the storage host was asked %q", got)
		}
	})
	t.Run("another host is refused and never asked", func(t *testing.T) {
		before := len(storage.asked())
		// Same server, another name for it: the policy compares names, so this one is another host.
		otherName := "https://localhost:" + strings.TrimPrefix(storageHost, "127.0.0.1:") + "/blob"
		front := redirecting(t, otherName)
		_, err := Fetch(context.Background(), clientFor(t, front, storageHost), front.URL, planOf(zipName), &memSink{}, Limits{Bytes: 1 << 20})
		if !errors.Is(err, ErrFetchFailed) {
			t.Errorf("err = %v, want ErrFetchFailed", err)
		}
		if got := len(storage.asked()); got != before {
			t.Errorf("the refused host was asked %d times", got-before)
		}
	})
	t.Run("a downgrade to http is refused even for the allowed host", func(t *testing.T) {
		front := redirecting(t, plainStorage.URL+"/blob")
		_, err := Fetch(context.Background(), clientFor(t, front, hostOf(t, plainStorage.URL)), front.URL, planOf(zipName), &memSink{}, Limits{Bytes: 1 << 20})
		if !errors.Is(err, ErrFetchFailed) {
			t.Errorf("err = %v, want ErrFetchFailed", err)
		}
	})
	t.Run("a loop is refused", func(t *testing.T) {
		var self string
		loop := newTLS(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, self, http.StatusFound) })
		self = loop.URL + "/again"
		_, err := Fetch(context.Background(), clientFor(t, loop, hostOf(t, loop.URL)), loop.URL, planOf(zipName), &memSink{}, Limits{Bytes: 1 << 20})
		if !errors.Is(err, ErrFetchFailed) {
			t.Errorf("err = %v, want ErrFetchFailed", err)
		}
	})
}

func TestClientAllowsARedirectOnlyToStorageGoogleapisOverHTTPS(t *testing.T) {
	c := Client(7 * time.Second)
	if c.Timeout != 7*time.Second {
		t.Errorf("Timeout = %v, want 7s", c.Timeout)
	}
	redirect := func(target string, hops int) error {
		u, err := url.Parse(target)
		if err != nil {
			t.Fatal(err)
		}
		return c.CheckRedirect(&http.Request{URL: u}, make([]*http.Request, hops))
	}
	for _, ok := range []string{"https://storage.googleapis.com/go-proxy/blob", "https://Storage.GoogleAPIs.com/x"} {
		if err := redirect(ok, 1); err != nil {
			t.Errorf("a redirect to %s is refused: %v", ok, err)
		}
	}
	for _, refused := range []string{
		"http://storage.googleapis.com/x",
		"https://storage.googleapis.com.evil.example/x",
		"https://evil.example/storage.googleapis.com",
		"https://proxy.golang.org/x",
		"https://storage.googleapis.com:8443/x",
		"https://storage.googleapis.com@evil.example/x",
		"https://127.0.0.1/x",
	} {
		if err := redirect(refused, 1); err == nil {
			t.Errorf("a redirect to %s is allowed", refused)
		}
	}
	if err := redirect("https://storage.googleapis.com/x", 4); err == nil {
		t.Error("the fifth request of a chain is allowed")
	}
	if tr, ok := c.Transport.(*http.Transport); !ok || !tr.DisableCompression {
		t.Error("the client must not ask for a compressed answer: Content-Length is how a file is streamed")
	}
}

// A body of unknown length that is exactly as large as what is left of the bound fits; one byte more does not.
func TestFetchBuffersABodyWithNoLengthThatIsExactlyTheBound(t *testing.T) {
	body := bytes.Repeat([]byte("c"), 3000)
	srv := newTLS(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body[:1000])
		w.(http.Flusher).Flush()
		_, _ = w.Write(body[1000:])
	})
	for name, tc := range map[string]struct {
		bound   int64
		wantErr bool
	}{"exactly the bound": {3000, false}, "one byte more than the bound": {2999, true}} {
		t.Run(name, func(t *testing.T) {
			sink := &memSink{}
			_, err := Fetch(context.Background(), clientFor(t, srv, ""), srv.URL, planOf(modName), sink, Limits{Bytes: tc.bound})
			if tc.wantErr != passesTheBound(err) || (err != nil) != tc.wantErr {
				t.Errorf("err = %v, want a passed bound = %v", err, tc.wantErr)
			}
			if !tc.wantErr && (len(sink.entries) != 1 || !bytes.Equal(sink.entries[0].data, body)) {
				t.Errorf("the sink took %d entries, want the whole body in one", len(sink.entries))
			}
		})
	}
	// The body is cut short at exactly the bound: that is a failure of the proxy, not a body that fits.
	cut := newTLS(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler)
	})
	_, err := Fetch(context.Background(), clientFor(t, cut, ""), cut.URL, planOf(modName), &memSink{}, Limits{Bytes: 3000})
	if !errors.Is(err, ErrFetchFailed) {
		t.Errorf("a body cut after the bound: err = %v, want ErrFetchFailed", err)
	}
}

func TestFetchRefusesASinkThatDoesNotTakeTheWholeFile(t *testing.T) {
	proxy := newFakeProxy(t, map[string][]byte{modName: modBody})
	half := sinkFunc(func(name string, size int64, r io.Reader) error {
		_, err := io.CopyN(io.Discard, r, size/2)
		return err
	})
	_, err := Fetch(context.Background(), clientFor(t, proxy.Server, ""), proxy.URL, planOf(modName), half, Limits{Bytes: 1 << 20})
	if err == nil || errors.Is(err, ErrFetchFailed) {
		t.Errorf("err = %v, want a refusal of the sink that is not a fetch failure", err)
	}
}

func TestFetchRefusesAFileNameThatIsNotAURL(t *testing.T) {
	proxy := newFakeProxy(t, nil)
	_, err := Fetch(context.Background(), clientFor(t, proxy.Server, ""), proxy.URL, planOf("a.example/m/@v/\x7f.mod"), &memSink{}, Limits{Bytes: 1 << 20})
	if err == nil || errors.Is(err, ErrFetchFailed) {
		t.Errorf("err = %v, want a refusal that is not a counted fetch failure", err)
	}
	if got := proxy.asked(); len(got) != 0 {
		t.Errorf("requests = %q, want none", got)
	}
}

func TestTailBufferKeepsTheLastBytesWhateverTheChunks(t *testing.T) {
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i)
	}
	for _, chunk := range []int{1, 7, 100, 250, 999, 1000} {
		for _, keep := range []int{1, 10, 100, 499, 1000, 1500} {
			tail := tailBuffer{max: keep}
			for rest := data; len(rest) > 0; {
				n := min(chunk, len(rest))
				tail.write(rest[:n])
				rest = rest[n:]
			}
			if want := data[max(0, len(data)-keep):]; !bytes.Equal(tail.bytes(), want) {
				t.Errorf("chunks of %d, tail of %d: kept %d bytes that are not the last %d", chunk, keep, len(tail.bytes()), len(want))
			}
		}
	}
}

func TestTailReaderAtReadsOnlyWhatWasKept(t *testing.T) {
	r := tailReaderAt{tail: []byte("0123456789"), off: 100}
	buf := make([]byte, 4)
	if n, err := r.ReadAt(buf, 102); n != 4 || err != nil || string(buf) != "2345" {
		t.Errorf("a read inside the tail got %d %q %v", n, buf, err)
	}
	if n, err := r.ReadAt(buf, 108); n != 2 || !errors.Is(err, io.EOF) || string(buf[:2]) != "89" {
		t.Errorf("a read across the end got %d %q %v, want 2 bytes and io.EOF", n, buf[:n], err)
	}
	if n, err := r.ReadAt(buf, 110); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("a read at the end got %d %v, want io.EOF", n, err)
	}
	if _, err := r.ReadAt(buf, 99); !errors.Is(err, errNotInTail) {
		t.Errorf("a read before the tail: err = %v, want errNotInTail", err)
	}
}
