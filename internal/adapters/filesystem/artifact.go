// Package filesystem stores immutable content-addressed artifact bytes.
package filesystem

import (
	"context"
	"errors"
	"io"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

var (
	ErrDigestMismatch  = errors.New("artifact content does not match digest")
	ErrCorruptArtifact = errors.New("corrupt artifact object")
)

type Store struct{ root string }

func NewStore(root string) (*Store, error) { return &Store{root: root}, nil }

func (s *Store) Put(context.Context, artifact.Digest, io.Reader) error {
	return errors.New("artifact publication is not implemented")
}

func (s *Store) Read(context.Context, artifact.Digest) ([]byte, error) {
	return nil, errors.New("artifact read is not implemented")
}

func (s *Store) Verify(context.Context, artifact.Digest) error {
	return errors.New("artifact verification is not implemented")
}
