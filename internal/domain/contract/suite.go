package contract

type SuiteID string

type SuiteVersionID string

type StateRevision uint64

type Suite struct{}

func NewSuite(project ProjectID, id SuiteID, current SuiteVersionID, revision StateRevision) (Suite, error) {
	return Suite{}, nil
}

func (s Suite) ProjectID() ProjectID {
	return ""
}

func (s Suite) ID() SuiteID {
	return ""
}

func (s Suite) CurrentVersionID() (SuiteVersionID, bool) {
	return "", false
}

func (s Suite) Revision() StateRevision {
	return 0
}
