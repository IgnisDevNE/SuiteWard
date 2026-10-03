package filesystem

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

func TestNewStoreRejectsUnsafeRoots(t *testing.T) {
	for _, root := range []string{"", "  "} {
		if store, err := NewStore(root); err == nil || store != nil {
			t.Errorf("blank root %q accepted: %v, %v", root, store, err)
		}
	}
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{file, filepath.Join(file, "child"), filepath.Join(parent, "invalid\x00root")} {
		if store, err := NewStore(root); err == nil || store != nil {
			t.Errorf("non-directory root accepted: %v, %v", store, err)
		}
	}
	t.Run("symlink", func(t *testing.T) {
		target := t.TempDir()
		link := filepath.Join(parent, "root-link")
		createTestSymlink(t, target, link)
		if store, err := NewStore(link); err == nil || store != nil {
			t.Fatalf("symlink root accepted: %v, %v", store, err)
		}
	})
}

func TestNonregularAndSymlinkObjectsAreRejected(t *testing.T) {
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			store := newTestStore(t, t.TempDir())
			content := []byte("canonical bytes must reside in an object")
			digest := artifact.Hash(content)
			path := testObjectPath(store.root, digest)
			if kind == "directory" {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				target := filepath.Join(t.TempDir(), "candidate-content")
				if err := os.WriteFile(target, content, 0o600); err != nil {
					t.Fatal(err)
				}
				createTestSymlink(t, target, path)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := store.Read(context.Background(), digest); got != nil || !errors.Is(err, ErrCorruptArtifact) {
				t.Errorf("Read unsafe object = %q, %v; want corruption", got, err)
			}
			if err := store.Verify(context.Background(), digest); !errors.Is(err, ErrCorruptArtifact) {
				t.Errorf("Verify unsafe object = %v, want corruption", err)
			}
			if err := store.Put(context.Background(), digest, bytes.NewReader(content)); !errors.Is(err, ErrCorruptArtifact) {
				t.Errorf("Put unsafe existing object = %v, want corruption", err)
			}
			after, err := os.Lstat(path)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("Put replaced unsafe existing object: %v", err)
			}
		})
	}
}

func TestCancellationPreventsArtifactSuccess(t *testing.T) {
	content := []byte("cancelled artifact publication")
	digest := artifact.Hash(content)
	t.Run("before put", func(t *testing.T) {
		store := newTestStore(t, t.TempDir())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := store.Put(ctx, digest, bytes.NewReader(content)); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Put = %v, want cancellation", err)
		}
		assertNoObjects(t, store.root)
	})
	t.Run("reader cancels with final bytes", func(t *testing.T) {
		store := newTestStore(t, t.TempDir())
		ctx, cancel := context.WithCancel(context.Background())
		reader := &cancellingReader{content: content, cancel: cancel}
		if err := store.Put(ctx, digest, reader); !errors.Is(err, context.Canceled) {
			t.Fatalf("source cancellation at EOF published object: %v", err)
		}
		assertNoObjects(t, store.root)
	})
	t.Run("after close before publication", func(t *testing.T) {
		store := newTestStore(t, t.TempDir())
		ctx, cancel := context.WithCancel(context.Background())
		original := store.io.openFile
		store.io.openFile = func(root *os.Root, name string, flags int, mode fs.FileMode) (artifactFile, error) {
			file, err := original(root, name, flags, mode)
			return &faultFile{artifactFile: file, closed: cancel}, err
		}
		if err := store.Put(ctx, digest, bytes.NewReader(content)); !errors.Is(err, context.Canceled) {
			t.Fatalf("post-close cancellation published object: %v", err)
		}
		assertNoObjects(t, store.root)
	})
	t.Run("between streamed chunks", func(t *testing.T) {
		store := newTestStore(t, t.TempDir())
		ctx, cancel := context.WithCancel(context.Background())
		original := store.io.openFile
		store.io.openFile = func(root *os.Root, name string, flags int, mode fs.FileMode) (artifactFile, error) {
			file, err := original(root, name, flags, mode)
			return &faultFile{artifactFile: file, written: cancel}, err
		}
		if err := store.Put(ctx, digest, bytes.NewReader(content)); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation between chunks published object: %v", err)
		}
		assertNoObjects(t, store.root)
	})
	for _, duringRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "before read", true: "during read"}[duringRead], func(t *testing.T) {
			store := newTestStore(t, t.TempDir())
			if err := store.Put(context.Background(), digest, bytes.NewReader(content)); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if duringRead {
				original := store.io.openFile
				store.io.openFile = func(root *os.Root, name string, flags int, mode fs.FileMode) (artifactFile, error) {
					file, err := original(root, name, flags, mode)
					return &faultFile{artifactFile: file, read: cancel}, err
				}
			} else {
				cancel()
			}
			if got, err := store.Read(ctx, digest); got != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled Read = %q, %v", got, err)
			}
			if err := store.Verify(ctx, digest); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled Verify = %v", err)
			}
		})
	}
}

