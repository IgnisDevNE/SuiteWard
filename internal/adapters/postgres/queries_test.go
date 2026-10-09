//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
)

func text(value string) pgtype.Text { return pgtype.Text{String: value, Valid: true} }

// seedWithQueries writes a complete history through the generated inserts only.
func seedWithQueries(t *testing.T, queries *dbgen.Queries) {
	t.Helper()
	ctx := t.Context()
	recorded := time.Date(2026, 10, 2, 12, 0, 0, 123000000, time.UTC)
	steps := []func() error{
		func() error {
			return queries.InsertPolicy(ctx, dbgen.InsertPolicyParams{ProjectID: "project", RevisionID: "policy-1", OwnerID: "owner", OwnerKind: "human"})
		},
		func() error {
			return queries.InsertSuite(ctx, dbgen.InsertSuiteParams{ProjectID: "project", SuiteID: "suite", Revision: 4, CurrentVersionID: text("v1"), TargetID: "main", PolicyRevisionID: "policy-1"})
		},
		func() error {
			return queries.InsertSuiteVersion(ctx, dbgen.InsertSuiteVersionParams{ProjectID: "project", SuiteID: "suite", VersionID: "v1", ManifestDigest: schemaDigest, Manifest: []byte(`{"entries":[{"path":"a"}]}`)})
		},
		func() error {
			return queries.InsertProposal(ctx, dbgen.InsertProposalParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", CarrierID: "pr-1"})
		},
		func() error {
			return queries.InsertProposalRevision(ctx, dbgen.InsertProposalRevisionParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r2", Seq: 2, Origin: "origin-2", CarrierID: "pr-1", ManifestDigest: schemaDigest, ScopeDigest: schemaDigest, CoveredInputs: []byte(`{"k":"v"}`), PolicyRevisionID: "policy-1"})
		},
		func() error {
			return queries.InsertProposalRevision(ctx, dbgen.InsertProposalRevisionParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r1", Seq: 1, Origin: "origin-1", CarrierID: "pr-1", ManifestDigest: schemaDigest, ScopeDigest: schemaDigest, CoveredInputs: []byte(`{}`), ExpectedVersionID: text("v0"), PolicyRevisionID: "policy-1"})
		},
		func() error {
			return queries.InsertAssessment(ctx, dbgen.InsertAssessmentParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r1", Source: "sha-1", EvidenceEmitter: text("ci"), EvidenceSource: text("sha-1"), EvidenceRevisionID: text("r1"), Outcome: text("passed")})
		},
		func() error {
			return queries.InsertAssessment(ctx, dbgen.InsertAssessmentParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r2", Source: "sha-2"})
		},
		func() error {
			return queries.InsertConsentResult(ctx, dbgen.InsertConsentResultParams{SourceCommandID: "src-b", OperationID: "op-b", ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r2", ActorID: "owner", ActorKind: "human", CarrierID: "pr-1", Action: "revoke", CommandOrder: 2, Outcome: "no_active_approval", Reason: "none"})
		},
		func() error {
			return queries.InsertConsentResult(ctx, dbgen.InsertConsentResultParams{SourceCommandID: "src-a", OperationID: "op-a", ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r2", ActorID: "owner", ActorKind: "human", CarrierID: "pr-1", Action: "approve", CommandOrder: 1, Outcome: "rejected", Reason: "policy_mismatch"})
		},
		func() error {
			return queries.InsertOperation(ctx, dbgen.InsertOperationParams{OperationID: "op-b", ProjectID: "project", SuiteID: "suite", Kind: "consent", SourceCommandID: text("src-b"), Receipt: []byte(`{"kind":"consent","n":"b"}`)})
		},
		func() error {
			return queries.InsertOperation(ctx, dbgen.InsertOperationParams{OperationID: "op-a", ProjectID: "project", SuiteID: "suite", Kind: "consent", SourceCommandID: text("src-a"), Receipt: []byte(`{"kind":"consent","n":"a"}`)})
		},
		func() error {
			return queries.InsertOperation(ctx, dbgen.InsertOperationParams{OperationID: "op-alias", ProjectID: "project", SuiteID: "suite", Kind: "consent", SourceCommandID: text("src-a"), Receipt: []byte(`{"kind":"consent","n":"a"}`)})
		},
		func() error {
			return queries.InsertPromotion(ctx, dbgen.InsertPromotionParams{OperationID: "op-promote", ProjectID: "project", SuiteID: "suite", VersionID: "v1", ProposalID: "p1", RevisionID: "r1", CarrierID: "pr-1", SourceRevision: "sha-1", TargetID: "main", RecordedAt: pgtype.Timestamptz{Time: recorded, Valid: true}})
		},
		func() error {
			return queries.InsertOperation(ctx, dbgen.InsertOperationParams{OperationID: "op-promote", ProjectID: "project", SuiteID: "suite", Kind: "promote", Receipt: []byte(`{"kind":"promote"}`)})
		},
	}
	for i, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("seed step %d: %v", i, err)
		}
	}
}

func TestGovernanceGeneratedQueries(t *testing.T) {
	database := newSchemaDatabase(t)
	if err := migrations.Up(t.Context(), database.url); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	transaction, err := database.conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback(context.Background()) }() // cleanup path: a failed rollback only means the connection is gone
	queries := dbgen.New(transaction)
	// The seeded Suite points at v1 and the revision v0 has no row: the deferred
	// pointer is satisfied at commit and expected_version_id has no foreign key.
	seedWithQueries(t, queries)
	if err := transaction.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	queries = dbgen.New(database.conn)

	t.Run("lock suite", func(t *testing.T) {
		locking, err := database.conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = locking.Rollback(context.Background()) }() // cleanup path: a failed rollback only means the connection is gone
		suite, err := dbgen.New(locking).LockSuite(ctx, dbgen.LockSuiteParams{ProjectID: "project", SuiteID: "suite"})
		if err != nil || suite.Revision != 4 || suite.CurrentVersionID.String != "v1" || suite.TargetID != "main" || suite.PolicyRevisionID != "policy-1" {
			t.Fatalf("locked suite %+v, error %v", suite, err)
		}
		other, err := pgx.Connect(ctx, database.url)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = other.Close(context.Background()) }() // cleanup path: a failed close only means the connection is gone
		deadline, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		if _, err := dbgen.New(other).LockSuite(deadline, dbgen.LockSuiteParams{ProjectID: "project", SuiteID: "suite"}); err == nil {
			t.Fatal("a second session acquired a lock that the first still holds")
		}
		if _, err := dbgen.New(locking).LockSuite(ctx, dbgen.LockSuiteParams{ProjectID: "project", SuiteID: "missing"}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("unknown suite error = %v; want no rows", err)
		}
	})
	t.Run("policy", func(t *testing.T) {
		policy, err := queries.GetPolicy(ctx, dbgen.GetPolicyParams{ProjectID: "project", RevisionID: "policy-1"})
		if err != nil || policy.OwnerID != "owner" || policy.OwnerKind != "human" {
			t.Fatalf("policy %+v, error %v", policy, err)
		}
	})
	t.Run("proposal and revisions in sequence order", func(t *testing.T) {
		proposal, err := queries.GetProposal(ctx, dbgen.GetProposalParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1"})
		if err != nil || proposal.CarrierID != "pr-1" {
			t.Fatalf("proposal %+v, error %v", proposal, err)
		}
		revisions, err := queries.ListProposalRevisions(ctx, dbgen.ListProposalRevisionsParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1"})
		if err != nil || len(revisions) != 2 || revisions[0].RevisionID != "r1" || revisions[1].RevisionID != "r2" {
			t.Fatalf("revisions %+v, error %v; want r1 then r2 although r2 was inserted first", revisions, err)
		}
		if revisions[0].ExpectedVersionID.String != "v0" || revisions[1].ExpectedVersionID.Valid || string(revisions[1].CoveredInputs) != `{"k": "v"}` {
			t.Fatalf("revision payloads %+v", revisions)
		}
	})
	t.Run("consent results in insertion order with aliases", func(t *testing.T) {
		results, err := queries.ListConsentResults(ctx, dbgen.ListConsentResultsParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1"})
		if err != nil || len(results) != 2 || results[0].SourceCommandID != "src-b" || results[1].SourceCommandID != "src-a" {
			t.Fatalf("results %+v, error %v; want insertion order src-b then src-a", results, err)
		}
		aliases, err := queries.ListConsentAliases(ctx, dbgen.ListConsentAliasesParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1"})
		if err != nil || len(aliases) != 1 || aliases[0].OperationID != "op-alias" || aliases[0].SourceCommandID != "src-a" {
			t.Fatalf("aliases %+v, error %v; only the replay operation is an alias", aliases, err)
		}
	})
	t.Run("assessment by key", func(t *testing.T) {
		assessment, err := queries.GetAssessment(ctx, dbgen.GetAssessmentParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r1", Source: "sha-1"})
		if err != nil || assessment.Outcome.String != "passed" || assessment.EvidenceEmitter.String != "ci" || assessment.EvidenceRevisionID.String != "r1" {
			t.Fatalf("assessment %+v, error %v", assessment, err)
		}
		missing, err := queries.GetAssessment(ctx, dbgen.GetAssessmentParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r2", Source: "sha-2"})
		if err != nil || missing.Outcome.Valid || missing.EvidenceEmitter.Valid || missing.EvidenceRevisionID.Valid {
			t.Fatalf("missing evidence %+v, error %v", missing, err)
		}
		if _, err := queries.GetAssessment(ctx, dbgen.GetAssessmentParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r1", Source: "other"}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("unknown assessment error = %v; want no rows", err)
		}
	})
	t.Run("version with its promotion and binding", func(t *testing.T) {
		row, err := queries.GetVersion(ctx, dbgen.GetVersionParams{ProjectID: "project", SuiteID: "suite", VersionID: "v1"})
		if err != nil || row.SuiteVersion.ManifestDigest != schemaDigest || row.Promotion.OperationID != "op-promote" || row.ProposalRevision.RevisionID != "r1" || row.ProposalRevision.Origin != "origin-1" {
			t.Fatalf("version %+v, error %v", row, err)
		}
		if !row.Promotion.RecordedAt.Time.Equal(time.Date(2026, 10, 2, 12, 0, 0, 123000000, time.UTC)) || row.Promotion.CorrectsVersionID.Valid {
			t.Fatalf("promotion %+v", row.Promotion)
		}
		if _, err := queries.GetVersion(ctx, dbgen.GetVersionParams{ProjectID: "project", SuiteID: "suite", VersionID: "v9"}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("unknown version error = %v; want no rows", err)
		}
	})
	t.Run("promotion by reference", func(t *testing.T) {
		row, err := queries.GetPromotionByReference(ctx, dbgen.GetPromotionByReferenceParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r1"})
		if err != nil || row.Promotion.VersionID != "v1" || row.ProposalRevision.ManifestDigest != schemaDigest {
			t.Fatalf("promotion %+v, error %v", row, err)
		}
		if _, err := queries.GetPromotionByReference(ctx, dbgen.GetPromotionByReferenceParams{ProjectID: "project", SuiteID: "suite", ProposalID: "p1", RevisionID: "r2"}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("unpromoted reference error = %v; want no rows", err)
		}
	})
	t.Run("operations by id and by source command", func(t *testing.T) {
		operation, err := queries.GetOperation(ctx, "op-alias")
		if err != nil || operation.Kind != "consent" || operation.SourceCommandID.String != "src-a" {
			t.Fatalf("operation %+v, error %v", operation, err)
		}
		original, err := queries.GetOperationBySource(ctx, "src-a")
		if err != nil || original.OperationID != "op-a" {
			t.Fatalf("by source %+v, error %v; want the original operation, not the alias", original, err)
		}
		if _, err := queries.GetOperation(ctx, "missing"); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("unknown operation error = %v; want no rows", err)
		}
	})
	t.Run("bump revision", func(t *testing.T) {
		revision, err := queries.BumpSuiteRevision(ctx, dbgen.BumpSuiteRevisionParams{ProjectID: "project", SuiteID: "suite"})
		if err != nil || revision != 5 {
			t.Fatalf("plain bump revision=%d error=%v", revision, err)
		}
		if err := queries.InsertSuiteVersion(ctx, dbgen.InsertSuiteVersionParams{ProjectID: "project", SuiteID: "suite", VersionID: "v2", ManifestDigest: schemaDigest, Manifest: []byte(`{}`)}); err != nil {
			t.Fatal(err)
		}
		revision, err = queries.BumpSuiteRevision(ctx, dbgen.BumpSuiteRevisionParams{ProjectID: "project", SuiteID: "suite", CurrentVersionID: text("v2")})
		if err != nil || revision != 6 {
			t.Fatalf("promoting bump revision=%d error=%v", revision, err)
		}
		suite, err := dbgen.New(database.conn).LockSuite(ctx, dbgen.LockSuiteParams{ProjectID: "project", SuiteID: "suite"})
		if err != nil || suite.CurrentVersionID.String != "v2" || suite.Revision != 6 || suite.TargetID != "main" {
			t.Fatalf("bumped suite %+v, error %v", suite, err)
		}
	})
	t.Run("schema version", func(t *testing.T) {
		version, err := queries.GetSchemaVersion(ctx)
		if err != nil || version != migrations.SupportedVersion {
			t.Fatalf("schema version %d, error %v; want %d", version, err, migrations.SupportedVersion)
		}
	})
}
