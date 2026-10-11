package rest

import "testing"

// LimitBodiesTo lowers the response body limit for the duration of the test.
func LimitBodiesTo(t testing.TB, n int64) {
	t.Helper()
	old := maxBody
	maxBody = n
	t.Cleanup(func() { maxBody = old })
}
