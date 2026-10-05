package contract

import (
	"errors"
	"strings"
)

// ErrInvalidSuite identifies invalid suite snapshot input.
var ErrInvalidSuite = errors.New("invalid suite")

// SuiteID identifies a suite independently of its source provider.
type SuiteID string

// SuiteVersionID identifies a logical version, rather than its content digest.
type SuiteVersionID string

// StateRevision counts canonical changes of a Suite, starting at an initial zero.
type StateRevision int64

// Suite is an immutable snapshot of a suite's canonical reference.
type Suite struct {
	project  ProjectID
	id       SuiteID
	current  SuiteVersionID
	revision StateRevision
}

// NewSuite reconstitutes supplied state; it does not authorize canonical promotion.
func NewSuite(project ProjectID, id SuiteID, current SuiteVersionID, revision StateRevision) (Suite, error) {
	if strings.TrimSpace(string(project)) == "" || strings.TrimSpace(string(id)) == "" {
		return Suite{}, ErrInvalidSuite
	}
	if current != "" && strings.TrimSpace(string(current)) == "" {
		return Suite{}, ErrInvalidSuite
	}
	return Suite{project: project, id: id, current: current, revision: revision}, nil
}

func (s Suite) ProjectID() ProjectID {
	return s.project
}

func (s Suite) ID() SuiteID {
	return s.id
}

func (s Suite) CurrentVersionID() (SuiteVersionID, bool) {
	return s.current, s.current != ""
}

func (s Suite) Revision() StateRevision {
	return s.revision
}
