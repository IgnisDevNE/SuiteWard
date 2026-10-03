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
}

type artifactFile interface {
	io.Reader
	io.Writer
	Sync() error
	Close() error
}

// NewStore creates the instance-owned local volume when it is absent.
func NewStore(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create artifact root: %w", err)
	}
	return &Store{root: abs, io: artifactIO{
		openRoot: os.OpenRoot,
		openFile: func(root *os.Root, name string, flag int, mode fs.FileMode) (artifactFile, error) {
			return root.OpenFile(name, flag, mode)
		},
		lstat: (*os.Root).Lstat, link: (*os.Root).Link, remove: (*os.Root).Remove,
	}}, nil
}

// Put validates every supplied byte stream and atomically publishes without
// replacing an existing object. A duplicate revalidates the existing bytes.
func (s *Store) Put(ctx context.Context, digest artifact.Digest, content io.Reader) error {
	name, err := objectName(digest)
	if err != nil {
		return err
	}
	root, err := s.io.openRoot(s.root)
	if err != nil {
		return fmt.Errorf("open artifact root: %w", err)
	}
	defer root.Close()
	stage := ".partial-" + rand.Text()
	file, err := s.io.openFile(root, stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create artifact stage: %w", err)
	}
	defer s.io.remove(root, stage)
	hash := sha256.New()
	_, writeErr := io.Copy(io.MultiWriter(file, hash), content)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write artifact stage: %w", err)
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != digest.String() {
		return ErrDigestMismatch
	}
	if err := s.io.link(root, stage, name); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return s.Verify(ctx, digest)
		}
		return fmt.Errorf("publish artifact: %w", err)
	}
	return nil
}

// Read returns independent bytes only after validating their exact identity.
func (s *Store) Read(_ context.Context, digest artifact.Digest) ([]byte, error) {
	name, err := objectName(digest)
	if err != nil {
		return nil, err
	}
	root, err := s.io.openRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("open artifact root: %w", err)
	}
	defer root.Close()
	file, err := s.io.openFile(root, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open artifact object: %w", err)
	}
	content, readErr := io.ReadAll(file)
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
