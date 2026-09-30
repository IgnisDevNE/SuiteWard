package contract

import "github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"

type SuiteVersion struct{}

func NewSuiteVersion(project ProjectID, suite SuiteID, id SuiteVersionID, manifest artifact.Manifest) (SuiteVersion, error) {
	return SuiteVersion{}, nil
}

func (v SuiteVersion) ProjectID() ProjectID {
	return ""
}

func (v SuiteVersion) SuiteID() SuiteID {
	return ""
}

func (v SuiteVersion) ID() SuiteVersionID {
	return ""
}

func (v SuiteVersion) Manifest() artifact.Manifest {
	return artifact.Manifest{}
}
