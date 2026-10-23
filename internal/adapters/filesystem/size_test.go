package filesystem

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

var _ interface {
	Size(context.Context, artifact.Digest) (int64, error)
} = (*Store)(nil)

func TestSizeIsTheLengthOfTheStoredObjectAndReadsNothing(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	for _, content := range [][]byte{nil, []byte("canonical\r\nbytes\n"), {0, 255, 1, 0}, bytes.Repeat([]byte("x"), 3<<20)} {
		digest := artifact.Hash(content)
		if err := store.Put(context.Background(), digest, bytes.NewReader(content)); err != nil {
			t.Fatal(err)
		}
		store.io.openFile = func(*os.Root, string, int, fs.FileMode) (artifactFile, error) {
			t.Fatal("Size opened the object")
			return nil, nil
		}
		size, err := store.Size(context.Background(), digest)
		if err != nil || size != int64(len(content)) {
			t.Fatalf("Size of %d bytes = %d, %v", len(content), size, err)
		}
		store.io.openFile = func(root *os.Root, name string, flag int, mode fs.FileMode) (artifactFile, error) {
			return root.OpenFile(name, flag, mode)
		}
	}
}

func TestSizeRefusesWhatIsNotAnIntactObject(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	content := []byte("sealed artifact")
	digest := artifact.Hash(content)
	if size, err := store.Size(context.Background(), digest); !errors.Is(err, fs.ErrNotExist) || size != 0 {
		t.Fatalf("missing object Size = %d, %v, want not exist", size, err)
	}
	if _, err := store.Size(context.Background(), artifact.Digest{}); !errors.Is(err, artifact.ErrInvalidDigest) {
		t.Fatalf("absent digest Size = %v, want invalid digest", err)
	}
	if err := os.Mkdir(testObjectPath(store.root, digest), 0o700); err != nil { // an object that is no regular file
		t.Fatal(err)
	}
	if _, err := store.Size(context.Background(), digest); !errors.Is(err, ErrCorruptArtifact) {
		t.Fatalf("Size of an object that is a directory = %v, want corruption", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Size(cancelled, digest); !errors.Is(err, context.Canceled) {
		t.Fatalf("Size with a cancelled context = %v, want context.Canceled", err)
	}
}

func TestSizeReportsAFailureToInspectTheObject(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	failure := errors.New("disk failure")
	store.io.lstat = func(*os.Root, string) (fs.FileInfo, error) { return nil, failure }
	if _, err := store.Size(context.Background(), artifact.Hash([]byte("x"))); !errors.Is(err, failure) {
		t.Fatalf("Size = %v, want the failure of the inspection", err)
	}
	store = newTestStore(t, t.TempDir())
	store.io.openRoot = func(string) (*os.Root, error) { return nil, failure }
	if _, err := store.Size(context.Background(), artifact.Hash([]byte("x"))); !errors.Is(err, failure) {
		t.Fatalf("Size = %v, want the failure to open the root", err)
	}
}
