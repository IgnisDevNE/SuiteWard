//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/internal/dbgen"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres/migrations"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

type transactionFixture struct {
	authority TrustedAuthority
	command   contract.Command
	request   governance.PromoteRequest
}

type transactionVerifier func(context.Context, artifact.Digest) error

func (verify transactionVerifier) Verify(ctx context.Context, digest artifact.Digest) error {
	if verify != nil {
		return verify(ctx, digest)
	}
	return ctx.Err()
}

func transactionDatabase(t *testing.T) (*pgxpool.Pool, *Store, transactionFixture) {
	t.Helper()
	connectionURL := os.Getenv("SUITEWARD_TEST_DATABASE_URL")
	if connectionURL == "" {
		t.Fatal("SUITEWARD_TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	admin, err := pgx.Connect(t.Context(), connectionURL)
	if err != nil {
		t.Fatalf("connect owned PostgreSQL fixture: %v", err)
	}
	schema := "sw_transaction_" + strings.ToLower(rand.Text())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(t.Context(), "CREATE SCHEMA "+identifier); err != nil {
		admin.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Errorf("remove owned schema: %v", err)
		}
		admin.Close(context.Background())
	})
	parsed, err := url.Parse(connectionURL)
	if err != nil {
		t.Fatal("fixture connection must be a PostgreSQL URL")
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	if err := migrations.Up(t.Context(), parsed.String()); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := codecValue(NewStore(pool, transactionVerifier(nil)))
	owner := codecValue(contract.NewPrincipal("owner", contract.Human))
	policy := codecValue(contract.NewPolicy("project", "policy", owner))
	manifest := codecValue(artifact.NewManifest([]artifact.Entry{{Path: "tests/validation.txt", Content: artifact.Hash([]byte("validation protected content"))}}))
	protected := codecValue(contract.NewProtectedContract(manifest, artifact.Hash([]byte("scope")), map[string]string{"runner": "v1"}))
	reference := contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "proposal", RevisionID: "r1"}
	binding := codecValue(contract.NewApprovalBinding(contract.BindingInput{Reference: reference, Manifest: manifest.Digest(), Scope: protected.ScopeDigest(), PolicyRevision: "policy", CoveredInputs: protected.CoveredInputs()}))
	proposal := codecValue(contract.NewProposal(codecValue(contract.NewProposalRevision(binding, "origin", "carrier"))))
	consent := codecValue(contract.NewConsent("project", "suite", "proposal"))
	schedule := codecValue(contract.NewSchedule("project", "suite"))
	schedule = codecValue(schedule.Admit(proposal, true))
	canonical := codecValue(contract.NewCanonicalSnapshot(codecValue(contract.NewSuite("project", "suite", "", 0)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	evidence := codecValue(contract.NewIntegrityEvidence("checker", "integrated", binding, contract.IntegrityPassed))
	assessment := codecValue(contract.AssessIntegrity("integrated", binding, &evidence))
	command := codecValue(contract.NewCommand(contract.CommandInput{OperationID: "approve", SourceCommandID: "source-approve", Actor: owner, Reference: reference, Carrier: "carrier", Action: contract.ApproveConsent, Order: 1}))
	request := governance.PromoteRequest{OperationID: "promote", Reference: reference, Carrier: "carrier", Proposed: protected, AssessmentSource: "integrated", Integration: codecValue(contract.NewIntegration("project", "main", "integrated", "carrier", contract.IntegrationMergedChange)), NewVersionID: "v1", RecordedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)}
	fixture := transactionFixture{TrustedAuthority{Canonical: canonical, Policy: policy, Scheduling: schedule, Target: "main", Proposals: []TrustedProposal{{Proposal: proposal, Consent: consent, Assessments: []contract.IntegrityAssessment{assessment}}}}, command, request}
	if err := store.InitializeTrusted(t.Context(), fixture.authority); err != nil {
		t.Fatal(err)
	}
	return pool, store, fixture
}

func transactionExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("owned fixture statement: %v", err)
	}
}

