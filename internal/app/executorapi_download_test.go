package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

func archivePath(job execution.Job, side string) string {
	return fmt.Sprintf("/jobs/%d/archive/%s", job.ID, side)
}

func TestArchiveStreamsTheCommitFromTheReaderWithTheArchiveLimit(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()

	got := e.send(http.MethodGet, archivePath(leased, "head"), nil, withLease(leased.LeaseID))

	if got.status != http.StatusOK || got.text() != "archive of "+execSHA(1) {
		t.Fatalf("archive = %d %q; want 200 with the reader's bytes", got.status, got.body)
	}
	if got.header.Get("Content-Type") != "application/gzip" || got.header.Get("Content-Length") != "" {
		t.Errorf("headers = %v; want application/gzip and no Content-Length: the 200 goes out before the length is known", got.header)
	}
	calls := e.archives.asked()
	if len(calls) != 1 || calls[0].repo != execRepoName || calls[0].sha != execSHA(1) || calls[0].max != 268435456 {
		t.Errorf("reader calls = %+v; want one, for the head of %s with archiveBytes as the limit", calls, execRepoName)
	}
}

func TestArchiveStreamsTheMergeBaseOfAReceipt(t *testing.T) {
	e := newExecEnv(t)
	e.receiptJob(1, defaultFiles)
	leased := e.grant()

	got := e.send(http.MethodGet, archivePath(leased, "base"), nil, withLease(leased.LeaseID))

	if got.status != http.StatusOK || got.text() != "archive of "+execSHA(1001) {
		t.Fatalf("archive = %d %q; want 200 with the base's bytes", got.status, got.body)
	}
}

// The body is bounded by the job's remaining time, and by ten minutes at most.
func TestArchiveGivesTheReaderADeadline(t *testing.T) {
	for _, test := range []struct {
		name       string
		jobTimeout time.Duration
		want       time.Duration // from now
	}{{"ten minutes at most", 30 * time.Minute, 10 * time.Minute}, {"the job's remaining time", 90 * time.Second, 90 * time.Second}} {
		t.Run(test.name, func(t *testing.T) {
			e := newExecEnv(t)
			e.times.JobTimeout = test.jobTimeout
			e.canonicalJob(1, defaultFiles)
			leased := e.grant()

			e.send(http.MethodGet, archivePath(leased, "head"), nil, withLease(leased.LeaseID))

			calls := e.archives.asked()
			if len(calls) != 1 || !calls[0].deadline.Equal(e.clock.Now().Add(test.want)) {
				t.Errorf("reader calls = %+v; want a deadline %v from now", calls, test.want)
			}
		})
	}
}

// The listener bounds a response to 30 s; a download extends its own bound, so a stream that outlasts the listener's goes on.
func TestArchiveExtendsTheWriteDeadlineOfItsRequest(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	e.archives.set(func(_ context.Context, dst io.Writer) (int64, error) {
		if _, err := io.WriteString(dst, "first"); err != nil {
			return 0, err
		}
		time.Sleep(400 * time.Millisecond) // past the 150 ms that this listener allows a response
		n, err := io.WriteString(dst, "second")
		return int64(5 + n), err
	})
	server := httptest.NewUnstartedServer(e.server.Config.Handler)
	server.Config.WriteTimeout = 150 * time.Millisecond
	server.Start()
	t.Cleanup(server.Close)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/executor/v1"+archivePath(leased, "head"), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+execTestToken)
	req.Header.Set(leaseHeader, leased.LeaseID)

	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }() // the body is read to its end
	body, err := io.ReadAll(resp.Body)

	if err != nil || string(body) != "firstsecond" {
		t.Errorf("stream = %q, %v; want the whole of it past the listener's write bound", body, err)
	}
}

func TestArchiveAnswers404ForWhatIsNotThere(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	for name, path := range map[string]string{
		"the base of a canonical job": archivePath(leased, "base"), "another side": archivePath(leased, "tree"), "an upper-case side": archivePath(leased, "HEAD"),
		"an unknown job": "/jobs/99/archive/head", "a job that is not a number": "/jobs/x/archive/head",
	} {
		e.wantError(e.send(http.MethodGet, path, nil, withLease(leased.LeaseID)), http.StatusNotFound, "not-found")
		if len(e.archives.asked()) != 0 {
			t.Errorf("%s: the reader was asked for an archive", name)
		}
	}
}

func TestArchiveAnswers409ForALostLease(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	path := archivePath(leased, "head")
	e.wantError(e.send(http.MethodGet, path, nil, withLease("ffffffffffffffffffffffffffffffff")), http.StatusConflict, "lease-lost")
	e.wantError(e.send(http.MethodGet, path, nil), http.StatusConflict, "lease-lost")
	e.clock.Advance(execution.LeaseTTL)
	e.wantError(e.send(http.MethodGet, path, nil, withLease(leased.LeaseID)), http.StatusConflict, "lease-lost")
	if len(e.archives.asked()) != 0 {
		t.Error("the reader was asked for an archive without a lease")
	}
}

