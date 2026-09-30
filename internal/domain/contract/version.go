package contract

import (
	"errors"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// ErrInvalidSuiteVersion identifies invalid version snapshot input.
var ErrInvalidSuiteVersion = errors.New("invalid suite version")

// SuiteVersion is an immutable logical version that may share artifact content.
type SuiteVersion struct {
	project  ProjectID
	suite    SuiteID
	id       SuiteVersionID
	manifest artifact.Manifest
}

// NewSuiteVersion reconstitutes supplied facts without approving their promotion.
func NewSuiteVersion(project ProjectID, suite SuiteID, id SuiteVersionID, manifest artifact.Manifest) (SuiteVersion, error) {
	if strings.TrimSpace(string(project)) == "" || strings.TrimSpace(string(suite)) == "" || strings.TrimSpace(string(id)) == "" || manifest.IsZero() {
		return SuiteVersion{}, ErrInvalidSuiteVersion
	}
	return SuiteVersion{project: project, suite: suite, id: id, manifest: manifest}, nil
}

func (v SuiteVersion) ProjectID() ProjectID {
	return v.project
}

func (v SuiteVersion) SuiteID() SuiteID {
	return v.suite
}

func (v SuiteVersion) ID() SuiteVersionID {
	return v.id
}

func (v SuiteVersion) Manifest() artifact.Manifest {
	return v.manifest
}