func transactionApprove(t *testing.T, store *Store, fixture transactionFixture) governance.ConsentReceipt {
	t.Helper()
	response, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: fixture.command})
	if err != nil || !response.Committed {
		t.Fatalf("approve fixture: %+v, %v", response, err)
	}
	return response.Receipt
}

var errTransactionCaptured = errors.New("captured proposed write without committing")

type transactionCapture struct {
	*Store
	fence     governance.AuthorityFence
	promotion governance.PromotionWrite
	consent   governance.ConsentWrite
}

func (capture *transactionCapture) CommitPromotion(_ context.Context, fence governance.AuthorityFence, write governance.PromotionWrite) error {
	capture.fence, capture.promotion = fence, write
	return errTransactionCaptured
}

func (capture *transactionCapture) CommitConsent(_ context.Context, fence governance.AuthorityFence, write governance.ConsentWrite) error {
	capture.fence, capture.consent = fence, write
	return errTransactionCaptured
}

func transactionPreparePromotion(t *testing.T, store *Store, fixture transactionFixture) *transactionCapture {
	t.Helper()
	capture := &transactionCapture{Store: store}
	if _, err := governance.Promote(t.Context(), capture, fixture.request); !errors.Is(err, errTransactionCaptured) {
		t.Fatalf("capture valid proposed promotion: %v", err)
	}
	return capture
}

func transactionPrepareConsent(t *testing.T, store *Store, command contract.Command) *transactionCapture {
	t.Helper()
	capture := &transactionCapture{Store: store}
	if _, err := governance.ProcessConsent(t.Context(), capture, governance.ConsentRequest{Command: command}); !errors.Is(err, errTransactionCaptured) {
		t.Fatalf("capture valid proposed consent: %v", err)
	}
	return capture
}

func TestTransactionReadsRequireAuthorityOrPreserveOnlyGlobalIdentity(t *testing.T) {
	_, store, fixture := transactionDatabase(t)
	for _, reference := range []contract.ProposalReference{{SuiteID: "suite"}, {ProjectID: "project"}} {
		if _, err := store.Load(t.Context(), governance.ReadRequest{Reference: reference}); !errors.Is(err, governance.ErrInvalidRequest) {
			t.Fatalf("incomplete authority scope accepted: %v", err)
		}
	}
	for _, reference := range []contract.ProposalReference{
		{ProjectID: "missing", SuiteID: "missing", ProposalID: "missing"},
		{ProjectID: "project", SuiteID: "suite", ProposalID: "missing"},
	} {
		if _, err := store.Load(t.Context(), governance.ReadRequest{Reference: reference}); !errors.Is(err, governance.ErrNotFound) {
			t.Fatalf("missing authority or proposal yielded success: %v", err)
		}
	}
	want := transactionApprove(t, store, fixture)
	for _, reference := range []contract.ProposalReference{
		{ProjectID: "foreign", SuiteID: "foreign", ProposalID: "foreign"},
		{ProjectID: "project", SuiteID: "suite", ProposalID: "missing"},
	} {
		got, err := store.Load(t.Context(), governance.ReadRequest{Reference: reference, SourceCommandID: "source-approve"})
		if err != nil || got.Source.Consent != want || !got.Canonical.IsZero() || !got.Proposal.IsZero() || got.Fence != (governance.AuthorityFence{}) {
			t.Fatalf("global source identity became foreign current authority: %+v, %v", got, err)
		}
	}
	got, err := store.Load(t.Context(), governance.ReadRequest{OperationID: "approve", SourceCommandID: "source-approve"})
	if err != nil || got.Operation.Consent != want || got.Source.Consent != want || !got.Canonical.IsZero() {
		t.Fatalf("exact operation replay depended on current authority scope: %+v, %v", got, err)
	}
	foreign := codecValue(contract.NewCommand(contract.CommandInput{OperationID: "foreign-alias", SourceCommandID: "source-approve", Actor: fixture.command.Actor(), Reference: contract.ProposalReference{ProjectID: "foreign", SuiteID: "foreign", ProposalID: "foreign", RevisionID: "r1"}, Carrier: "carrier", Action: contract.ApproveConsent, Order: 1}))
	if _, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: foreign}); !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("global source reservation reused in another scope: %v", err)
	}
}

