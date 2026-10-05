//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// tamper rewrites stored facts the way a damaged or hand-edited database would
// hold them. The statements run as the owner of the isolated schema: the
// immutability triggers are switched off first, because the question is how
// the store reads state it never wrote, not whether the schema refuses it.
func (w *pgWorld) tamper(statements ...string) {
	w.t.Helper()
	for _, table := range governanceTables {
		if _, err := w.db.conn.Exec(w.t.Context(), "ALTER TABLE "+table+" DISABLE TRIGGER USER"); err != nil {
			w.t.Fatal(err)
		}
	}
	for _, statement := range statements {
		if _, err := w.db.conn.Exec(w.t.Context(), statement); err != nil {
			w.t.Fatalf("tamper %q: %v", statement, err)
		}
	}
}

// badDigest is a well-formed digest of something else.
var badDigest = "sha256:" + strings.Repeat("a", 64)

// damagedWorld holds a promoted baseline p0 (version v0) and an approved
// candidate p1 with two revisions, every one with its assessments.
func damagedWorld(t *testing.T) (*pgWorld, candidate) {
	t.Helper()
	w := newPGWorld(t)
	base := newCandidate("p0", "", w.protected("baseline"))
	one := newCandidate("p1", "v0", w.protected("change"), "r1", "r2")
	w.seedWithBaseline(base, one)
	w.approve(one)
	return w, one
}

func p1Reference(revision contract.ProposalRevisionID) contract.ProposalReference {
	return contract.ProposalReference{ProjectID: fxProject, SuiteID: fxSuite, ProposalID: "p1", RevisionID: revision}
}

func dropConstraint(table, name string) string {
	return "ALTER TABLE " + table + " DROP CONSTRAINT " + name
}

func renameTable(table string) string { return "ALTER TABLE " + table + " RENAME TO " + table + "_gone" }

