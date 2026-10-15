package integritytest

import (
	"errors"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The suite writes the Suite Project/Suite and the versions V1 and V2, which a store with referential integrity
// must already know.
const (
	Project contract.ProjectID      = "project"
	Suite   contract.SuiteID        = "suite"
	V1      contract.SuiteVersionID = "v1"
	V2      contract.SuiteVersionID = "v2"
)

// at is a time at microsecond precision, which is all PostgreSQL stores, n minutes after a fixed origin.
func at(n int) time.Time {
	return time.Date(2026, 10, 15, 12, 0, 0, 123456000, time.UTC).Add(time.Duration(n) * time.Minute)
}

// RunConformance runs the integrity.Store conformance suite against the stores made by newStore.
func RunConformance(t *testing.T, newStore func(*testing.T) integrity.Store) {
	run := func(name string, test func(t *testing.T, store integrity.Store)) {
		t.Run(name, func(t *testing.T) { test(t, newStore(t)) })
	}
	record := func(t *testing.T, store integrity.Store, version contract.SuiteVersionID, verdict integrity.Verdict, minute int) integrity.Record {
		t.Helper()
		r := integrity.Record{Project: Project, Suite: Suite, Version: version, Verdict: verdict, CheckedAt: at(minute)}
		if err := store.Record(t.Context(), r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	want := func(t *testing.T, store integrity.Store, want integrity.Record) {
		t.Helper()
		got, found, err := store.Latest(t.Context(), Project, Suite)
		if err != nil || !found || got != want {
			t.Fatalf("Latest = %+v, found %v, error %v; want %+v", got, found, err, want)
		}
	}

	run("Latest finds nothing before the first Record", func(t *testing.T, store integrity.Store) {
		if got, found, err := store.Latest(t.Context(), Project, Suite); err != nil || found {
			t.Fatalf("Latest = %+v, found %v, error %v; want no record", got, found, err)
		}
	})

	run("Record replaces the verdict of the Suite", func(t *testing.T, store integrity.Store) {
		record(t, store, V1, integrity.Passed, 1)
		want(t, store, record(t, store, V1, integrity.Failed, 2))
		want(t, store, record(t, store, V1, integrity.Passed, 3))
	})

	run("unavailable after failed for one version keeps failed and moves the check time", func(t *testing.T, store integrity.Store) {
		failed := record(t, store, V1, integrity.Failed, 1)
		record(t, store, V1, integrity.Unavailable, 5)
		failed.CheckedAt = at(5)
		want(t, store, failed)
	})

	run("unavailable replaces passed and unavailable", func(t *testing.T, store integrity.Store) {
		record(t, store, V1, integrity.Passed, 1)
		want(t, store, record(t, store, V1, integrity.Unavailable, 2))
		want(t, store, record(t, store, V1, integrity.Unavailable, 3))
	})

	run("passed after failed clears it", func(t *testing.T, store integrity.Store) {
		record(t, store, V1, integrity.Failed, 1)
		want(t, store, record(t, store, V1, integrity.Passed, 2))
	})

	run("a new version's verdict replaces the older one, also unavailable after failed", func(t *testing.T, store integrity.Store) {
		record(t, store, V1, integrity.Failed, 1)
		want(t, store, record(t, store, V2, integrity.Unavailable, 2))
		want(t, store, record(t, store, V2, integrity.Passed, 3))
	})

	run("a record that fails Validate is refused and changes nothing", func(t *testing.T, store integrity.Store) {
		kept := record(t, store, V1, integrity.Passed, 1)
		for name, bad := range map[string]integrity.Record{
			"unknown verdict": {Project: Project, Suite: Suite, Version: V1, Verdict: "ok", CheckedAt: at(2)},
			"no version":      {Project: Project, Suite: Suite, Verdict: integrity.Failed, CheckedAt: at(2)},
			"no time":         {Project: Project, Suite: Suite, Version: V1, Verdict: integrity.Failed},
		} {
			if err := store.Record(t.Context(), bad); !errors.Is(err, integrity.ErrInvalid) {
				t.Fatalf("%s: Record error = %v; want ErrInvalid", name, err)
			}
		}
		want(t, store, kept)
	})
}
