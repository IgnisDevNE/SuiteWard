package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution/executiontest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// The world of the execution commands of M2.1 (docs/contracts/runtime.md, "Commands"): repository 2 has a canonical V1 whose declaration names the
// execution policy, and a change proposal P2 whose revision R1 expects V1 and is carried by pull request 7, which has the head cjHead. The execution
// ledger is the in-memory store that writes its entries in the commands ledger of the world, as the PostgreSQL store does.

const cjExecProposal = "P2-R1" // the current revision of the change proposal, which the gate acts on

// cjExecWorld is a commands world with the execution ledger and a canonical whose policy has the levels it was built with.
type cjExecWorld struct {
	*cjWorld
	store       *executiontest.Memory
	declaration string
	version     artifact.Manifest // V1, the manifest that the revision expects
	leases      int
}

// newCjExecWorld builds the world for the canonical and receipt levels. applicable says whether R1 adds or changes a file that the include matches.
func newCjExecWorld(t *testing.T, canonical, receipt string, applicable bool) *cjExecWorld {
	t.Helper()
	w := newCjWorld(t)
	x := &cjExecWorld{cjWorld: w, store: executiontest.NewMemory(w.ledger)}
	x.declaration = fmt.Sprintf("version: 1\ninclude:\n  - \"*_test.go\"\nexecution:\n  profile: go\n  canonical: %s\n  receipt: %s\n", canonical, receipt)
	parsed := cjMust(scope.ParseDeclaration([]byte(x.declaration)))
	declared := artifact.Entry{Path: scope.DeclarationPath, Content: artifact.Hash([]byte(x.declaration))}
	x.version = cjMust(artifact.NewManifest([]artifact.Entry{declared, {Path: "a_test.go", Content: artifact.Hash([]byte("v1"))}}))
	revised := []artifact.Entry{declared, {Path: "a_test.go", Content: artifact.Hash([]byte("v1"))}, {Path: "notes.txt", Content: artifact.Hash([]byte("a note"))}}
	if applicable {
		revised[1] = artifact.Entry{Path: "a_test.go", Content: artifact.Hash([]byte("v2"))}
	}
	revision := cjMust(artifact.NewManifest(revised))
	protected := cjMust(contract.NewProtectedContract(x.version, scope.NewScope(parsed).Digest(), map[string]string{}))

	project, carrier := owner.ProjectID(cjID2), carrierID(cjID2, cjPull)
	baseline := contract.ProposalReference{ProjectID: project, SuiteID: "default", ProposalID: "P1", RevisionID: "R1"}
	baselineBinding := cjMust(contract.NewApprovalBinding(contract.BindingInput{Reference: baseline, Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(),
		PolicyRevision: "setup-1", CoveredInputs: protected.CoveredInputs()}))
	first := cjMust(contract.NewProposal(cjMust(contract.NewProposalRevision(baselineBinding, "origin", contract.OriginBranch, carrierID(cjID2, 3)))))
	change := contract.ProposalReference{ProjectID: project, SuiteID: "default", ProposalID: "P2", RevisionID: "R1"}
	changeBinding := cjMust(contract.NewApprovalBinding(contract.BindingInput{Reference: change, ExpectedCanonical: "V1", Manifest: revision.Digest(), Scope: protected.ScopeDigest(),
		PolicyRevision: "setup-1", CoveredInputs: protected.CoveredInputs()}))
	second := cjMust(contract.NewProposal(cjMust(contract.NewProposalRevision(changeBinding, cjHead, contract.OriginPull, carrier))))
	version := cjMust(contract.NewSuiteVersion(project, "default", "V1", x.version))
	record := cjMust(contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: "seed-canonical", VersionID: "V1", Binding: baselineBinding, Carrier: carrierID(cjID2, 3),
		Source: "origin", Target: "branch:main", RecordedAt: cjOrigin.Add(time.Minute)}))
	canonicalSnapshot := cjMust(contract.NewCanonicalSnapshot(cjMust(contract.NewSuite(project, "default", "V1", 1)), version, protected, record))
	principal := cjMust(contract.NewPrincipal(owner.PrincipalID(cjOwnerID), contract.Human))
	seed := governance.Seed{Suite: governance.SuiteState{Canonical: canonicalSnapshot, Policy: cjMust(contract.NewPolicy(project, "setup-1", principal)), Target: "branch:main"},
		History:   []contract.HistoricalCanonical{cjMust(contract.NewHistoricalCanonical(version, record))},
		Proposals: []governance.SeedProposal{{Proposal: first}, {Proposal: second}}}
	if err := w.mem.Seed(t.Context(), seed); err != nil {
		t.Fatal(err)
	}
	for digest, bytes := range map[artifact.Digest][]byte{declared.Content: []byte(x.declaration), revision.Digest(): revision.Encoding()} {
		w.content.stored[digest.String()] = true
		if w.content.bytes == nil {
			w.content.bytes = map[string][]byte{}
		}
		w.content.bytes[digest.String()] = bytes
	}
	w.job.contents = w.content
	w.job.executions = x.store
	w.job.liveness = execution.NewLiveness(w.job.now()) // an executor was just seen: the runner responds, until a test says otherwise
	return x
}

