package contract

import "github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"

// SuiteVersion is an immutable logical version that may share artifact content.
type SuiteVersion struct {
	project  ProjectID
	suite    SuiteID
	id       SuiteVersionID
	manifest artifact.Manifest
}

func NewSuiteVersion(project ProjectID, suite SuiteID, id SuiteVersionID, manifest artifact.Manifest) (SuiteVersion, error) {
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