func TestTransactionReadQueryErrorsPreservePostgresCause(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		table string
		read  governance.ReadRequest
	}{
		{"operation receipt", "operation_receipts", governance.ReadRequest{OperationID: "lookup"}},
		{"source receipt", "consent_sources", governance.ReadRequest{SourceCommandID: "lookup"}},
		{"authority", "suites", governance.ReadRequest{Reference: contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "proposal"}}},
		{"promotion history", "promotions", governance.ReadRequest{Reference: contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "proposal"}}},
		{"historical version", "suite_versions", governance.ReadRequest{Reference: contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "proposal"}, HistoricalVersionID: "lookup"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pool, store, _ := transactionDatabase(t)
			transactionExec(t, pool, "ALTER TABLE "+pgx.Identifier{scenario.table}.Sanitize()+" RENAME TO unavailable")
			got, err := store.Load(t.Context(), scenario.read)
			var postgresError *pgconn.PgError
			if !errors.As(err, &postgresError) || postgresError.Code != "42P01" || !got.Canonical.IsZero() {
				t.Fatalf("failed persisted read lost PostgreSQL cause or yielded authority: %+v, %v", got, err)
			}
		})
	}
}

func TestTransactionFencesAndMissingProposalCannotAuthorizeWrites(t *testing.T) {
	pool, store, fixture := transactionDatabase(t)
	write := transactionPrepareConsent(t, store, fixture.command)
	for _, fence := range []governance.AuthorityFence{
		{ProjectID: "missing", SuiteID: "missing"},
		{ProjectID: "project", SuiteID: "suite", Revision: 1},
	} {
		want := governance.ErrAuthorityConflict
		if fence.ProjectID == "missing" {
			want = governance.ErrNotFound
		}
		if err := store.CommitConsent(t.Context(), fence, write.consent); !errors.Is(err, want) {
			t.Fatalf("missing or stale fence authorized consent: %v", err)
		}
	}
	unknown := codecValue(contract.NewCommand(contract.CommandInput{OperationID: "unknown", SourceCommandID: "unknown-source", Actor: fixture.command.Actor(), Reference: contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "missing", RevisionID: "r1"}, Carrier: "carrier", Action: contract.ApproveConsent, Order: 1}))
	if err := store.CommitConsent(t.Context(), write.fence, governance.ConsentWrite{Command: unknown}); !errors.Is(err, governance.ErrNotFound) {
		t.Fatalf("missing scoped proposal committed: %v", err)
	}
	foreign := codecValue(contract.NewCommand(contract.CommandInput{OperationID: "foreign", SourceCommandID: "foreign-source", Actor: fixture.command.Actor(), Reference: contract.ProposalReference{ProjectID: "foreign", SuiteID: "suite", ProposalID: "proposal", RevisionID: "r1"}, Carrier: "carrier", Action: contract.ApproveConsent, Order: 1}))
	if err := store.CommitConsent(t.Context(), write.fence, governance.ConsentWrite{Command: foreign}); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("authority fence authorized a command in another project: %v", err)
	}
	transactionExec(t, pool, `UPDATE suites SET authority_revision=authority_revision+1, governance_payload='{}'`)
	if _, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference}); !errors.Is(err, governance.ErrInvalidSnapshot) {
		t.Fatalf("malformed stored authority became usable: %v", err)
	}
	if err := store.CommitConsent(t.Context(), governance.AuthorityFence{ProjectID: "project", SuiteID: "suite", Revision: 1}, write.consent); !errors.Is(err, governance.ErrInvalidSnapshot) {
		t.Fatalf("malformed locked authority authorized consent: %v", err)
	}
}

func TestTransactionSourceIndexMustMatchOriginalSourceIdentity(t *testing.T) {
	pool, store, fixture := transactionDatabase(t)
	transactionApprove(t, store, fixture)
	transactionExec(t, pool, `INSERT INTO consent_sources(source_command_id,project_id,suite_id,operation_id) VALUES ('copied-source','project','suite','approve')`)
	if _, err := store.Load(t.Context(), governance.ReadRequest{SourceCommandID: "copied-source"}); !errors.Is(err, governance.ErrInvalidSnapshot) {
		t.Fatalf("copied index authorized a different source identity: %v", err)
	}
}

