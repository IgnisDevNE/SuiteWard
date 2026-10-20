package app

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

// What the job remembers in memory (the four maps of checksJob) follows the open pull requests of each repository: a pull request that closed
// is forgotten, so a long-running host does not keep one entry per pull request it ever checked.

// jobMemory is what the job remembers, as the keys of each map.
type jobMemory struct {
	unchanged, scoped, announced, runners []string
}

func rememberedBy(j *checksJob) jobMemory {
	return jobMemory{
		unchanged: slices.Sorted(maps.Keys(stringKeys(j.unchanged))),
		scoped:    slices.Sorted(maps.Keys(stringKeys(j.scoped))),
		announced: slices.Sorted(maps.Keys(j.announced)),
		runners:   slices.Sorted(maps.Keys(stringKeys(j.runners))),
	}
}

func stringKeys[V any](m map[checkPull]V) map[string]bool {
	keys := map[string]bool{}
	for k := range m {
		keys[fmt.Sprintf("%d/%d", k.repositoryID, k.number)] = true
	}
	return keys
}

// fillPullFive makes the job remember something of pull 5 of the repository in all four maps, by the paths that do it in a run.
func fillPullFive(t *testing.T, w *cjJobWorld) {
	t.Helper()
	w.runnerPull(withFiles(bootFiles("v1"), cjJest), added(cjJest)...)
	policy := owner.ProjectPolicy{RepositoryID: bootRepoID, Repo: bootRepo}
	for _, reference := range []string{"", "P1-R1"} {
		if err := w.check.announce(t.Context(), policy, 5, reference); err != nil {
			t.Fatal(err)
		}
	}
	key := checkPull{bootRepoID, 5}
	at := unchangedAt{cjHead2, "V1"}
	w.check.unchanged[key] = at // pull 5 is not an unchanged head, so the run does not remember one
	if w.check.scoped[key] == (unchangedAt{}) || w.check.runners[key] == nil || len(w.check.announced) == 0 {
		t.Fatalf("the setup did not fill the memory of pull 5: %+v", rememberedBy(w.check))
	}
}

func TestChecksForgetAPullRequestThatClosed(t *testing.T) {
	w := newCJWorld(t, true)
	w.sync(map[int]string{4: bootHead})
	w.mustCheck()
	open := rememberedBy(w.check) // what the open pull 4 alone leaves

	fillPullFive(t, w)
	w.sync(map[int]string{4: bootHead}) // pull 5 closed
	w.mustCheck()

	if got := rememberedBy(w.check); !equalMemory(got, open) {
		t.Fatalf("after pull 5 closed the job remembers %+v, want what pull 4 alone leaves: %+v", got, open)
	}
}

func TestChecksKeepWhatTheyRememberWhenTheListingOfTheRepositoryFailed(t *testing.T) {
	w := newCJWorld(t, true)
	fillPullFive(t, w)
	before := rememberedBy(w.check)
	w.sync(map[int]string{4: bootHead}) // pull 5 closed, but this run cannot read the observations
	w.check.pulls = failingPulls{errors.New("store down")}

	if err := w.runChecks(); err == nil {
		t.Fatal("the run did not fail with the listing")
	}

	if got := rememberedBy(w.check); !equalMemory(got, before) {
		t.Fatalf("a failed listing changed the memory: got %+v, want %+v", got, before)
	}
}

func TestChecksKeepWhatTheyRememberOfAnotherRepository(t *testing.T) {
	w := newCJWorld(t, true)
	other := checkPull{bootRepoID + 1, 5}
	w.check.scoped = map[checkPull]unchangedAt{other: {cjHead2, "V1"}}
	w.check.announced = map[string]bool{"scope-set:1002:5:abc": true}
	w.sync(map[int]string{4: bootHead})

	w.mustCheck()

	if _, kept := w.check.scoped[other]; !kept || !w.check.announced["scope-set:1002:5:abc"] {
		t.Fatalf("a run of one repository dropped what the job remembers of another: %+v", rememberedBy(w.check))
	}
}

type failingPulls struct{ err error }

func (f failingPulls) Pulls(context.Context, string) ([]githubsync.PullState, error) {
	return nil, f.err
}

func equalMemory(a, b jobMemory) bool {
	return slices.Equal(a.unchanged, b.unchanged) && slices.Equal(a.scoped, b.scoped) && slices.Equal(a.announced, b.announced) && slices.Equal(a.runners, b.runners)
}
