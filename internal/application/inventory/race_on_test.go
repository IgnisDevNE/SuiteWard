//go:build race

package inventory_test

// raceEnabled tells timing tests that the race detector slows every memory access.
const raceEnabled = true
