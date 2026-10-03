package filesystem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

var _ interface {
	Verify(context.Context, artifact.Digest) error
} = (*Store)(nil)

func TestArtifactsRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private", "artifacts")
	store := newTestStore(t, root)
	for _, content := range [][]byte{nil, []byte("canonical\r\nbytes\n"), {0, 255, 1, 0}} {
		digest := artifact.Hash(content)
		if err := store.Put(context.Background(), digest, bytes.NewReader(content)); err != nil {
			t.Fatalf("Put verified bytes: %v", err)
		}
		got, err := store.Read(context.Background(), digest)
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("Read exact content = %q, %v; want %q", got, err, content)
		}
		if err := store.Verify(context.Background(), digest); err != nil {
			t.Fatalf("Verify stored bytes: %v", err)
		}
		if len(got) != 0 {
			got[0] ^= 255
			again, err := store.Read(context.Background(), digest)
			if err != nil || !bytes.Equal(again, content) {
				t.Fatalf("mutating returned bytes changed storage: %q, %v", again, err)
			}
		}
	}
}

func TestPutRejectsInvalidAndMismatchedIdentity(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	content := []byte("approved contract")
	digest := artifact.Hash(content)
	if err := store.Put(context.Background(), artifact.Digest{}, bytes.NewReader(content)); !errors.Is(err, artifact.ErrInvalidDigest) {
		t.Fatalf("absent digest = %v, want invalid digest", err)
	}
	if err := store.Put(context.Background(), digest, strings.NewReader("changed contract")); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("mismatched new content = %v, want digest mismatch", err)
	}
	assertNoObjects(t, store.root)
	if err := store.Put(context.Background(), digest, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(testObjectPath(store.root, digest))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), digest, strings.NewReader("changed contract")); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("mismatched duplicate = %v, want digest mismatch", err)
	}
	after, err := os.Stat(testObjectPath(store.root, digest))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("mismatched duplicate replaced existing object: %v", err)
	}
	got, err := store.Read(context.Background(), digest)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("mismatched duplicate changed canonical bytes: %q, %v", got, err)
	}
	for _, action := range []func(context.Context, artifact.Digest) error{
		store.Verify,
		func(ctx context.Context, d artifact.Digest) error { _, err := store.Read(ctx, d); return err },
	} {
		if err := action(context.Background(), artifact.Digest{}); !errors.Is(err, artifact.ErrInvalidDigest) {
			t.Fatalf("absent read identity = %v, want invalid digest", err)
		}
	}
}

func TestConcurrentPutsPreserveExistingBytes(t *testing.T) {
	root := t.TempDir()
	content := []byte("one immutable winner")
	digest := artifact.Hash(content)
	store := newTestStore(t, root)
	runConcurrentPuts(t, root, digest, content)
	before, err := os.Stat(testObjectPath(root, digest))
	if err != nil {
		t.Fatal(err)
	}
	runConcurrentPuts(t, root, digest, content)
	after, err := os.Stat(testObjectPath(root, digest))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("concurrent repeated puts replaced canonical object: %v", err)
	}
	got, err := store.Read(context.Background(), digest)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("concurrent puts damaged canonical bytes: %q, %v", got, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("published objects/stages = %d, %v; want one object", len(entries), err)
	}
}

func TestPartialWriteIsInvisibleAndAborted(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	content := []byte("complete canonical artifact")
	digest := artifact.Hash(content)
	failure := errors.New("source interrupted after partial content")
	reader := &pausedReader{first: content[:7], waiting: make(chan struct{}), release: make(chan struct{}), failure: failure}
	done := make(chan error, 1)
	go func() { done <- store.Put(context.Background(), digest, reader) }()
	select {
	case <-reader.waiting:
	case err := <-done:
		t.Fatalf("Put did not stage the partial source: %v", err)
	}
	for _, action := range []func(context.Context, artifact.Digest) error{
		store.Verify,
		func(ctx context.Context, d artifact.Digest) error { _, err := store.Read(ctx, d); return err },
	} {
		if err := action(context.Background(), digest); !errors.Is(err, fs.ErrNotExist) {
			close(reader.release)
			<-done
			t.Fatalf("partial stage was digest-addressable: %v", err)
		}
	}
	close(reader.release)
	if err := <-done; !errors.Is(err, failure) {
		t.Fatalf("partial reader failure = %v, want original cause", err)
	}
	assertNoObjects(t, store.root)
}

func TestMissingAndCorruptObjectsPreventSuccess(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	content := []byte("sealed artifact")
	digest := artifact.Hash(content)
	if err := store.Verify(context.Background(), digest); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing object verification = %v, want not exist", err)
	}
	if err := store.Put(context.Background(), digest, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	path := testObjectPath(store.root, digest)
	corrupt := []byte("host-corrupted object")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Read(context.Background(), digest); got != nil || !errors.Is(err, ErrCorruptArtifact) {
		t.Fatalf("corrupt Read = %q, %v; want no bytes and corruption", got, err)
	}
	if err := store.Verify(context.Background(), digest); !errors.Is(err, ErrCorruptArtifact) {
		t.Fatalf("corrupt Verify = %v, want corruption", err)
	}
	if err := store.Put(context.Background(), digest, bytes.NewReader(content)); !errors.Is(err, ErrCorruptArtifact) {
		t.Fatalf("duplicate silently repaired corrupt existing object: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, corrupt) {
		t.Fatalf("duplicate changed corrupt existing bytes: %q, %v", got, err)
	}
}

func newTestStore(t *testing.T, root string) *Store {
	t.Helper()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testObjectPath(root string, digest artifact.Digest) string {
	return filepath.Join(root, "sha256-"+strings.TrimPrefix(digest.String(), "sha256:"))
}

func assertNoObjects(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed Put left published/staged content: %v, %v", entries, err)
	}
}

func runConcurrentPuts(t *testing.T, root string, digest artifact.Digest, content []byte) {
	t.Helper()
	const writers = 16
	start := make(chan struct{})
	ready := make(chan struct{}, writers)
	publish := make(chan struct{})
	results := make(chan error, writers)
	for range writers {
		store := newTestStore(t, root)
		link := store.io.link
		store.io.link = func(root *os.Root, old, name string) error {
			ready <- struct{}{}
			<-publish
			return link(root, old, name)
		}
		go func() {
			<-start
			results <- store.Put(context.Background(), digest, bytes.NewReader(content))
		}()
	}
	close(start)
	completed := 0
	for range writers {
		select {
		case <-ready:
		case err := <-results:
			completed++
			t.Errorf("writer failed before competing publication: %v", err)
		}
	}
	close(publish)
	for range writers - completed {
		if err := <-results; err != nil {
			t.Errorf("concurrent content-addressed Put: %v", err)
		}
	}
}

type pausedReader struct {
	first   []byte
	waiting chan struct{}
	release chan struct{}
	failure error
}

func (r *pausedReader) Read(p []byte) (int, error) {
	if len(r.first) != 0 {
		n := copy(p, r.first)
		r.first = r.first[n:]
		return n, nil
	}
	close(r.waiting)
	<-r.release
	return 0, r.failure
}

var _ io.Reader = (*pausedReader)(nil)