func TestTransactionCorruptHistoryPreventsCurrentAndExactReplaySuccess(t *testing.T) {
	for _, scenario := range []struct {
		name string
		sql  string
	}{
		{"malformed version", `ALTER TABLE suite_versions DISABLE TRIGGER USER; UPDATE suite_versions SET version_payload='{}'`},
		{"version digest mismatch", `ALTER TABLE suite_versions DISABLE TRIGGER USER; UPDATE suite_versions SET manifest_digest='sha256:0000000000000000000000000000000000000000000000000000000000000000'`},
		{"malformed promotion", `ALTER TABLE promotions DISABLE TRIGGER USER; UPDATE promotions SET promotion_payload='{}'`},
		{"promotion foreign reference", `ALTER TABLE promotions DISABLE TRIGGER USER; UPDATE promotions SET proposal_revision_id='foreign-revision'`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pool, store, fixture := transactionDatabase(t)
			transactionApprove(t, store, fixture)
			if response, err := governance.Promote(t.Context(), store, fixture.request); err != nil || !response.Committed {
				t.Fatalf("promotion fixture: %+v, %v", response, err)
			}
			transactionExec(t, pool, scenario.sql)
			request := governance.ReadRequest{Reference: fixture.request.Reference}
			if scenario.name == "promotion foreign reference" {
				request.Reference.RevisionID = "foreign-revision"
			}
			if _, err := store.Load(t.Context(), request); !errors.Is(err, governance.ErrInvalidSnapshot) {
				t.Fatalf("inconsistent stored history yielded a usable current snapshot: %v", err)
			}
			if scenario.name != "malformed promotion" && scenario.name != "promotion foreign reference" {
				if _, err := store.Load(t.Context(), governance.ReadRequest{OperationID: "promote"}); !errors.Is(err, governance.ErrInvalidSnapshot) {
					t.Fatalf("exact promotion replay ignored inconsistent canonical history: %v", err)
				}
			}
		})
	}
}

func TestTransactionPostPromotionConsentReplaysBoundCanonicalHistory(t *testing.T) {
	for _, historyState := range []string{"valid", "missing referenced version", "unreadable referenced version"} {
		t.Run(historyState, func(t *testing.T) {
			pool, store, fixture := transactionDatabase(t)
			transactionApprove(t, store, fixture)
			if response, err := governance.Promote(t.Context(), store, fixture.request); err != nil || !response.Committed {
				t.Fatalf("promotion fixture: %+v, %v", response, err)
			}
			revoke := codecValue(contract.NewCommand(contract.CommandInput{OperationID: "revoke", SourceCommandID: "source-revoke", Actor: fixture.command.Actor(), Reference: fixture.request.Reference, Carrier: "carrier", Action: contract.RevokeConsent, Order: 2}))
			response, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: revoke})
			if err != nil || !response.Committed || response.Receipt.PromotedVersionID != "v1" {
				t.Fatalf("postpromotion consent lost its canonical history binding: %+v, %v", response, err)
			}
			if historyState == "missing referenced version" {
				transactionExec(t, pool, `ALTER TABLE operation_receipts DISABLE TRIGGER USER; UPDATE operation_receipts SET receipt_payload=jsonb_set(receipt_payload,'{Consent,PromotedVersion}','"absent"') WHERE operation_id='revoke'`)
			}
			if historyState == "unreadable referenced version" {
				transactionExec(t, pool, `ALTER TABLE suite_versions RENAME TO unavailable`)
			}
			got, err := store.Load(t.Context(), governance.ReadRequest{OperationID: "revoke", SourceCommandID: "source-revoke"})
			if historyState == "unreadable referenced version" {
				var postgresError *pgconn.PgError
				if !errors.As(err, &postgresError) || postgresError.Code != "42P01" {
					t.Fatalf("consent replay lost referenced version read cause: %v", err)
				}
			} else if historyState == "missing referenced version" {
				if !errors.Is(err, governance.ErrInvalidSnapshot) {
					t.Fatalf("consent replay ignored its missing successful version: %v", err)
				}
			} else if err != nil || got.Operation.Consent != response.Receipt || got.Source.Consent != response.Receipt {
				t.Fatalf("original postpromotion receipt changed on replay: %+v, %v", got, err)
			}
		})
	}
}