// spec is the spec of a job of the plan on the head, bound to V1 and its declaration. A receipt job that is created already unavailable has no base.
func (x *cjExecWorld) spec(plan execution.Plan, head string, final bool) execution.Spec {
	s := execution.Spec{RepositoryID: cjID2, PullNumber: cjPull, Plan: plan, Profile: "go", HeadSHA: head, GoverningDigest: x.version.Digest(), GoverningSource: "V1",
		DeclarationDigest: artifact.Hash([]byte(x.declaration)), CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff}
	if plan == execution.PlanReceipt {
		s.CanonicalLevel, s.ReceiptLevel = domain.CanonicalLevelOff, domain.ReceiptLevelRequired
		if !final {
			s.BaseSHA = fmt.Sprintf("%040x", 77)
		}
		s.ChangedPaths, s.ChangedTotal = []string{"a_test.go"}, 1
	}
	return s
}

// pending creates a pending job of the plan on the head.
func (x *cjExecWorld) pending(plan execution.Plan, head string) execution.Job {
	x.t.Helper()
	job, created, err := x.store.Enqueue(x.t.Context(), execution.NewJob{Spec: x.spec(plan, head, false), At: cjOrigin.Add(time.Minute)})
	if err != nil || !created {
		x.t.Fatalf("Enqueue: created %v, error %v", created, err)
	}
	return job
}

// unavailable creates a job of the plan on the head that is already unavailable for reason (no-merge-base, executor-not-configured or github-unavailable).
func (x *cjExecWorld) unavailable(plan execution.Plan, head string, reason execution.Reason) execution.Job {
	x.t.Helper()
	job, created, err := x.store.Enqueue(x.t.Context(), execution.NewJob{Spec: x.spec(plan, head, true), Unavailable: reason, At: cjOrigin.Add(time.Minute)})
	if err != nil || !created {
		x.t.Fatalf("Enqueue: created %v, error %v", created, err)
	}
	return job
}

// lease grants the pending job to an executor, and returns it with its lease.
func (x *cjExecWorld) lease() execution.Job {
	x.t.Helper()
	x.leases++
	request := execution.LeaseRequest{Now: cjOrigin.Add(2 * time.Minute), LeaseID: fmt.Sprintf("%032x", x.leases), JobTimeout: 10 * time.Minute, GitHubWait: 10 * time.Minute}
	job, found, err := x.store.Lease(x.t.Context(), request, func(context.Context, execution.Job) error { return nil })
	if err != nil || !found {
		x.t.Fatalf("Lease: found %v, error %v", found, err)
	}
	return job
}

// cjTest is a test row of a complete receipt that has the state, for the head and the base to be consistent with it.
func cjTest(name, state string) execution.Test {
	t := execution.Test{Package: ".", Name: name, File: "a_test.go", Kind: execution.KindChanged, State: state}
	switch state {
	case "excluded":
		t.ExcludedReason = execution.ExcludedBuildConstraint
	default:
		t.HeadRuns, t.BaseRuns = []string{"pass"}, []string{"pass"}
	}
	return t
}

