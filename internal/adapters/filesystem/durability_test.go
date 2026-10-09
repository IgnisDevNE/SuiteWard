package filesystem

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

func TestPublicationSyncsParentDirectoryAfterLink(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	content := []byte("directory entry must be durable")
	digest := artifact.Hash(content)
	linked, syncs := false, 0
	originalLink := store.io.link
	store.io.link = func(root *os.Root, old, name string) error {
		err := originalLink(root, old, name)
		linked = err == nil
		return err
	}
	store.io.syncDir = func(*os.Root) error {
		syncs++
		if !linked {
			return errors.New("directory synced before publication")
		}
		return nil
	}
	if err := store.Put(context.Background(), digest, bytes.NewReader(content)); err != nil {
		t.Fatalf("Put with directory sync: %v", err)
	}
	if syncs != 1 {
		t.Fatalf("directory syncs = %d, want 1", syncs)
	}
}

func TestDirectorySyncFailureIsReturned(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	content := []byte("durability failure must not be reported as success")
	digest := artifact.Hash(content)
	failure := errors.New("directory fsync failed")
	store.io.syncDir = func(*os.Root) error { return failure }
	if err := store.Put(context.Background(), digest, bytes.NewReader(content)); !errors.Is(err, failure) {
		t.Fatalf("Put after directory sync failure = %v, want preserved cause", err)
	}
}

func TestOpeningStoreRemovesOnlyOrphanPartials(t *testing.T) {
	root := t.TempDir()
	first := newTestStore(t, root)
	content := []byte("published content survives cleanup")
	digest := artifact.Hash(content)
	if err := first.Put(context.Background(), digest, bytes.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsidePartial := filepath.Join(outside, ".partial-outside")
	if err := os.WriteFile(outsidePartial, []byte("not store content"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphans := []string{".partial-interrupted", ".partial-empty"}
	for _, name := range orphans {
		if err := os.WriteFile(filepath.Join(root, name), []byte("half written"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	unrelated := filepath.Join(root, "not-a-stage")
	if err := os.WriteFile(unrelated, []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}

	store := newTestStore(t, root)

	for _, name := range orphans {
		if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("orphan %s survived opening the store: %v", name, err)
		}
	}
	if _, err := os.Lstat(unrelated); err != nil {
		t.Errorf("non-partial file was removed: %v", err)
	}
	if _, err := os.Stat(outsidePartial); err != nil {
		t.Errorf("partial outside the store root was removed: %v", err)
	}
	if err := store.Verify(context.Background(), digest); err != nil {
		t.Errorf("published artifact damaged by cleanup: %v", err)
	}
}

func TestOpeningStoreNeverFollowsPartialSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "symlink-target")
	if err := os.WriteFile(target, []byte("not store content"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, ".partial-symlink")
	createTestSymlink(t, target, link)

	newTestStore(t, root)

	if got, err := os.ReadFile(target); err != nil || string(got) != "not store content" {
		t.Fatalf("symlink target outside the store was altered: %q, %v", got, err)
	}
}

func TestDuplicatePutSyncsDirectoryBeforeReportingSuccess(t *testing.T) {
	store := newTestStore(t, t.TempDir())
	content := []byte("retry must not report success without a durable entry")
	digest := artifact.Hash(content)
	failure := errors.New("directory fsync failed")
	syncs := 0
	syncErr := failure
	store.io.syncDir = func(*os.Root) error { syncs++; return syncErr }
	if err := store.Put(context.Background(), digest, bytes.NewReader(content)); !errors.Is(err, failure) {
		t.Fatalf("first Put = %v, want directory sync failure", err)
	}
	if err := store.Put(context.Background(), digest, bytes.NewReader(content)); !errors.Is(err, failure) || syncs != 2 {
		t.Fatalf("retry = %v after %d syncs, want the failure from a second sync", err, syncs)
	}
	syncErr = nil
	if err := store.Put(context.Background(), digest, bytes.NewReader(content)); err != nil || syncs != 3 {
		t.Fatalf("retry with working sync = %v after %d syncs, want success from a third sync", err, syncs)
	}
}
