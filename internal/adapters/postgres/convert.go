package postgres

import (
	"fmt"
	"math"
)

// The sqlc parameters use the column types (int32, int64), the domain uses int and uint64. A value outside the
// range is refused, never wrapped or clamped.

func int32Of(n int) (int32, error) {
	if n < math.MinInt32 || n > math.MaxInt32 {
		return 0, fmt.Errorf("%d does not fit a 32-bit column", n)
	}
	return int32(n), nil
}

func uint64Of(n int64) (uint64, error) {
	if n < 0 {
		return 0, fmt.Errorf("%d is negative", n)
	}
	return uint64(n), nil
}

func int64Of(n uint64) (int64, error) {
	if n > math.MaxInt64 {
		return 0, fmt.Errorf("%d exceeds the stored range", n)
	}
	return int64(n), nil
}