func TestArchiveAnswers409ForAJobThatEnded(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	if _, err := e.store.Cancel(t.Context(), leased.ID, execution.ReasonHeadMoved, e.clock.Now()); err != nil {
		t.Fatal(err)
	}

	e.wantError(e.send(http.MethodGet, archivePath(leased, "head"), nil, withLease(leased.LeaseID)), http.StatusConflict, "lease-lost")
}

// A declared length over the limit is refused before a byte is sent.
func TestArchiveAnswers413WhenTheArchiveDeclaresTooMuch(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	e.archives.set(func(context.Context, io.Writer) (int64, error) {
		return 0, fmt.Errorf("download: %w", rest.ErrTooLarge)
	})

	e.wantError(e.send(http.MethodGet, archivePath(leased, "head"), nil, withLease(leased.LeaseID)), http.StatusRequestEntityTooLarge, "too-large")
}

// A GitHub failure or a refused budget before a byte is a 502 with a fixed code and none of GitHub's words.
func TestArchiveAnswers502WhenGitHubCannotBeRead(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	for name, err := range map[string]error{
		"a failure":     errors.New("GitHub says: secret detail"),
		"a budget":      &rest.BudgetExhaustedError{Reset: execOrigin},
		"a status":      &rest.StatusError{Status: http.StatusBadGateway},
		"the redirect":  rest.ErrInsecureRedirect,
		"no deadline":   rest.ErrNoDeadline, // the deadline is the service's own: a bug of it, which the executor reads as a GitHub failure it can retry
		"a wrapped one": fmt.Errorf("archive: %w", io.ErrUnexpectedEOF),
	} {
		e.archives.set(func(context.Context, io.Writer) (int64, error) { return 0, err })
		got := e.send(http.MethodGet, archivePath(leased, "head"), nil, withLease(leased.LeaseID))
		e.wantError(got, http.StatusBadGateway, "github")
		if strings.Contains(e.logs.String(), "secret detail") {
			t.Errorf("%s: GitHub's text reached the log: %s", name, e.logs.String())
		}
	}
}

// Once the 200 is out the status cannot change: an error aborts the response, and the executor sees a stream that breaks.
func TestArchiveAbortsTheResponseWhenTheStreamFailsAfterTheStatus(t *testing.T) {
	for name, cause := range map[string]error{"GitHub cut off": io.ErrUnexpectedEOF, "the limit passed": rest.ErrTooLarge} {
		t.Run(name, func(t *testing.T) {
			e := newExecEnv(t)
			e.canonicalJob(1, defaultFiles)
			leased := e.grant()
			e.archives.set(func(_ context.Context, dst io.Writer) (int64, error) {
				n, err := io.WriteString(dst, "partial archive")
				if err != nil {
					return int64(n), err
				}
				return int64(n), fmt.Errorf("download: %w", cause)
			})
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, e.server.URL+"/executor/v1"+archivePath(leased, "head"), nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+execTestToken)
			req.Header.Set(leaseHeader, leased.LeaseID)

			resp, err := e.server.Client().Do(req)
			if err != nil {
				t.Fatalf("the request failed before the status: %v", err)
			}
			defer func() { _ = resp.Body.Close() }() // the read below ends in an error
			body, err := io.ReadAll(resp.Body)

			if resp.StatusCode != http.StatusOK {
				t.Errorf("status = %d; want the 200 that was already sent", resp.StatusCode)
			}
			if err == nil {
				t.Errorf("stream = %q read to a clean end; want a broken stream, which the executor reads as a failure", body)
			}
		})
	}
}

func TestArchiveMayBeRepeatedAndIsNotTheExecutorBeingSeen(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	for range 2 {
		if got := e.send(http.MethodGet, archivePath(leased, "head"), nil, withLease(leased.LeaseID)); got.status != http.StatusOK {
			t.Fatalf("archive = %d %s; want 200 each time", got.status, got.text())
		}
	}
	if _, requested := e.liveness.LastRequest(); requested {
		t.Error("an archive request counts as an executor seen; only a lease request and a heartbeat do")
	}
}

// --- files ---

func filePath(job execution.Job, digest artifact.Digest) string {
	return fmt.Sprintf("/jobs/%d/files/%s", job.ID, digest)
}

