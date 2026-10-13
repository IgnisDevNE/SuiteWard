package owner_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
)

func validEnrollment() owner.Enrollment {
	return owner.Enrollment{
		Owner:      owner.Owner{AccountID: 884695, Login: "magalz"},
		Secret:     bytes.Repeat([]byte{0xA7}, 20),
		Projects:   []owner.ProjectPolicy{{RepositoryID: 1, Repo: "acme/widgets", TestCheck: "ci / test"}},
		Step:       100,
		Tier:       owner.TierIsolated,
		EnrolledAt: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC),
	}
}

func TestIdentifiers(t *testing.T) {
	if got := owner.ProjectID(12345); got != "github-repo:12345" {
		t.Errorf("ProjectID = %q", got)
	}
	if got := owner.PrincipalID(884695); got != "github:884695" {
		t.Errorf("PrincipalID = %q", got)
	}
	if got := owner.RevisionID(3); got != "setup-3" {
		t.Errorf("RevisionID = %q", got)
	}
}

func TestEnrollmentValidate(t *testing.T) {
	if err := validEnrollment().Validate(); err != nil {
		t.Fatalf("a valid enrollment: %v", err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*owner.Enrollment)
	}{
		{"secret of 19 bytes", func(e *owner.Enrollment) { e.Secret = e.Secret[:19] }},
		{"secret of 21 bytes", func(e *owner.Enrollment) { e.Secret = append(e.Secret, 1) }},
		{"no secret", func(e *owner.Enrollment) { e.Secret = nil }},
		{"account id zero", func(e *owner.Enrollment) { e.Owner.AccountID = 0 }},
		{"account id negative", func(e *owner.Enrollment) { e.Owner.AccountID = -1 }},
		{"repository id zero", func(e *owner.Enrollment) { e.Projects[0].RepositoryID = 0 }},
		{"repository id negative", func(e *owner.Enrollment) { e.Projects[0].RepositoryID = -4 }},
		{"empty test check", func(e *owner.Enrollment) { e.Projects[0].TestCheck = "" }},
		{"blank test check", func(e *owner.Enrollment) { e.Projects[0].TestCheck = " \t" }},
		{"oversized test check", func(e *owner.Enrollment) { e.Projects[0].TestCheck = strings.Repeat("c", owner.MaxTestCheckBytes+1) }},
		{"no tier", func(e *owner.Enrollment) { e.Tier = "" }},
		{"unknown tier", func(e *owner.Enrollment) { e.Tier = "cloud" }},
		{"no projects", func(e *owner.Enrollment) { e.Projects = nil }},
		{"duplicate repository id", func(e *owner.Enrollment) {
			e.Projects = append(e.Projects, owner.ProjectPolicy{RepositoryID: 1, Repo: "acme/other", TestCheck: "ci / test"})
		}},
		{"zero enrollment time", func(e *owner.Enrollment) { e.EnrolledAt = time.Time{} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := validEnrollment()
			test.mutate(&e)
			if err := e.Validate(); !errors.Is(err, owner.ErrInvalidState) {
				t.Fatalf("Validate = %v; want ErrInvalidState", err)
			}
		})
	}
	t.Run("a test check at the bound, shared by two repositories", func(t *testing.T) {
		e := validEnrollment()
		e.Projects[0].TestCheck = strings.Repeat("c", owner.MaxTestCheckBytes)
		e.Projects = append(e.Projects, owner.ProjectPolicy{RepositoryID: 2, Repo: "acme/gadgets", TestCheck: e.Projects[0].TestCheck})
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestProtectionResultValidate(t *testing.T) {
	checked := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	valid := []owner.ProtectionResult{
		{RepositoryID: 1, Result: owner.ResultEnforced, CheckedAt: checked},
		{RepositoryID: 1, Result: owner.ResultEnforced, Caveats: []string{owner.CaveatRulesetBypass}, CheckedAt: checked},
		{RepositoryID: 1, Result: owner.ResultFindings, Findings: []string{owner.FindingAdminBypass, owner.FindingMissingContractCheck, owner.FindingMissingTestCheck, owner.FindingNotUpToDate}, CheckedAt: checked},
		{RepositoryID: 1, Result: owner.ResultEnforcementUnavailable, CheckedAt: checked},
		{RepositoryID: 1, Result: owner.ResultUnknown, CheckedAt: checked},
	}
	for _, r := range valid {
		if err := r.Validate(); err != nil {
			t.Errorf("%+v: %v", r, err)
		}
	}
	for _, test := range []struct {
		name string
		r    owner.ProtectionResult
	}{
		{"repository id zero", owner.ProtectionResult{Result: owner.ResultEnforced, CheckedAt: checked}},
		{"unknown result", owner.ProtectionResult{RepositoryID: 1, Result: "fine", CheckedAt: checked}},
		{"empty result", owner.ProtectionResult{RepositoryID: 1, CheckedAt: checked}},
		{"findings result without findings", owner.ProtectionResult{RepositoryID: 1, Result: owner.ResultFindings, CheckedAt: checked}},
		{"findings with another result", owner.ProtectionResult{RepositoryID: 1, Result: owner.ResultEnforced, Findings: []string{owner.FindingAdminBypass}, CheckedAt: checked}},
		{"unknown finding", owner.ProtectionResult{RepositoryID: 1, Result: owner.ResultFindings, Findings: []string{"other"}, CheckedAt: checked}},
		{"unsorted findings", owner.ProtectionResult{RepositoryID: 1, Result: owner.ResultFindings, Findings: []string{owner.FindingNotUpToDate, owner.FindingAdminBypass}, CheckedAt: checked}},
		{"duplicate finding", owner.ProtectionResult{RepositoryID: 1, Result: owner.ResultFindings, Findings: []string{owner.FindingAdminBypass, owner.FindingAdminBypass}, CheckedAt: checked}},
		{"unknown caveat", owner.ProtectionResult{RepositoryID: 1, Result: owner.ResultEnforced, Caveats: []string{"other"}, CheckedAt: checked}},
		{"duplicate caveat", owner.ProtectionResult{RepositoryID: 1, Result: owner.ResultEnforced, Caveats: []string{owner.CaveatRulesetBypass, owner.CaveatRulesetBypass}, CheckedAt: checked}},
		{"zero check time", owner.ProtectionResult{RepositoryID: 1, Result: owner.ResultEnforced}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.r.Validate(); !errors.Is(err, owner.ErrInvalidState) {
				t.Fatalf("Validate = %v; want ErrInvalidState", err)
			}
		})
	}
}

// The secret appears in no output of any verb, encoder or logger, alone or inside the types that hold it.
func TestSecretNeverRenders(t *testing.T) {
	raw := []byte("SECRET-BYTES-0123456")
	if len(raw) != 20 {
		t.Fatal("the fixture secret must be 20 bytes")
	}
	e := validEnrollment()
	e.Secret = raw
	state := owner.State{Owner: e.Owner, Secret: raw, Projects: e.Projects, Tier: e.Tier, PolicyRevision: "setup-1", EnrolledAt: e.EnrolledAt}
	wrapper := struct{ Inner owner.State }{state}

	decimal := strings.Trim(fmt.Sprintf("%d", raw), "[]")
	leaks := []string{string(raw), hex.EncodeToString(raw), strings.ToUpper(hex.EncodeToString(raw)), base64.StdEncoding.EncodeToString(raw),
		base64.RawStdEncoding.EncodeToString(raw), decimal}
	var outputs []string
	for _, value := range []any{e, &e, state, &state, e.Secret, wrapper, []owner.State{state}} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%b", "%o", "%c", "%U"} {
			outputs = append(outputs, fmt.Sprintf(verb, value))
		}
		if encoded, err := json.Marshal(value); err == nil {
			outputs = append(outputs, string(encoded))
		}
		var text, jsonText strings.Builder
		slog.New(slog.NewTextHandler(&text, nil)).Info("m", "v", value)
		slog.New(slog.NewJSONHandler(&jsonText, nil)).Info("m", "v", value)
		outputs = append(outputs, text.String(), jsonText.String())
	}
	for _, output := range outputs {
		for _, leak := range leaks {
			if strings.Contains(output, leak) {
				t.Fatalf("output %q contains the secret as %q", output, leak)
			}
		}
	}
	if got := fmt.Sprintf("%v", e.Secret); !strings.Contains(got, "redacted") {
		t.Fatalf("a secret renders as %q; want a redaction marker", got)
	}
}