func TestTransactionCanceledContextCannotPublishAuthority(t *testing.T) {
	pool, store, fixture := transactionDatabase(t)
	consent := transactionPrepareConsent(t, store, fixture.command)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for name, call := range map[string]func() error{
		"initialize": func() error { return store.InitializeTrusted(ctx, fixture.authority) },
		"load": func() error {
			_, err := store.Load(ctx, governance.ReadRequest{Reference: fixture.request.Reference})
			return err
		},
		"consent": func() error { return store.CommitConsent(ctx, consent.fence, consent.consent) },
		"promotion": func() error {
			return store.CommitPromotion(ctx, consent.fence, governance.PromotionWrite{Receipt: governance.PromotionReceipt{Identity: governance.PromotionIdentity{Request: fixture.request}}})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled call lost cancellation: %v", err)
			}
		})
	}
	transactionApprove(t, store, fixture)
	promotion := transactionPreparePromotion(t, store, fixture)
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	store.verifier = transactionVerifier(func(context.Context, artifact.Digest) error { cancel(); return ctx.Err() })
	if err := store.CommitPromotion(ctx, promotion.fence, promotion.promotion); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during content validation lost cause: %v", err)
	}
	var revision string
	var versions int
	if err := pool.QueryRow(t.Context(), `SELECT authority_revision::text FROM suites`).Scan(&revision); err != nil || revision != "1" {
		t.Fatalf("canceled promotion advanced authority: %q, %v", revision, err)
	}
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM suite_versions`).Scan(&versions); err != nil || versions != 0 {
		t.Fatalf("canceled promotion created canonical history: %d, %v", versions, err)
	}
}

func TestTransactionCASRejectsChangedPointerWithoutReplacingAuthority(t *testing.T) {
	pool, store, fixture := transactionDatabase(t)
	fence := governance.AuthorityFence{ProjectID: "project", SuiteID: "suite"}
	tx, queries, state, err := store.lockAuthority(t.Context(), fence)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	state.canonical = codecValue(contract.NewCanonicalSnapshot(codecValue(contract.NewSuite("project", "suite", "", 1)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	broken := state
	broken.consents = map[contract.ProposalID]contract.Consent{"proposal": codecValue(contract.NewConsent("project", "suite", "foreign-proposal"))}
	if err := casAuthority(t.Context(), queries, fence, "", broken); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("CAS serialized inconsistent paired proposal/consent facts: %v", err)
	}
	if err := casAuthority(t.Context(), queries, fence, "foreign-pointer", state); !errors.Is(err, governance.ErrAuthorityConflict) {
		t.Fatalf("CAS ignored changed current pointer: %v", err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference})
	if err != nil || !reflect.DeepEqual(got.Canonical, fixture.authority.Canonical) {
		t.Fatalf("failed CAS replaced canonical authority: %+v, %v", got.Canonical, err)
	}
	if _, _, err := store.loadVersion(t.Context(), dbgen.New(pool), "project", "suite", "absent"); err != nil {
		t.Fatalf("missing optional history became storage failure: %v", err)
	}
}

func TestTransactionWriterReadFailuresNeverAdvanceAuthority(t *testing.T) {
	for _, kind := range []string{"consent", "promotion"} {
		tables := []string{"operation_receipts", "consent_sources", "promotions"}
		if kind == "promotion" {
			tables = []string{"operation_receipts", "suite_versions", "promotions"}
		}
		for _, table := range tables {
			t.Run(kind+"/"+table, func(t *testing.T) {
				pool, store, fixture := transactionDatabase(t)
				var capture *transactionCapture
				if kind == "promotion" {
					transactionApprove(t, store, fixture)
					capture = transactionPreparePromotion(t, store, fixture)
				} else {
					capture = transactionPrepareConsent(t, store, fixture.command)
				}
				transactionExec(t, pool, "ALTER TABLE "+pgx.Identifier{table}.Sanitize()+" RENAME TO unavailable")
				var err error
				if kind == "promotion" {
					err = store.CommitPromotion(t.Context(), capture.fence, capture.promotion)
				} else {
					err = store.CommitConsent(t.Context(), capture.fence, capture.consent)
				}
				var postgresError *pgconn.PgError
				if !errors.As(err, &postgresError) || postgresError.Code != "42P01" {
					t.Fatalf("failed persisted write read lost PostgreSQL cause: %v", err)
				}
				var revision string
				if err := pool.QueryRow(t.Context(), `SELECT authority_revision::text FROM suites`).Scan(&revision); err != nil || revision != map[string]string{"consent": "0", "promotion": "1"}[kind] {
					t.Fatalf("failed write read advanced authority: %q, %v", revision, err)
				}
			})
		}
	}
}

func TestTransactionAliasSourceMustPointToOriginalReceipt(t *testing.T) {
	pool, store, fixture := transactionDatabase(t)
	transactionApprove(t, store, fixture)
	alias := codecValue(contract.NewCommand(contract.CommandInput{OperationID: "alias", SourceCommandID: "source-approve", Actor: fixture.command.Actor(), Reference: fixture.request.Reference, Carrier: "carrier", Action: contract.ApproveConsent, Order: 3}))
	if response, err := governance.ProcessConsent(t.Context(), store, governance.ConsentRequest{Command: alias}); err != nil || !response.Duplicate {
		t.Fatalf("alias fixture: %+v, %v", response, err)
	}
	transactionExec(t, pool, `ALTER TABLE consent_sources DISABLE TRIGGER USER; UPDATE consent_sources SET operation_id='alias'`)
	if _, err := store.Load(t.Context(), governance.ReadRequest{SourceCommandID: "source-approve"}); !errors.Is(err, governance.ErrInvalidSnapshot) {
		t.Fatalf("source alias replaced immutable original operation identity: %v", err)
	}
}

func TestTransactionPromotionReplayRequiresMatchingStoredFacts(t *testing.T) {
	for _, scenario := range []struct {
		name string
		sql  string
	}{
		{"operation identity", `ALTER TABLE operation_receipts DISABLE TRIGGER USER; UPDATE operation_receipts SET receipt_payload=jsonb_set(receipt_payload,'{Promotion,Operation}','"foreign"') WHERE operation_id='promote'`},
		{"protected contract", `ALTER TABLE operation_receipts DISABLE TRIGGER USER; UPDATE operation_receipts SET receipt_payload=jsonb_set(receipt_payload,'{Promotion,Domain,protected,Scope}','"sha256:0000000000000000000000000000000000000000000000000000000000000000"') WHERE operation_id='promote'`},
		{"current record differs from version", `UPDATE suites SET authority_revision=authority_revision+1, governance_payload=jsonb_set(jsonb_set(governance_payload,'{base,canonical,Record,Carrier}','"foreign"'),'{base,canonical,Suite,Revision}',to_jsonb(authority_revision+1))`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pool, store, fixture := transactionDatabase(t)
			transactionApprove(t, store, fixture)
			if response, err := governance.Promote(t.Context(), store, fixture.request); err != nil || !response.Committed {
				t.Fatalf("promotion fixture: %+v, %v", response, err)
			}
			transactionExec(t, pool, scenario.sql)
			read := governance.ReadRequest{OperationID: "promote"}
			if scenario.name == "current record differs from version" {
				read = governance.ReadRequest{Reference: fixture.request.Reference}
			}
			if _, err := store.Load(t.Context(), read); !errors.Is(err, governance.ErrInvalidSnapshot) {
				t.Fatalf("inconsistent persisted successful facts yielded authority: %v", err)
			}
		})
	}
}

type transactionTraceKey struct{}

type transactionCancelAfterRead struct {
	query  string
	cancel context.CancelFunc
}

func (trace transactionCancelAfterRead) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, transactionTraceKey{}, strings.Contains(data.SQL, trace.query))
}

func (trace transactionCancelAfterRead) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if match, _ := ctx.Value(transactionTraceKey{}).(bool); match {
		trace.cancel()
	}
}

func TestTransactionCancellationAfterCompletedReadCannotYieldReplayOrSnapshot(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		query string
		read  governance.ReadRequest
	}{
		{"exact replay", "FROM operation_receipts WHERE", governance.ReadRequest{OperationID: "approve"}},
		{"source reservation without current authority", "FROM suites WHERE", governance.ReadRequest{Reference: contract.ProposalReference{ProjectID: "foreign", SuiteID: "foreign"}, SourceCommandID: "source-approve"}},
		{"current authority", "FROM promotions", governance.ReadRequest{Reference: contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "proposal", RevisionID: "r1"}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			pool, store, fixture := transactionDatabase(t)
			transactionApprove(t, store, fixture)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			config := pool.Config()
			config.ConnConfig.Tracer = transactionCancelAfterRead{scenario.query, cancel}
			tracedPool, err := pgxpool.NewWithConfig(t.Context(), config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tracedPool.Close)
			tracedStore := codecValue(NewStore(tracedPool, store.verifier))
			got, err := tracedStore.Load(ctx, scenario.read)
			if !errors.Is(err, context.Canceled) || got.Operation.Kind != 0 || got.Source.Kind != 0 || !got.Canonical.IsZero() {
				t.Fatalf("canceled completed read yielded usable persisted facts: %+v, %v", got, err)
			}
		})
	}
}

func TestTransactionInitializationCannotImportForeignConsentOrReplaceBaseline(t *testing.T) {
	pool, store, fixture := transactionDatabase(t)
	if _, err := NewStore(nil, transactionVerifier(nil)); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("store accepted absent database pool: %v", err)
	}
	foreign := fixture.authority
	foreign.Proposals = []TrustedProposal{{Proposal: fixture.authority.Proposals[0].Proposal, Consent: codecValue(contract.NewConsent("project", "suite", "foreign-proposal"))}}
	if err := store.InitializeTrusted(t.Context(), foreign); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("initializer imported inconsistent consent scope: %v", err)
	}
	transactionApprove(t, store, fixture)
	if response, err := governance.Promote(t.Context(), store, fixture.request); err != nil || !response.Committed {
		t.Fatalf("promotion fixture: %+v, %v", response, err)
	}
	current, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference})
	if err != nil {
		t.Fatal(err)
	}
	baseline := fixture.authority
	baseline.Canonical = current.Canonical
	if err := store.InitializeTrusted(t.Context(), baseline); !errors.Is(err, governance.ErrInvalidRequest) {
		t.Fatalf("initializer replaced existing canonical baseline: %v", err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO operation_receipts SELECT * FROM operation_receipts WHERE operation_id='approve'`)
	if !errors.Is(storageError(err), governance.ErrOperationConflict) {
		t.Fatalf("real global operation uniqueness conflict lost its domain category: %v", err)
	}
}

func TestTransactionOriginalSourceCannotBeCommittedAsNewReceipt(t *testing.T) {
	_, store, fixture := transactionDatabase(t)
	transactionApprove(t, store, fixture)
	alias := codecValue(contract.NewCommand(contract.CommandInput{OperationID: "alias", SourceCommandID: "source-approve", Actor: fixture.command.Actor(), Reference: fixture.request.Reference, Carrier: "carrier", Action: contract.ApproveConsent, Order: 3}))
	capture := transactionPrepareConsent(t, store, alias)
	if !capture.consent.Alias {
		t.Fatal("fixture failed to capture a true source alias")
	}
	capture.consent.Alias = false
	if err := store.CommitConsent(t.Context(), capture.fence, capture.consent); !errors.Is(err, governance.ErrOperationConflict) {
		t.Fatalf("duplicate source committed as a new original receipt: %v", err)
	}
	got, err := store.Load(t.Context(), governance.ReadRequest{Reference: fixture.request.Reference})
	if err != nil || got.Fence.Revision != 1 || len(got.Consent.Results()) != 1 {
		t.Fatalf("rejected false-original alias changed authority: %+v, %v", got, err)
	}
}
