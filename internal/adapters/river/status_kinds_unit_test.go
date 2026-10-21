package river

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// The /status work counts exclude the bookkeeping jobs by kind (queries/status.sql); the list must follow the Go declarations.
func TestStatusQueryExcludesExactlyTheBookkeepingJobKinds(t *testing.T) {
	query, err := os.ReadFile("../postgres/queries/status.sql")
	if err != nil {
		t.Fatal(err)
	}
	list := regexp.MustCompile(`kind NOT IN \(([^)]*)\)`).FindSubmatch(query)
	if list == nil {
		t.Fatal("status.sql has no `kind NOT IN (...)` exclusion list")
	}
	var inSQL []string
	for _, kind := range regexp.MustCompile(`'([^']+)'`).FindAllSubmatch(list[1], -1) {
		inSQL = append(inSQL, string(kind[1]))
	}

	// SyncRepoKind is deliberately not here: a repository synchronization is work and must be counted.
	inGo := []string{relayKind}
	for _, s := range periodicSpecs {
		inGo = append(inGo, s.args.Kind())
	}

	slices.Sort(inSQL)
	slices.Sort(inGo)
	if missing := missingFrom(inGo, inSQL); len(missing) > 0 {
		t.Errorf("status.sql does not exclude %v: a bookkeeping job would count as work", missing)
	}
	if extra := missingFrom(inSQL, inGo); len(extra) > 0 {
		t.Errorf("status.sql excludes %v, which is not a periodic kind nor relayKind: that work would not be counted", extra)
	}
}

// missingFrom is the elements of want that are not in have.
func missingFrom(want, have []string) []string {
	var missing []string
	for _, kind := range want {
		if !slices.Contains(have, kind) {
			missing = append(missing, kind)
		}
	}
	return missing
}
