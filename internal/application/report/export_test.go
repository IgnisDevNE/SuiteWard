package report

// BuildWithWork is Build with a work budget the test chooses, so that a test can run it out without millions of steps.
func BuildWithWork(changes []FileChange, work int) (Report, error) { return build(changes, work) }