func TestReadsRefuseStoredStateThatCannotBeRebuilt(t *testing.T) {
	suite := func(ctx context.Context, tx governance.Tx) error { _, err := tx.Suite(ctx); return err }
	proposal := func(id contract.ProposalID) func(context.Context, governance.Tx) error {
		return func(ctx context.Context, tx governance.Tx) error { _, _, err := tx.Proposal(ctx, id); return err }
	}
	assessment := func(reference contract.ProposalReference, source contract.SourceRevision) func(context.Context, governance.Tx) error {
		return func(ctx context.Context, tx governance.Tx) error { _, _, err := tx.Assessment(ctx, reference, source); return err }
	}
	version := func(id contract.SuiteVersionID) func(context.Context, governance.Tx) error {
		return func(ctx context.Context, tx governance.Tx) error { _, _, err := tx.Version(ctx, id); return err }
	}
	promotionFor := func(ctx context.Context, tx governance.Tx) error {
		_, _, err := tx.PromotionFor(ctx, contract.ProposalReference{ProjectID: fxProject, SuiteID: fxSuite, ProposalID: "p0", RevisionID: "revision-1"})
		return err
	}
	receipt := func(ctx context.Context, tx governance.Tx) error { _, _, err := tx.Receipt(ctx, "approve-p1"); return err }
	receiptBySource := func(ctx context.Context, tx governance.Tx) error {
		_, _, err := tx.ReceiptBySource(ctx, "comment-p1")
		return err
	}
	brokenCovered := `UPDATE proposal_revisions SET covered_inputs = '{"runner":1}' WHERE proposal_id = `
	cases := []struct {
		name     string
		sql      []string
		call     func(context.Context, governance.Tx) error
		invalid  bool // the stored state contradicts itself; otherwise the storage failed
		contains string
	}{
		// Suite: the governing policy.
		{"a Suite whose policy revision is gone", []string{dropConstraint("suites", "suites_policy_fkey"), "DELETE FROM policies"}, suite, true, "governing policy"},
		{"a policy table that cannot be read", []string{renameTable("policies")}, suite, false, "load governing policy"},
		{"a policy owner of an unknown kind", []string{dropConstraint("policies", "policies_owner_kind_check"), "UPDATE policies SET owner_kind = 'robot'"}, suite, true, "policy owner"},
		{"a policy owner without an identity", []string{dropConstraint("policies", "policies_owner_id_check"), "UPDATE policies SET owner_id = ''"}, suite, true, "policy owner"},
		{"a policy owned by an agent", []string{"UPDATE policies SET owner_kind = 'agent'"}, suite, true, "governing policy"},
		// Suite: the canonical version.
		{"a current version that is only spaces", []string{dropConstraint("suites", "suites_current_version_fkey"), dropConstraint("suites", "suites_current_version_id_check"),
			"UPDATE suites SET current_version_id = ' '"}, suite, true, "suite"},
		{"a current version that is not stored", []string{dropConstraint("suites", "suites_current_version_fkey"), "UPDATE suites SET current_version_id = 'ghost'"}, suite, true, "current version"},
		{"a current version whose binding cannot be rebuilt", []string{brokenCovered + "'p0'"}, suite, true, "binding of version v0"},

		// Proposal and its revisions.
		{"a proposal table that cannot be read", []string{renameTable("proposals")}, proposal("p1"), false, "load proposal"},
		{"a revision table that cannot be read", []string{renameTable("proposal_revisions")}, proposal("p1"), false, "load proposal revisions"},
		{"a revision whose covered inputs are not text", []string{brokenCovered + "'p1' AND revision_id = 'r1'"}, proposal("p1"), true, "proposal revision r1"},
		{"a revision without an origin", []string{dropConstraint("proposal_revisions", "proposal_revisions_origin_check"),
			"UPDATE proposal_revisions SET origin = '' WHERE proposal_id = 'p1' AND revision_id = 'r1'"}, proposal("p1"), true, "proposal revision r1"},
		{"a later revision carried elsewhere", []string{"UPDATE proposal_revisions SET carrier_id = 'elsewhere' WHERE proposal_id = 'p1' AND revision_id = 'r2'"}, proposal("p1"), true, "proposal revision r2"},
		{"a proposal carried elsewhere than its revisions", []string{"UPDATE proposals SET carrier_id = 'elsewhere' WHERE proposal_id = 'p1'"}, proposal("p1"), true, "carried elsewhere"},
		{"a proposal without revisions", []string{"INSERT INTO proposals (project_id, suite_id, proposal_id, carrier_id) VALUES ('project', 'suite', 'bare', 'carrier-bare')"}, proposal("bare"), true, "revisions are missing"},

		// Proposal: consent history.
		{"a consent table that cannot be read", []string{renameTable("consent_results")}, proposal("p1"), false, "load consent results"},
		{"a consent result by an actor of an unknown kind", []string{dropConstraint("consent_results", "consent_results_actor_kind_check"), "UPDATE consent_results SET actor_kind = 'robot'"}, proposal("p1"), true, "consent result comment-p1"},
		{"a consent result with an unknown action", []string{dropConstraint("consent_results", "consent_results_action_check"), "UPDATE consent_results SET action = 'delete'"}, proposal("p1"), true, "consent result comment-p1"},
		{"a consent result with order zero", []string{dropConstraint("consent_results", "consent_results_command_order_check"), "UPDATE consent_results SET command_order = 0"}, proposal("p1"), true, "consent result comment-p1"},
		{"a consent result with an unknown outcome", []string{dropConstraint("consent_results", "consent_results_outcome_check"), "UPDATE consent_results SET outcome = 'maybe'"}, proposal("p1"), true, "consent result comment-p1"},
		{"a consent result for a revision that does not exist", []string{"UPDATE consent_results SET revision_id = 'ghost'"}, proposal("p1"), true, "consent history"},
		{"an operation table that cannot be read for aliases", []string{renameTable("operations")}, proposal("p1"), false, "load consent aliases"},

		// Assessments.
		{"an assessment table that cannot be read", []string{renameTable("assessments")}, assessment(p1Reference("r2"), "merged-p1"), false, "load assessment"},
		{"an assessment of a revision that does not exist", []string{dropConstraint("assessments", "assessments_revision_fkey"), dropConstraint("assessments", "assessments_evidence_revision_fkey"),
			"UPDATE assessments SET revision_id = 'ghost', evidence_revision_id = 'ghost' WHERE proposal_id = 'p1' AND revision_id = 'r2' AND source = 'merged-p1'"},
			assessment(p1Reference("ghost"), "merged-p1"), false, `load proposal revision "ghost"`},
		{"an assessment of a revision whose binding cannot be rebuilt", []string{brokenCovered + "'p1' AND revision_id = 'r2'"}, assessment(p1Reference("r2"), "merged-p1"), true, "proposal revision r2"},
		{"evidence citing a revision that does not exist", []string{dropConstraint("assessments", "assessments_evidence_revision_fkey"),
			"UPDATE assessments SET evidence_revision_id = 'ghost' WHERE proposal_id = 'p1' AND revision_id = 'r2' AND source = 'merged-p1'"},
			assessment(p1Reference("r2"), "merged-p1"), false, `load proposal revision "ghost"`},
		{"evidence with an unknown outcome", []string{dropConstraint("assessments", "assessments_outcome_check"), "UPDATE assessments SET outcome = 'maybe' WHERE source = 'merged-p1'"},
			assessment(p1Reference("r2"), "merged-p1"), true, "assessment"},
		{"evidence without an emitter", []string{dropConstraint("assessments", "assessments_evidence_check"), "UPDATE assessments SET evidence_emitter = '' WHERE source = 'merged-p1'"},
			assessment(p1Reference("r2"), "merged-p1"), true, "assessment evidence"},
		{"an assessment of a source that is only spaces", []string{dropConstraint("assessments", "assessments_source_check"),
			"UPDATE assessments SET source = ' ' WHERE proposal_id = 'p1' AND revision_id = 'r2' AND source = 'merged-p1'"}, assessment(p1Reference("r2"), " "), true, "assessment"},

		// Versions and promotions.
		{"a promotion table that cannot be read for a version", []string{renameTable("promotions")}, version("v0"), false, "load version"},
		{"a version that is only spaces", []string{dropConstraint("promotions", "promotions_version_fkey"), dropConstraint("promotions", "promotions_corrects_fkey"), dropConstraint("suites", "suites_current_version_fkey"),
			dropConstraint("suite_versions", "suite_versions_version_id_check"), "UPDATE suite_versions SET version_id = ' '", "UPDATE promotions SET version_id = ' '"}, version(" "), true, "version"},
		{"a version whose promoted binding cannot be rebuilt", []string{brokenCovered + "'p0'"}, version("v0"), true, "binding of version v0"},
		{"a version whose promoted manifest differs from its own", []string{"UPDATE proposal_revisions SET manifest_digest = '" + badDigest + "' WHERE proposal_id = 'p0'"}, version("v0"), true, "version v0"},
		{"a promotion that corrects nothing but expects its own version", []string{"UPDATE proposal_revisions SET expected_version_id = 'v0' WHERE proposal_id = 'p0'"}, version("v0"), true, "promotion of version v0"},
		{"a promotion table that cannot be read for a reference", []string{renameTable("promotions")}, promotionFor, false, "load promotion"},
		{"a promotion whose binding cannot be rebuilt", []string{brokenCovered + "'p0'"}, promotionFor, true, "binding of version v0"},
		{"a promotion that expects its own version", []string{"UPDATE proposal_revisions SET expected_version_id = 'v0' WHERE proposal_id = 'p0'"}, promotionFor, true, "promotion of version v0"},

		// Operations.
		{"an operation table that cannot be read by id", []string{renameTable("operations")}, receipt, false, "load operation"},
		{"an operation table that cannot be read by source command", []string{renameTable("operations")}, receiptBySource, false, "load operation of source command"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			w, _ := damagedWorld(t)
			w.tamper(tt.sql...)
			err := w.store.Do(t.Context(), fxProject, fxSuite, tt.call)
			if err == nil || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("read = %v; want an error mentioning %q", err, tt.contains)
			}
			if got := errors.Is(err, governance.ErrInvalidState); got != tt.invalid {
				t.Fatalf("read = %v; ErrInvalidState = %v, want %v", err, got, tt.invalid)
			}
			if errors.Is(err, governance.ErrNotFound) {
				t.Fatalf("read = %v; damaged state must not look like a missing record", err)
			}
		})
	}
}

func TestPromotionForIgnoresOtherSuites(t *testing.T) {
	w, _ := damagedWorld(t)
	w.read(func(ctx context.Context, tx governance.Tx) error {
		other := contract.ProposalReference{ProjectID: fxProject, SuiteID: "suite-b", ProposalID: "p0", RevisionID: "revision-1"}
		if record, found, err := tx.PromotionFor(ctx, other); err != nil || found || !record.IsZero() {
			t.Fatalf("PromotionFor a reference of another Suite = %+v found=%v err=%v; want nothing", record, found, err)
		}
		return nil
	})
}
