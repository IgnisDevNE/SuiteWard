package prefetch

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// redirectHost is where proxy.golang.org sends the zips it does not serve itself.
	redirectHost = "storage.googleapis.com"
	// maxRedirects is how many redirects one request may follow; the proxy needs one.
	maxRedirects = 3
	// maxTail bounds what is kept of a module zip's stream to read its directory, which is at the end of the file. The largest directory among
	// the module zips that this repository's dependencies ship is 0.4 MB, so 4 MiB leaves ten times that.
	maxTail = 4 << 20
)

// Client is the HTTP client of Fetch: a request is only made to the https base that Fetch is given, and a redirect is only followed to
// https://storage.googleapis.com. It asks for no compression, because the Content-Length of an answer that go would decompress is lost, and the
// length is how a file streams into the tar. It does not read HTTP_PROXY, HTTPS_PROXY or NO_PROXY: the egress of the executor goes direct, so
// that an allow-list for proxy.golang.org holds. timeout bounds one request, its body included.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableCompression:  true,
			TLSHandshakeTimeout: 10 * time.Second,
			IdleConnTimeout:     30 * time.Second,
		},
		CheckRedirect: allowRedirectTo(redirectHost),
	}
}

// allowRedirectTo is a redirect policy that follows at most maxRedirects redirects, each to https and the given host (with its port, if any).
func allowRedirectTo(host string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Host, host) {
			return fmt.Errorf("a redirect to %s://%s is refused", req.URL.Scheme, req.URL.Host)
		}
		return nil
	}
}

// Fetch streams the files of p from the proxy at base (https://proxy.golang.org), one after the other, into sink. A file whose length the
// proxy states is streamed with that length, and one with no length is buffered, within the bound: at most one file is in memory. l.Bytes
// bounds the files together. Nothing is written to disk.
//
// A failure of the proxy, of the network, a status other than 200, a redirect that is refused and an answer that is not what the proxy
// should send are ErrFetchFailed. A file past l.Bytes is ErrInstallFailed, as is a plan that passes its bound. A zip whose directory does not
// fit the bounded tail that is kept to read it, or whose unpacked size is larger than any size, is ErrTooLarge: its unpacked size, which the
// caller sums into its size check, cannot be had. When ctx ends, its error comes back unwrapped from any fetch failure, and a failure of the sink is the sink's own.
func Fetch(ctx context.Context, c *http.Client, base string, p Plan, sink Sink, l Limits) (Stats, error) {
	return fetch(ctx, c, base, p, sink, l, maxTail)
}

// fetch is Fetch with the size of the tail as a parameter, for the tests.
func fetch(ctx context.Context, c *http.Client, base string, p Plan, sink Sink, l Limits, tail int) (Stats, error) {
	if u, err := url.Parse(base); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Stats{}, errors.New("prefetch: the proxy base is not an https URL without user info, a query or a fragment")
	}
	f := &fetcher{client: c, root: strings.TrimRight(base, "/"), sink: sink, tail: tail}
	var st Stats
	for _, file := range p.Files {
		got, err := f.file(ctx, file.Name, l.Bytes-st.Bytes)
		if err != nil {
			return st, err
		}
		if got.unpacked > math.MaxInt64-st.Unpacked {
			return st, fmt.Errorf("%w: the unpacked size of the modules is larger than any size", ErrTooLarge)
		}
		st.Files++
		st.Bytes += got.size
		st.Unpacked += got.unpacked
	}
	return st, nil
}

type fetcher struct {
	client *http.Client
	root   string
	sink   Sink
	tail   int
}

type fetched struct{ size, unpacked int64 }

// file fetches one file into the sink. budget is what is left of the byte bound.
func (f *fetcher) file(ctx context.Context, name string, budget int64) (fetched, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.root+"/"+name, nil)
	if err != nil {
		return fetched{}, fmt.Errorf("prefetch: request for %s: %w", name, err)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return fetched{}, fetchFailed(ctx, name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fetched{}, fmt.Errorf("%w: %s answered %d", ErrFetchFailed, name, resp.StatusCode)
	}
	// The transport gives a body no more bytes than its Content-Length, so a known length is checked once, before the first byte.
	body, size := io.Reader(resp.Body), resp.ContentLength
	if size < 0 {
		data, fits, err := readWithin(resp.Body, budget)
		if err != nil {
			return fetched{}, fetchFailed(ctx, name, err)
		}
		if !fits {
			return fetched{}, boundPassed(name, budget)
		}
		body, size = bytes.NewReader(data), int64(len(data))
	} else if size > budget {
		return fetched{}, boundPassed(name, budget)
	}
	return f.add(ctx, name, size, body)
}

// boundPassed is the refusal of a file that passes the byte bound: install-failed, as the runtime contract says of a bound on the bytes of modules.
func boundPassed(name string, budget int64) error {
	return fmt.Errorf("%w: %s passes the %d bytes that are left of the bound", ErrInstallFailed, name, budget)
}

