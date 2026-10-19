package commands

import "slices"

// Dispositions returns every disposition, for the test that each is placed in the conformance suite.
func Dispositions() []Disposition { return slices.Clone(dispositions) }
