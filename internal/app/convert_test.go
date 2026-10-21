package app

import (
	"fmt"
	"math"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// testOrder and testID convert between GitHub's int64 comment identifiers and the uint64 command order; a test that
// supplies a value outside the range is broken, so they panic.
func testOrder(id int64) contract.CommandOrder {
	if id < 0 {
		panic(fmt.Sprintf("comment identifier %d is negative", id))
	}
	return contract.CommandOrder(id)
}

func testID(order contract.CommandOrder) int64 {
	if order > math.MaxInt64 {
		panic(fmt.Sprintf("command order %d exceeds int64", order))
	}
	return int64(order)
}
