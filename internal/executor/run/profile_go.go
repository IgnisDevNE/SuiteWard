package run

import (
	"context"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// goProfile is the Go adapter of the seam.
type goProfile struct{}

// goPlan is what goProfile plans of a side.
type goPlan struct{}

func (goProfile) image() string { return goprofile.Image }

func (goProfile) plan(sideFiles, client.Limits, string) (sidePlan, error) { return goPlan{}, nil }

func (goProfile) run(context.Context, invoker, sidePlan, []testID) (map[testID][]execution.Observation, error) {
	return map[testID][]execution.Observation{}, nil
}
