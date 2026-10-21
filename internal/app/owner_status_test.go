package app

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync/githubsynctest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner/ownertest"
)

const differingTierNote = "The configured tier and the tier recorded at enrollment differ, so the effective tier is co-located until setup is run again."

var enrolledAt = time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)

// enrolledStore is an owner store enrolled with the given tier, or an empty one when tier is "".
func enrolledStore(t *testing.T, tier string) *ownertest.Memory {
	t.Helper()
	store := ownertest.NewMemory()
	if tier == "" {
		return store
	}
	err := store.Enroll(t.Context(), owner.Enrollment{
		Owner: owner.Owner{AccountID: 884695, Login: "magalz"}, Secret: bytes.Repeat([]byte{7}, 20), Step: 100, Tier: tier, EnrolledAt: enrolledAt,
		Projects: []owner.ProjectPolicy{{RepositoryID: 10, Repo: "acme/widgets", TestCheck: "ci / test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// tierCases are the combinations of the configured tier and the enrollment ("" is no enrollment).
var tierCases = map[string]struct {
	configured, enrolled string
	wantTier, wantNote   string
}{
	"not enrolled, isolated":            {"isolated", "", "isolated", ""},
	"not enrolled, co-located":          {"co-located", "", "co-located", ""},
	"both isolated":                     {"isolated", "isolated", "isolated", ""},
	"both co-located":                   {"co-located", "co-located", "co-located", ""},
	"enrolled co-located, now isolated": {"isolated", "co-located", "co-located", differingTierNote},
	"enrolled isolated, now co-located": {"co-located", "isolated", "co-located", differingTierNote},
}

func TestEffectiveTier(t *testing.T) {
	for name, test := range tierCases {
		t.Run(name, func(t *testing.T) {
			state, found, err := enrolledStore(t, test.enrolled).Current(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			tier, note := effectiveTier(test.configured, state, found)
			if tier != test.wantTier || note != test.wantNote {
				t.Fatalf("effectiveTier(%q, enrolled %q) = %q, %q; want %q, %q", test.configured, test.enrolled, tier, note, test.wantTier, test.wantNote)
			}
		})
	}
}

func TestStatusReportsTheOwnerAndTheEffectiveTier(t *testing.T) {
	for name, test := range tierCases {
		t.Run(name, func(t *testing.T) {
			a := &api{logger: quietLogger(), tier: test.configured, owners: enrolledStore(t, test.enrolled), store: fakeStore{version: 12}}

			code, body := call(t, a, http.MethodGet, "/status")

			want := `{"version":"` + Version + `","instance":"","schemaVersion":12,"tier":"` + test.wantTier + `",
				"jobs":{"available":0,"running":0,"retryable":0,"scheduled":0,"completed":0,"discarded":0},
				"outbox":{"pending":0,"delivered":0,"failed":0}`
			if test.wantNote != "" {
				want += `,"tierNote":"` + test.wantNote + `"`
			}
			if test.enrolled != "" {
				want += `,"owner":{"login":"magalz","accountId":884695,"enrolledAt":"2026-10-13T09:30:00Z"}`
			}
			want += `}`
			if code != http.StatusOK || !jsonEqual(t, body, want) {
				t.Fatalf("GET /status = %d %s; want 200 %s", code, body, want)
			}
		})
	}
}

// failingOwners fails the read with a cause that must not reach a response.
type failingOwners struct{ cause error }

func (f failingOwners) Current(context.Context) (owner.State, bool, error) {
	return owner.State{}, false, f.cause
}

func (f failingOwners) Protection(context.Context) ([]owner.ProtectionResult, error) {
	return nil, f.cause
}

func TestStatusIsUnavailableWhenTheOwnerCannotBeRead(t *testing.T) {
	a := &api{logger: quietLogger(), owners: failingOwners{errors.New("dial tcp db.internal:5432: password=hunter2")}, store: fakeStore{version: 12}}

	code, body := call(t, a, http.MethodGet, "/status")

	if code != http.StatusServiceUnavailable || strings.Contains(body, "hunter2") || !strings.Contains(body, "database unavailable") {
		t.Fatalf("GET /status = %d %s; want 503 database unavailable without the cause", code, body)
	}
}

func TestMCPStatusReportsTheOwnerAndTheEffectiveTierWithAndWithoutGitHub(t *testing.T) {
	for _, repos := range [][]string{nil, {"acme/ok"}} {
		for name, test := range tierCases {
			t.Run(name+" with "+strings.Join(repos, ""), func(t *testing.T) {
				var logs bytes.Buffer
				b := testBackend(githubsynctest.NewMemory(), &fakeInserter{}, &logs, repos...)
				b.tier, b.owners = test.configured, enrolledStore(t, test.enrolled)

				view, err := b.Status(t.Context())

				if err != nil {
					t.Fatal(err)
				}
				if view.Tier != test.wantTier || view.TierNote != test.wantNote {
					t.Fatalf("Status tier = %q, note %q; want %q, %q", view.Tier, view.TierNote, test.wantTier, test.wantNote)
				}
				if test.enrolled == "" {
					if view.Owner != nil {
						t.Fatalf("Status owner = %+v; want none before setup", *view.Owner)
					}
					return
				}
				if view.Owner == nil || view.Owner.Login != "magalz" || view.Owner.AccountID != 884695 || !view.Owner.EnrolledAt.Equal(enrolledAt) {
					t.Fatalf("Status owner = %+v; want magalz, 884695, enrolled %v", view.Owner, enrolledAt)
				}
			})
		}
	}
}

func TestMCPStatusFailsAndLogsWhenTheOwnerCannotBeRead(t *testing.T) {
	cause := errors.New("SECRET-CAUSE db.internal")
	var logs bytes.Buffer
	b := testBackend(githubsynctest.NewMemory(), &fakeInserter{}, &logs)
	b.owners = failingOwners{cause}

	_, err := b.Status(t.Context())

	if !errors.Is(err, cause) {
		t.Fatalf("Status error = %v; want the cause wrapped, for the handler to hide", err)
	}
	if !strings.Contains(logs.String(), `"level":"ERROR"`) || !strings.Contains(logs.String(), "SECRET-CAUSE") {
		t.Fatalf("logged %q; want the cause in an error entry", logs.String())
	}
}