// readWithin reads a body whole when it has at most budget bytes, and reports fits false when it has more.
func readWithin(r io.Reader, budget int64) (data []byte, fits bool, err error) {
	lr := &io.LimitedReader{R: r, N: budget}
	data, err = io.ReadAll(lr)
	if err != nil {
		return nil, false, err
	}
	if lr.N > 0 {
		return data, true, nil
	}
	// The bound is reached: the body fits only if nothing follows. A byte that follows reads with no error, and then the body does not fit.
	var next [1]byte
	if _, err = io.ReadFull(r, next[:]); errors.Is(err, io.EOF) {
		return data, true, nil
	}
	return nil, false, err
}

// add streams a body of a known size into the sink, and for a zip reads its unpacked size from the directory in the tail of the stream.
func (f *fetcher) add(ctx context.Context, name string, size int64, body io.Reader) (fetched, error) {
	br := &bodyReader{r: body, tail: tailBuffer{max: f.tail}}
	if err := f.sink.Add(name, size, br); err != nil || br.err != nil {
		if br.err != nil || ctx.Err() != nil {
			return fetched{}, fetchFailed(ctx, name, br.err)
		}
		return fetched{}, fmt.Errorf("prefetch: adding %s to the sink: %w", name, err)
	}
	if br.n != size {
		return fetched{}, fmt.Errorf("prefetch: the sink took %d of the %d bytes of %s", br.n, size, name)
	}
	if !strings.HasSuffix(name, ".zip") {
		return fetched{size: size}, nil
	}
	unpacked, err := unpackedSize(name, br.tail.bytes(), size)
	return fetched{size: size, unpacked: unpacked}, err
}

// fetchFailed is the error of a body or a request that failed. When ctx has ended, that is the cause and it is not counted.
func fetchFailed(ctx context.Context, name string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("prefetch: %s: %w", name, ctxErr)
	}
	return fmt.Errorf("%w: %s: %w", ErrFetchFailed, name, err)
}

// bodyReader counts what the sink reads, keeps the failure of the body apart from the failure of the sink (which returns the first as its own),
// and keeps the tail of the stream.
type bodyReader struct {
	r    io.Reader
	n    int64
	err  error
	tail tailBuffer
}

func (b *bodyReader) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	b.n += int64(n)
	b.tail.write(p[:n])
	if err != nil && !errors.Is(err, io.EOF) {
		b.err = err
	}
	return n, err
}

// tailBuffer keeps the last max bytes written to it, in at most twice that memory.
type tailBuffer struct {
	b   []byte
	max int
}

func (t *tailBuffer) write(p []byte) {
	t.b = append(t.b, p...)
	if len(t.b) > 2*t.max {
		t.b = append(t.b[:0], t.b[len(t.b)-t.max:]...)
	}
}

func (t *tailBuffer) bytes() []byte {
	if len(t.b) > t.max {
		return t.b[len(t.b)-t.max:]
	}
	return t.b
}

var errNotInTail = errors.New("the zip reads before the tail that was kept")

// tailReaderAt serves the last bytes of a stream as if they were the whole of it: tail starts at offset off.
type tailReaderAt struct {
	tail []byte
	off  int64
}

func (t tailReaderAt) ReadAt(p []byte, at int64) (int, error) {
	if at < t.off {
		return 0, errNotInTail
	}
	i := at - t.off
	if i >= int64(len(t.tail)) {
		return 0, io.EOF
	}
	n := copy(p, t.tail[i:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// unpackedSize sums the sizes of the files of a zip from its directory, which archive/zip reads from the last bytes of the zip. A directory
// that does not fit the tail is ErrTooLarge, and a body that is not a zip is ErrFetchFailed: the proxy sent what it should not.
func unpackedSize(name string, tail []byte, size int64) (int64, error) {
	zr, err := zip.NewReader(tailReaderAt{tail: tail, off: size - int64(len(tail))}, size)
	switch {
	case errors.Is(err, errNotInTail):
		return 0, fmt.Errorf("%w: the directory of %s does not fit the last %d bytes that are kept to read it", ErrTooLarge, name, len(tail))
	case err != nil && !errors.Is(err, zip.ErrInsecurePath): // a reader comes with that one: only the sizes are read
		return 0, fmt.Errorf("%w: %s is not a zip: %w", ErrFetchFailed, name, err)
	}
	var total int64
	for _, entry := range zr.File {
		n, err := int64Of(entry.UncompressedSize64)
		if err != nil || n > math.MaxInt64-total {
			return 0, fmt.Errorf("%w: the files of %s are larger than any size", ErrTooLarge, name)
		}
		total += n
	}
	return total, nil
}

func int64Of(n uint64) (int64, error) {
	if n > math.MaxInt64 {
		return 0, fmt.Errorf("%d exceeds the range of a size", n)
	}
	return int64(n), nil
}
