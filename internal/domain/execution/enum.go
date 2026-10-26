package execution

import "fmt"

// parseEnum returns the value of names whose name is s, exactly as spelled, or an error wrapping invalid.
func parseEnum[T comparable](names map[T]string, s string, invalid error) (T, error) {
	for value, name := range names {
		if name == s {
			return value, nil
		}
	}
	var none T
	return none, fmt.Errorf("%w: %q", invalid, s)
}
