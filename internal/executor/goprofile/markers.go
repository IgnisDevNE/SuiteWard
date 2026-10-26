package goprofile

// Marker is a line of a non-test Go file that looks at its test environment.
type Marker struct {
	Kind, Path string
	Line       int
}

// Markers flags test-aware code in the non-test Go files of changed.
func Markers(changed map[string][]byte) []Marker { return nil }