func TestPutOperationalFailuresPreserveExistingObjects(t *testing.T) {
	failure := errors.New("controlled real filesystem operation failure")
	for _, operation := range []string{"open root", "create stage", "write", "sync", "close", "publish"} {
		for _, existing := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/new", true: "/existing"}[existing], func(t *testing.T) {
				store := newTestStore(t, t.TempDir())
				content := []byte("existing canonical content")
				digest := artifact.Hash(content)
				if existing {
					if err := store.Put(context.Background(), digest, bytes.NewReader(content)); err != nil {
						t.Fatal(err)
					}
				}
				injectOperationFailure(store, operation, failure)
				if err := store.Put(context.Background(), digest, bytes.NewReader(content)); !errors.Is(err, failure) {
					t.Fatalf("Put after %s failure = %v, want preserved cause", operation, err)
				}
				if existing {
					got, err := os.ReadFile(testObjectPath(store.root, digest))
					if err != nil || !bytes.Equal(got, content) {
						t.Fatalf("failed Put damaged existing object: %q, %v", got, err)
					}
					entries, err := os.ReadDir(store.root)
					if err != nil || len(entries) != 1 {
						t.Fatalf("failed duplicate left stages: %v, %v", entries, err)
					}
				} else {
					assertNoObjects(t, store.root)
				}
			})
		}
	}
}

func TestSyncAndClosePrecedePublication(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	content := []byte("flush before publication")
	digest := artifact.Hash(content)
	flushed, closed := false, false
	originalOpen, originalLink := store.io.openFile, store.io.link
	store.io.openFile = func(root *os.Root, name string, flags int, mode fs.FileMode) (artifactFile, error) {
		file, err := originalOpen(root, name, flags, mode)
		return &faultFile{artifactFile: file, synced: func() { flushed = true }, closed: func() { closed = true }}, err
	}
	store.io.link = func(root *os.Root, old, name string) error {
		if !flushed || !closed {
			return errors.New("published before successful flush and close")
		}
		return originalLink(root, old, name)
	}
	if err := store.Put(context.Background(), digest, bytes.NewReader(content)); err != nil {
		t.Fatalf("publication must follow flush and close: %v", err)
	}
}

func TestCleanupFailureIsReportedAndPublishedObjectRetriesSafely(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	content := []byte("valid published bytes survive cleanup failure")
	digest := artifact.Hash(content)
	failure := errors.New("staging cleanup unavailable")
	originalRemove := store.io.remove
	store.io.remove = func(*os.Root, string) error { return failure }
	if err := store.Put(context.Background(), digest, bytes.NewReader(content)); !errors.Is(err, failure) {
		t.Fatalf("postpublish cleanup failure hidden: %v", err)
	}
	if err := store.Verify(context.Background(), digest); err != nil {
		t.Fatalf("cleanup failure damaged valid publication: %v", err)
	}
	store.io.remove = originalRemove
	if err := store.Put(context.Background(), digest, bytes.NewReader(content)); err != nil {
		t.Fatalf("retry of immutable published object: %v", err)
	}
	got, err := store.Read(context.Background(), digest)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("safe retry changed object: %q, %v", got, err)
	}
}

