package postgres

import (
	"math"
	"testing"
)

func TestInt32OfRefusesWhatDoesNotFit(t *testing.T) {
	for _, n := range []int{0, 1, math.MaxInt32, math.MinInt32} {
		got, err := int32Of(n)
		if err != nil || int(got) != n {
			t.Errorf("int32Of(%d) = %d, %v; want the same value", n, got, err)
		}
	}
	for _, n := range []int{math.MaxInt32 + 1, math.MinInt32 - 1, math.MaxInt64} {
		if got, err := int32Of(n); err == nil {
			t.Errorf("int32Of(%d) = %d, nil; want an error, not a wrapped value", n, got)
		}
	}
}

func TestUint64OfRefusesNegative(t *testing.T) {
	if got, err := uint64Of(math.MaxInt64); err != nil || got != math.MaxInt64 {
		t.Errorf("uint64Of(MaxInt64) = %d, %v", got, err)
	}
	if got, err := uint64Of(-1); err == nil {
		t.Errorf("uint64Of(-1) = %d, nil; want an error, not a wrapped value", got)
	}
}

func TestInt64OfRefusesAboveRange(t *testing.T) {
	if got, err := int64Of(math.MaxInt64); err != nil || got != math.MaxInt64 {
		t.Errorf("int64Of(MaxInt64) = %d, %v", got, err)
	}
	if got, err := int64Of(math.MaxInt64 + 1); err == nil {
		t.Errorf("int64Of(MaxInt64+1) = %d, nil; want an error, not a wrapped value", got)
	}
}