func TestFileServesEachFileOfTheManifestFromTheStore(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	for path, content := range defaultFiles {
		got := e.send(http.MethodGet, filePath(leased, execDigest(content)), nil, withLease(leased.LeaseID))

		if got.status != http.StatusOK || got.text() != content {
			t.Errorf("%s = %d %q; want 200 with its bytes", path, got.status, got.body)
		}
		if got.header.Get("Content-Type") != "application/octet-stream" || got.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s headers = %v; want application/octet-stream and nosniff", path, got.header)
		}
	}
}

// Only the files this job's manifest names: another object of the store is not served, whatever its digest.
func TestFileServesNothingThatTheManifestDoesNotName(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	outside := e.put("a file of another job, which this one must never read")
	manifestDigest := e.manifest(defaultFiles).Digest() // the manifest itself is in the store, and is not one of its files

	for name, path := range map[string]string{
		"another job's file": filePath(leased, outside), "the manifest": filePath(leased, manifestDigest), "a missing digest": filePath(leased, execDigest("never stored")),
		"a malformed digest": "/jobs/1/files/sha256:xyz", "no prefix": "/jobs/1/files/" + strings.TrimPrefix(execDigest("x").String(), "sha256:"), "upper case": "/jobs/1/files/" + strings.ToUpper(execDigest("x").String()),
		"a traversal": "/jobs/1/files/..%2F..%2Fetc%2Fpasswd",
	} {
		got := e.send(http.MethodGet, path, nil, withLease(leased.LeaseID))
		if got.status != http.StatusNotFound || strings.Contains(got.text(), "never read") {
			t.Errorf("%s = %d %q; want 404 and nothing of the store", name, got.status, got.body)
		}
	}
}

func TestFileAnswers409ForALostLeaseAnd404ForAnUnknownJob(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()
	digest := execDigest(defaultFiles["contract.go"])
	e.wantError(e.send(http.MethodGet, filePath(leased, digest), nil, withLease("ffffffffffffffffffffffffffffffff")), http.StatusConflict, "lease-lost")
	e.wantError(e.send(http.MethodGet, filePath(leased, digest), nil), http.StatusConflict, "lease-lost")
	e.wantError(e.send(http.MethodGet, "/jobs/99/files/"+digest.String(), nil, withLease(leased.LeaseID)), http.StatusNotFound, "not-found")
}

func TestFileHonorsTheFileLimit(t *testing.T) {
	for _, test := range []struct {
		name   string
		size   int
		status int
	}{{"the limit", 8 << 20, http.StatusOK}, {"one byte over", 8<<20 + 1, http.StatusRequestEntityTooLarge}} {
		t.Run(test.name, func(t *testing.T) {
			e := newExecEnv(t)
			content := strings.Repeat("x", test.size)
			e.canonicalJob(1, map[string]string{"big.go": content, ".suiteward.yml": "execution: {}\n"})
			leased := e.grant()

			got := e.send(http.MethodGet, filePath(leased, execDigest(content)), nil, withLease(leased.LeaseID))

			if got.status != test.status {
				t.Fatalf("file of %d bytes = %d; want %d", test.size, got.status, test.status)
			}
			if test.status == http.StatusOK && !bytes.Equal(got.body, []byte(content)) {
				t.Error("the bytes differ")
			}
			if test.status == http.StatusRequestEntityTooLarge {
				e.wantError(got, http.StatusRequestEntityTooLarge, "too-large")
			}
		})
	}
}

// A digest of the manifest that the store cannot give is the service's fault: 500, logged at Error.
func TestFileAnswers500WhenTheStoreLacksAFileOfTheManifest(t *testing.T) {
	e := newExecEnv(t)
	missing := execDigest("a file that was never stored")
	m, err := artifact.NewManifest([]artifact.Entry{{Path: "ghost.go", Content: missing}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.content.Put(t.Context(), m.Digest(), bytes.NewReader(m.Encoding())); err != nil {
		t.Fatal(err)
	}
	e.enqueue(execution.Spec{RepositoryID: execRepoID, PullNumber: 3, Plan: execution.PlanCanonical, Profile: "go", HeadSHA: execSHA(1), GoverningDigest: m.Digest(), GoverningSource: "V1",
		DeclarationDigest: execDigest("d"), CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff})
	leased := e.grant()

	e.wantError(e.send(http.MethodGet, filePath(leased, missing), nil, withLease(leased.LeaseID)), http.StatusInternalServerError, "internal")

	if !strings.Contains(e.logs.String(), `"level":"ERROR"`) {
		t.Errorf("log = %s; want the failure logged at Error", e.logs.String())
	}
}

func TestFileIsNotTheExecutorBeingSeen(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, defaultFiles)
	leased := e.grant()

	e.send(http.MethodGet, filePath(leased, execDigest(defaultFiles["contract.go"])), nil, withLease(leased.LeaseID))

	if _, requested := e.liveness.LastRequest(); requested {
		t.Error("a file request counts as an executor seen; only a lease request and a heartbeat do")
	}
}
