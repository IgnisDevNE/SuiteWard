// Package filesystem stores immutable content-addressed artifact bytes.
package filesystem

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// stagePrefix names unpublished temporary files inside the store root.
const stagePrefix = ".partial-"

var (
	ErrDigestMismatch  = errors.New("artifact content does not match digest")
	ErrCorruptArtifact = errors.New("corrupt artifact object")
)

// Store addresses raw content by identity, never by a candidate path.
type Store struct {
	root string
	io   artifactIO
}

// artifactIO is a private per-store seam for deterministic I/O failure tests.
// Normal operations always use the real filesystem.
type artifactIO struct {
	openRoot func(string) (*os.Root, error)
	openFile func(*os.Root, string, int, fs.FileMode) (artifactFile, error)
	lstat    func(*os.Root, string) (fs.FileInfo, error)
	link     func(*os.Root, string, string) error
	remove   func(*os.Root, string) error
	syncDir  func(*os.Root) error
}

type artifactFile interface {
	io.Reader
	io.Writer
	Sync() error
	Close() error
}

// NewStore creates the instance-owned local volume when it is absent. Opening
// removes every .partial-* file in the root, so exactly one store instance may
// own a root at a time.
func NewStore(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("blank artifact root: %w", fs.ErrInvalid)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	info, err := os.Lstat(abs)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return nil, fmt.Errorf("create artifact root: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("inspect artifact root: %w", err)
	} else if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("artifact root must be a directory without a symlink: %w", fs.ErrInvalid)
	}
	if err := removeOrphanStages(abs); err != nil {
		return nil, err
	}
	return &Store{root: abs, io: artifactIO{
		openRoot: os.OpenRoot,
		openFile: func(root *os.Root, name string, flag int, mode fs.FileMode) (artifactFile, error) {
			return root.OpenFile(name, flag, mode)
		},
		lstat: (*os.Root).Lstat, link: (*os.Root).Link, remove: (*os.Root).Remove,
		syncDir: syncDirectory,
	}}, nil
}

// Put validates every supplied byte stream and atomically publishes without
// replacing an existing object. A duplicate revalidates the existing bytes.
func (s *Store) Put(ctx context.Context, digest artifact.Digest, content io.Reader) (result error) {
	name, err := objectName(digest)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if content == nil {
		return fmt.Errorf("nil artifact source: %w", fs.ErrInvalid)
	}
	root, err := s.io.openRoot(s.root)
	if err != nil {
		return fmt.Errorf("open artifact root: %w", err)
	}
	defer root.Close()
	stage := stagePrefix + rand.Text()
	file, err := s.io.openFile(root, stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create artifact stage: %w", err)
	}
	defer func() {
		if err := s.io.remove(root, stage); err != nil {
			result = errors.Join(result, fmt.Errorf("clean artifact stage: %w", err))
		}
	}()
	hash := sha256.New()
	_, writeErr := io.Copy(io.MultiWriter(file, hash), contextReader{ctx, content})
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write artifact stage: %w", err)
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != digest.String() {
		return ErrDigestMismatch
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.io.link(root, stage, name); errors.Is(err, fs.ErrExist) {
		// A prior or concurrent writer may have linked without syncing yet, so
		// a duplicate is durable only after the directory sync below.
		if err := s.Verify(ctx, digest); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("publish artifact: %w", err)
	}
	if err := s.io.syncDir(root); err != nil {
		return fmt.Errorf("sync artifact directory: %w", err)
	}
	return nil
}

// Read returns independent bytes only after validating their exact identity.
func (s *Store) Read(ctx context.Context, digest artifact.Digest) ([]byte, error) {
	name, err := objectName(digest)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := s.io.openRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("open artifact root: %w", err)
	}
	defer root.Close()
	info, err := s.io.lstat(root, name)
	if err != nil {
		return nil, fmt.Errorf("inspect artifact object: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, ErrCorruptArtifact
	}
	file, err := s.io.openFile(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open artifact object: %w", err)
	}
	content, readErr := io.ReadAll(contextReader{ctx, file})
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("read artifact object: %w", err)
	}
	if artifact.Hash(content) != digest {
		return nil, ErrCorruptArtifact
	}
	return content, nil
}

// Verify checks that the digest names available, intact raw content.
func (s *Store) Verify(ctx context.Context, digest artifact.Digest) error {
	_, err := s.Read(ctx, digest)
	return err
}

func objectName(digest artifact.Digest) (string, error) {
	if digest.IsZero() {
		return "", artifact.ErrInvalidDigest
	}
	return "sha256-" + strings.TrimPrefix(digest.String(), "sha256:"), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if cancelled := r.ctx.Err(); cancelled != nil {
		return n, cancelled
	}
	return n, err
}

// removeOrphanStages deletes regular stage files that interrupted writes left
// in the instance-owned root. Published objects, other names, and symlinks are
// never touched.
func removeOrphanStages(abs string) error {
	root, err := os.OpenRoot(abs)
	if err != nil {
		return fmt.Errorf("open artifact root: %w", err)
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("list artifact root: %w", err)
	}
	entries, err := dir.ReadDir(-1)
	if err := errors.Join(err, dir.Close()); err != nil {
		return fmt.Errorf("list artifact root: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), stagePrefix) || !entry.Type().IsRegular() {
			continue
		}
		if err := root.Remove(entry.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove orphan artifact stage: %w", err)
		}
	}
	return nil
}
