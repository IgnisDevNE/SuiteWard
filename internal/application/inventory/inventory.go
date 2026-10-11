// Package inventory resolves the exact protected inventory of one pinned
// source tree and classifies a candidate tree against the governing contract
// (D-SCOPE, D-SCOPE-DEFAULTS and D-APPROVAL-CONTEXT in docs/plan/decisions.md,
// ADR 0013).
package inventory

import (
	"context"
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// EntryKind is the kind of a source tree entry; its zero value is invalid.
type EntryKind uint8

const (
	File EntryKind = iota + 1
	Symlink
	Submodule
)

// Entry is one non-directory entry of a source tree.
type Entry struct {
	Path string
	Kind EntryKind
}

// Tree is one pinned source revision; directories are implicit.
type Tree interface {
	Entries(context.Context) ([]Entry, error)
	Read(ctx context.Context, path string) ([]byte, error)
}

var (
	ErrInvalidTree      = errors.New("invalid source tree")
	ErrUnsafeEntry      = errors.New("unsafe entry in the protected scope")
	ErrInvalidGoverning = errors.New("invalid governing contract")
)

var errNotImplemented = errors.New("not implemented")

// Inventory is the protected inventory of a tree under its own declaration.
type Inventory struct {
	manifest artifact.Manifest
	scope    scope.Scope
}

// Build resolves the protected inventory of tree under its own declaration.
func Build(ctx context.Context, tree Tree) (Inventory, error) {
	return Inventory{}, errNotImplemented
}

// Manifest returns the exact protected inventory.
func (i Inventory) Manifest() artifact.Manifest {
	panic("not implemented")
}

// Scope returns the effective scope of the tree's declaration.
func (i Inventory) Scope() scope.Scope {
	panic("not implemented")
}

// Contract returns the protected contract the inventory proposes.
func (i Inventory) Contract() (contract.ProtectedContract, error) {
	return contract.ProtectedContract{}, errNotImplemented
}

// Evaluate resolves the manifest of tree under the governing scope instead of
// the tree's own declaration.
func Evaluate(ctx context.Context, tree Tree, governing scope.Scope) (artifact.Manifest, error) {
	return artifact.Manifest{}, errNotImplemented
}

// Change classifies a candidate against the governing contract; its zero
// value is invalid.
type Change uint8

const (
	Unchanged Change = iota + 1
	ProtectedChange
	ScopeReduction
)

// Classification describes how a candidate differs from the governing
// contract. Every list is sorted and never nil.
type Classification struct {
	Change                                               Change
	Added, Removed, Modified, ReducedPaths, ReducedRules []string
}

// Governing is the approved scope and exact inventory of the canonical contract.
type Governing struct {
	Scope    scope.Scope
	Manifest artifact.Manifest
}

// Classify compares candidate with the governing contract.
func Classify(ctx context.Context, governing Governing, candidate Tree) (Classification, Inventory, error) {
	return Classification{}, Inventory{}, errNotImplemented
}
