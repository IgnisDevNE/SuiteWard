package integrity_test

import (
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestNextKeepsAFailedVerdictAgainstAnUnavailableOneOfTheSameVersion(t *testing.T) {
	at := func(n int) time.Time { return time.Date(2026, 10, 19, 12, n, 0, 0, time.UTC) }
	rec := func(version string, v integrity.Verdict, n int) integrity.Record {
		return integrity.Record{Project: "p", Suite: "s", Version: contract.SuiteVersionID("v" + version), Verdict: v, CheckedAt: at(n)}
	}
	tests := []struct {
		name    string
		current integrity.Record
		found   bool
		r       integrity.Record
		want    integrity.Record
	}{
		{"nothing before", integrity.Record{}, false, rec("1", integrity.Unavailable, 2), rec("1", integrity.Unavailable, 2)},
		{"unavailable over failed of the same version only moves the time", rec("1", integrity.Failed, 1), true, rec("1", integrity.Unavailable, 2), rec("1", integrity.Failed, 2)},
		{"unavailable over failed of another version replaces it", rec("1", integrity.Failed, 1), true, rec("2", integrity.Unavailable, 2), rec("2", integrity.Unavailable, 2)},
		{"passed over failed replaces it", rec("1", integrity.Failed, 1), true, rec("1", integrity.Passed, 2), rec("1", integrity.Passed, 2)},
		{"failed over unavailable replaces it", rec("1", integrity.Unavailable, 1), true, rec("1", integrity.Failed, 2), rec("1", integrity.Failed, 2)},
		{"unavailable over passed replaces it", rec("1", integrity.Passed, 1), true, rec("1", integrity.Unavailable, 2), rec("1", integrity.Unavailable, 2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := integrity.Next(tt.current, tt.found, tt.r); got != tt.want {
				t.Fatalf("Next = %+v; want %+v", got, tt.want)
			}
		})
	}
}

