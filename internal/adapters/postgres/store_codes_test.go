//go:build integration

package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// TestEveryStoredCodeRoundTrips writes each enumeration value through the
// store, reads it back, and checks the stable text code in the raw row.
func TestEveryStoredCodeRoundTrips(t *testing.T) {
	w := newPGWorld(t)
	one := newCandidate("p1", "", w.protected("one"), "r1", "r2")
	two := newCandidate("p2", "", w.protected("two"))
	three := newCandidate("p3", "", w.protected("three"))
	four := newCandidate("p4", "", w.protected("four"))
	misgoverned := newCandidateUnderPolicy(fxProject, fxSuite, "p5", "", w.protected("five"), "policy-other")
	r2 := one.proposal.Revisions()[1].Binding()
	failed := must(contract.NewIntegrityEvidence("verifier", "failing", r2, contract.IntegrityFailed))
	unavailable := must(contract.NewIntegrityEvidence("verifier", "offline", r2, contract.IntegrityUnavailable))
	one.assessments = append(one.assessments, must(contract.AssessIntegrity("failing", r2, &failed)), must(contract.AssessIntegrity("offline", r2, &unavailable)))

	seed := w.emptySeed(fxProject, fxSuite, one, two, three, four, misgoverned)
	// Schedule states: p1 closed, p2 promoted, p3 active, p4 and p5 waiting.
	schedule := must(seed.Suite.Schedule.Observe(one.proposal, contract.ObserveClosedUnmerged))
	seed.Suite.Schedule = must(schedule.Observe(two.proposal, contract.ObservePromoted))
	if err := w.store.Seed(t.Context(), seed); err != nil {
		t.Fatal(err)
	}

	agent := must(contract.NewPrincipal("agent", contract.Agent))
	service := must(contract.NewPrincipal("service", contract.Service))
	command := func(actor contract.Principal, c candidate, revision contract.ProposalRevisionID, carrier contract.ApprovalCarrierID, name string, action contract.ConsentAction, order contract.CommandOrder) contract.Command {
		reference := c.current()
		reference.RevisionID = revision
		return must(contract.NewCommand(contract.CommandInput{OperationID: contract.OperationID("op-" + name), SourceCommandID: contract.SourceCommandID("src-" + name),
			Actor: actor, Reference: reference, Carrier: carrier, Action: action, Order: order}))
	}
	type expectation struct {
		command       contract.Command
		outcome       contract.ConsentOutcome
		reason        contract.ConsentReason
		outcomeCode   string
		reasonCode    string
		actorKindCode string
	}
	cases := map[string]expectation{
		"approved":   {command(w.owner, one, "r2", one.carrier, "approved", contract.ApproveConsent, 1), contract.ConsentApproved, contract.ConsentReasonNone, "approved", "none", "human"},
		"revoked":    {command(w.owner, one, "r2", one.carrier, "revoked", contract.RevokeConsent, 2), contract.ConsentRevoked, contract.ConsentReasonNone, "revoked", "none", "human"},
		"noactive":   {command(w.owner, one, "r2", one.carrier, "noactive", contract.RevokeConsent, 3), contract.ConsentNoActiveApproval, contract.ConsentReasonNone, "no_active_approval", "none", "human"},
		"obsolete":   {command(w.owner, one, "r2", one.carrier, "obsolete", contract.ApproveConsent, 2), contract.ConsentRejected, contract.ConsentReasonObsoleteCommand, "rejected", "obsolete_command", "human"},
		"agent":      {command(agent, one, "r2", one.carrier, "agent", contract.ApproveConsent, 1), contract.ConsentRejected, contract.ConsentReasonUnauthorized, "rejected", "unauthorized", "agent"},
		"service":    {command(service, one, "r2", one.carrier, "service", contract.ApproveConsent, 1), contract.ConsentRejected, contract.ConsentReasonUnauthorized, "rejected", "unauthorized", "service"},
		"superseded": {command(w.owner, one, "r1", one.carrier, "superseded", contract.ApproveConsent, 9), contract.ConsentRejected, contract.ConsentReasonSupersededRevision, "rejected", "superseded_revision", "human"},
		"unknown":    {command(w.owner, one, "ghost", one.carrier, "unknown", contract.ApproveConsent, 9), contract.ConsentRejected, contract.ConsentReasonUnknownRevision, "rejected", "unknown_revision", "human"},
		"context":    {command(w.owner, one, "r2", "another-carrier", "context", contract.ApproveConsent, 9), contract.ConsentRejected, contract.ConsentReasonContextMismatch, "rejected", "context_mismatch", "human"},
		"policy":     {command(w.owner, misgoverned, "revision-1", misgoverned.carrier, "policy", contract.ApproveConsent, 1), contract.ConsentRejected, contract.ConsentReasonPolicyMismatch, "rejected", "policy_mismatch", "human"},
	}
	// The order matters: the approvals and revocations build on each other.
	for _, name := range []string{"approved", "revoked", "noactive", "obsolete", "agent", "service", "superseded", "unknown", "context", "policy"} {
		tt := cases[name]
		response, err := governance.ProcessConsent(t.Context(), w.store, governance.ConsentRequest{Command: tt.command})
		if err != nil || response.Receipt.Result.Outcome() != tt.outcome || response.Receipt.Result.Reason() != tt.reason {
			t.Fatalf("%s: outcome %v reason %v err %v; want %v and %v", name, response.Receipt.Result.Outcome(), response.Receipt.Result.Reason(), err, tt.outcome, tt.reason)
		}
		var outcome, reason, actorKind string
		if err := w.db.conn.QueryRow(t.Context(), "SELECT outcome, reason, actor_kind FROM consent_results WHERE source_command_id = $1", string(tt.command.SourceCommandID())).Scan(&outcome, &reason, &actorKind); err != nil ||
			outcome != tt.outcomeCode || reason != tt.reasonCode || actorKind != tt.actorKindCode {
			t.Fatalf("%s: stored %q %q %q, error %v; want %q %q %q", name, outcome, reason, actorKind, err, tt.outcomeCode, tt.reasonCode, tt.actorKindCode)
		}
	}
	for _, c := range []candidate{one, misgoverned} {
		w.read(func(ctx context.Context, tx governance.Tx) error {
			_, consent, err := tx.Proposal(ctx, c.current().ProposalID)
			if err != nil {
				t.Fatalf("reload %s: %v", c.id, err)
			}
			reloaded := 0
			for _, result := range consent.Results() {
				var tt *expectation
				for _, candidateCase := range cases {
					if candidateCase.command.SourceCommandID() == result.Command().SourceCommandID() {
						tt = &candidateCase
					}
				}
				if tt == nil || result.Outcome() != tt.outcome || result.Reason() != tt.reason || result.Command().Actor() != tt.command.Actor() {
					t.Fatalf("%s: result %v/%v of %v does not match what was written", c.id, result.Outcome(), result.Reason(), result.Command().Actor())
				}
				reloaded++
			}
			want := 0
			for _, tt := range cases {
				if tt.command.Reference().ProposalID == c.current().ProposalID {
					want++
				}
			}
			if reloaded != want {
				t.Fatalf("%s reloaded %d results; want %d", c.id, reloaded, want)
			}
			return nil
		})
	}

	stored := func(query string) string {
		rows, err := w.db.conn.Query(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var values []string
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				t.Fatal(err)
			}
			values = append(values, value)
		}
		return strings.Join(values, ",")
	}
	if got := stored("SELECT state FROM schedule_entries ORDER BY position"); got != "closed,promoted,active,waiting,waiting" {
		t.Fatalf("stored schedule states %s", got)
	}
	states := map[contract.ProposalID]contract.ScheduleEntryState{}
	for _, entry := range w.state().Schedule.Entries() {
		states[entry.ProposalID()] = entry.State()
	}
	if states["p1"] != contract.ScheduleClosed || states["p2"] != contract.SchedulePromoted || states["p3"] != contract.ScheduleActive || states["p4"] != contract.ScheduleWaiting {
		t.Fatalf("schedule states read back %v", states)
	}
	if got := stored("SELECT outcome FROM assessments WHERE source IN ('failing', 'offline') ORDER BY source"); got != "failed,unavailable" {
		t.Fatalf("stored integrity outcomes %s", got)
	}
	for source, want := range map[contract.SourceRevision]contract.IntegrityReason{"failing": contract.IntegrityReasonFailed, "offline": contract.IntegrityReasonUnavailable} {
		w.read(func(ctx context.Context, tx governance.Tx) error {
			assessment, found, err := tx.Assessment(ctx, r2.Reference(), source)
			if err != nil || !found || assessment.Reason() != want {
				t.Fatalf("assessment %q reads back reason %v found=%v err=%v; want %v", source, assessment.Reason(), found, err, want)
			}
			return nil
		})
	}
	if got := stored("SELECT owner_kind FROM policies"); got != "human" {
		t.Fatalf("stored policy owner kind %s", got)
	}
}