// complete leases the pending job and stores a complete receipt with the tests, the marker lines and the changed test files that do not parse.
func (x *cjExecWorld) complete(job execution.Job, tests []execution.Test, markers int, unparsed []string) execution.Job {
	x.t.Helper()
	leased := x.lease()
	if leased.ID != job.ID {
		x.t.Fatalf("the lease granted job %d; want %d", leased.ID, job.ID)
	}
	result := execution.Result{LeaseID: leased.LeaseID, Tests: tests, Report: execution.Report{Status: execution.StateComplete, ExecutorVersion: "0.4.0", ExecutorSHA256: artifact.Hash([]byte("executor")),
		ImageDigest: artifact.Hash([]byte("image")), Toolchain: "go1.25.1", Runtime: "podman 5.5.0", StartedAt: cjOrigin.Add(130 * time.Second), EndedAt: cjOrigin.Add(140 * time.Second),
		BodyDigest: artifact.Hash([]byte(fmt.Sprintf("body of %d", job.ID))), Unparsed: unparsed}}
	for i := range markers {
		result.Markers = append(result.Markers, execution.Marker{Kind: "env-read", Path: "a_test.go", Line: i + 1})
	}
	if _, err := x.store.Complete(x.t.Context(), job.ID, result, cjOrigin.Add(150*time.Second), 10*time.Minute); err != nil { // within the 60 s lease
		x.t.Fatalf("Complete: %v", err)
	}
	done, found, err := x.store.Job(x.t.Context(), job.ID)
	if err != nil || !found || done.State != execution.StateComplete {
		x.t.Fatalf("job %d is %+v (found %v, error %v); want complete", job.ID, done, found, err)
	}
	return done
}

// receiptJob creates the receipt job of the head and completes it with these tests.
func (x *cjExecWorld) receiptJob(tests []execution.Test, markers int, unparsed []string) execution.Job {
	x.t.Helper()
	return x.complete(x.pending(execution.PlanReceipt, cjHead), tests, markers, unparsed)
}

// receiptJobOf creates a complete, empty receipt job on another head.
func (x *cjExecWorld) receiptJobOf(head string) execution.Job {
	x.t.Helper()
	return x.complete(x.pending(execution.PlanReceipt, head), nil, 0, nil)
}

// say is a command comment of the owner on pull request 7 of repository 2, created at the minute, whose body is the command line.
func (x *cjExecWorld) say(id int64, minute int, command string) cjComment {
	created := cjAt(minute)
	return cjComment{pull: cjPull, id: id, author: cjOwnerID, created: created, updated: created, body: cjMarker + "\n/suiteward " + command}
}

// sayWithCode is say for a command that ends with the code of its comment, `offset` steps away from the right one.
func (x *cjExecWorld) sayWithCode(id int64, minute int, command string, offset int64) cjComment {
	return x.say(id, minute, command+" "+cjCode(cjAt(minute), offset))
}

// sync records a cycle of each repository that lists the comments, which are those of repository 2.
func (x *cjExecWorld) sync(comments ...cjComment) { x.syncAll(nil, comments...) }

// entry returns the ledger entry of the comment and its acknowledgment, which must exist.
func (x *cjExecWorld) entry(comment int64) (commands.Entry, *governance.OutboxMessage) {
	x.t.Helper()
	entry, ack, found := x.recorded(cjID2, comment)
	if !found {
		x.t.Fatalf("comment %d has no ledger entry\nlogs: %s", comment, x.logs)
	}
	return entry, ack
}

// requireEntry requires the disposition of the comment and the acknowledgment payload, and returns the entry.
func (x *cjExecWorld) requireEntry(comment int64, d commands.Disposition, ack map[string]any) commands.Entry {
	x.t.Helper()
	entry, _ := x.entry(comment)
	if entry.Disposition != d {
		x.t.Fatalf("comment %d is %q; want %q\nlogs: %s", comment, entry.Disposition, d, x.logs)
	}
	if ack == nil {
		x.requireSilent(cjID2, comment, d)
		return entry
	}
	x.requireAckPayload(cjID2, comment, d, ack)
	return entry
}

// requireGate requires the stored approval receipt of the comment, or none when want is nil.
func (x *cjExecWorld) requireGate(comment int64, want *execution.ApprovalGate) {
	x.t.Helper()
	got, found, err := x.store.Gate(x.t.Context(), cjID2, comment)
	if err != nil {
		x.t.Fatal(err)
	}
	switch {
	case want == nil && found:
		x.t.Fatalf("comment %d has the approval receipt %+v; want none", comment, got)
	case want != nil && (!found || got != *want):
		x.t.Fatalf("comment %d has the approval receipt %+v (found %v); want %+v", comment, got, found, *want)
	}
}

// consents counts the consent receipts of the Suite of repository 2 for the comment: 1 when ProcessConsent ran for it.
func (x *cjExecWorld) consented(comment int64) bool {
	x.t.Helper()
	var found bool
	err := x.mem.Do(x.t.Context(), owner.ProjectID(cjID2), "default", func(ctx context.Context, tx governance.Tx) error {
		_, ok, err := tx.Receipt(ctx, cjOperation(comment))
		found = ok
		return err
	})
	if err != nil {
		x.t.Fatal(err)
	}
	return found
}