func TestCleanupAndSourceFailurePreserveBothCauses(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	sourceFailure := errors.New("source interrupted")
	cleanupFailure := errors.New("cannot unlink stage")
	store.io.remove = func(*os.Root, string) error { return cleanupFailure }
	reader := &immediateFailureReader{failure: sourceFailure}
	err := store.Put(context.Background(), artifact.Hash([]byte("not complete")), reader)
	if !errors.Is(err, sourceFailure) || !errors.Is(err, cleanupFailure) {
		t.Fatalf("failed source and cleanup lost error causes: %v", err)
	}
	if err := store.Verify(context.Background(), artifact.Hash([]byte("not complete"))); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("failed source was published: %v", err)
	}
}

func TestReadAndVerifyPreserveOperationalFailures(t *testing.T) {
	failure := errors.New("controlled artifact read failure")
	for _, operation := range []string{"open root", "lstat", "open object", "read", "close"} {
		t.Run(operation, func(t *testing.T) {
			store := newTestStore(t, t.TempDir())
			content := []byte("read from real filesystem")
			digest := artifact.Hash(content)
			if err := store.Put(context.Background(), digest, bytes.NewReader(content)); err != nil {
				t.Fatal(err)
			}
			injectOperationFailure(store, operation, failure)
			if got, err := store.Read(context.Background(), digest); got != nil || !errors.Is(err, failure) {
				t.Fatalf("failed Read supplied content or lost cause: %q, %v", got, err)
			}
			if err := store.Verify(context.Background(), digest); !errors.Is(err, failure) {
				t.Fatalf("failed Verify lost cause: %v", err)
			}
		})
	}
}

func TestNilSourceIsInvalidAndLeavesNoObject(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	if err := store.Put(context.Background(), artifact.Hash(nil), nil); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("nil source = %v, want invalid request", err)
	}
	assertNoObjects(t, store.root)
}

func injectOperationFailure(store *Store, operation string, failure error) {
	switch operation {
	case "open root":
		store.io.openRoot = func(string) (*os.Root, error) { return nil, failure }
	case "lstat":
		store.io.lstat = func(*os.Root, string) (fs.FileInfo, error) { return nil, failure }
	case "create stage", "open object":
		store.io.openFile = func(*os.Root, string, int, fs.FileMode) (artifactFile, error) { return nil, failure }
	case "publish":
		store.io.link = func(*os.Root, string, string) error { return failure }
	default:
		original := store.io.openFile
		store.io.openFile = func(root *os.Root, name string, flags int, mode fs.FileMode) (artifactFile, error) {
			file, err := original(root, name, flags, mode)
			return &faultFile{artifactFile: file, failure: failure, operation: operation}, err
		}
	}
}

type faultFile struct {
	artifactFile
	failure   error
	operation string
	synced    func()
	closed    func()
	written   func()
	read      func()
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.operation == "write" {
		n, _ := f.artifactFile.Write(p[:len(p)/2])
		return n, f.failure
	}
	n, err := f.artifactFile.Write(p)
	if f.written != nil {
		f.written()
	}
	return n, err
}

func (f *faultFile) Read(p []byte) (int, error) {
	n, err := f.artifactFile.Read(p)
	if f.read != nil {
		f.read()
	}
	if f.operation == "read" {
		return n, f.failure
	}
	return n, err
}

func (f *faultFile) Sync() error {
	if f.synced != nil {
		f.synced()
	}
	if f.operation == "sync" {
		return f.failure
	}
	return f.artifactFile.Sync()
}

func (f *faultFile) Close() error {
	err := f.artifactFile.Close()
	if f.closed != nil {
		f.closed()
	}
	if f.operation == "close" {
		return errors.Join(err, f.failure)
	}
	return err
}

type cancellingReader struct {
	content []byte
	cancel  context.CancelFunc
}

func (r *cancellingReader) Read(p []byte) (int, error) {
	n := copy(p, r.content)
	r.cancel()
	return n, io.EOF
}

type immediateFailureReader struct{ failure error }

func (r *immediateFailureReader) Read([]byte) (int, error) { return 0, r.failure }

func createTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		// Windows ERROR_PRIVILEGE_NOT_HELD is not mapped to fs.ErrPermission.
		if errors.Is(err, fs.ErrPermission) || (runtime.GOOS == "windows" && errors.Is(err, syscall.Errno(1314))) {
			t.Skipf("symlink creation requires host privilege: %v", err)
		}
		t.Fatal(err)
	}
}
