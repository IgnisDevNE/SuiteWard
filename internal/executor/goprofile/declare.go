package goprofile

// DeclaredTests lists the test functions of the _test.go files in files.
func DeclaredTests(files map[string][]byte, goarch string) (Declared, error) {
	return Declared{}, errNotImplemented
}

// ChangedTests lists the test functions that head adds or changes against canonical.
func ChangedTests(head, canonical map[string][]byte, goarch string) (added, changed []TestID, err error) {
	return nil, nil, errNotImplemented
}
