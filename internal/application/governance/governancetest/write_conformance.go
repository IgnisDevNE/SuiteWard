package governancetest

import (
	"context"
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// The cases of the M1.6a governance writes: CreateSuite, RecordAssessment and
// the outbox messages of the use cases.

const cfOtherPolicy = contract.PolicyRevisionID("policy-2")

// seedPolicyHolder seeds a Suite that only registers the policy revisions the
// CreateSuite cases refer to: policy-1 for cfProject through the shared
// helper, and policy-2 through a second Suite of the same project.
func (e *cfEnv) seedPolicyHolder() {
	e.t.Helper()
	e.seedSimple(cfProject, cfSuite)
	snapshot := cfMust(contract.NewCanonicalSnapshot(cfMust(contract.NewSuite(cfProject, "holder-2", "", cfSeededRevision)), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}))
	e.seed(governance.Seed{Suite: governance.SuiteState{Canonical: snapshot, Policy: cfMust(contract.NewPolicy(cfProject, cfOtherPolicy, e.owner)), Target: cfTarget}})
}

func (e *cfEnv) createSuite(project contract.ProjectID, suite contract.SuiteID, policy contract.PolicyRevisionID, target contract.IntegrationTargetID) error {
	return e.store.CreateSuite(context.Background(), project, suite, policy, target)
}

func cfCreateSuiteCreatesAnEmptySuite(e *cfEnv) {
	e.seedPolicyHolder()

	if err := e.createSuite(cfProject, "created", cfPolicy, "branch:main"); err != nil {
		e.t.Fatal(err)
	}

	err := e.doIn(cfProject, "created", func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil {
			return err
		}
		suite := state.Canonical.Suite()
		if _, present := suite.CurrentVersionID(); present || suite.Revision() != 0 {
			e.t.Errorf("suite has a current version or revision %d, want an empty Suite at revision 0", suite.Revision())
		}
		if state.Policy.RevisionID() != cfPolicy || state.Policy.ProjectID() != cfProject || state.Target != "branch:main" {
			e.t.Errorf("suite is governed by %q of %q targeting %q", state.Policy.RevisionID(), state.Policy.ProjectID(), state.Target)
		}
		if _, _, err := tx.Proposal(ctx, "p1"); !errors.Is(err, governance.ErrNotFound) {
			e.t.Errorf("proposal of a new Suite = %v, want not found", err)
		}
		return nil
	})
	if err != nil {
		e.t.Fatalf("Do on the created Suite = %v, want it found", err)
	}
}

func cfCreateSuiteIsIdempotent(e *cfEnv) {
	e.seedPolicyHolder()
	if err := e.createSuite(cfProject, "created", cfPolicy, cfTarget); err != nil {
		e.t.Fatal(err)
	}
	// A Suite that has moved on must not be reset by a repeated call.
	c := cfNewCandidate(cfProject, "created", "p1", "", "v1")
	err := e.doIn(cfProject, "created", func(ctx context.Context, tx governance.Tx) error {
		return tx.AppendProposalRevision(ctx, governance.ProposalWrite{Revision: c.proposal.Current()})
	})
	if err != nil {
		e.t.Fatal(err)
	}

	if err := e.createSuite(cfProject, "created", cfPolicy, cfTarget); err != nil {
		e.t.Fatalf("identical CreateSuite = %v, want success", err)
	}

	if got := e.revisionIn(cfProject, "created"); got != 1 {
		e.t.Fatalf("revision = %d, want 1: the repeated call wrote", got)
	}
	err = e.doIn(cfProject, "created", func(ctx context.Context, tx governance.Tx) error {
		_, _, err := tx.Proposal(ctx, "p1")
		return err
	})
	if err != nil {
		e.t.Fatalf("the proposal is gone after the repeated call: %v", err)
	}
}

func cfCreateSuiteConflicts(e *cfEnv) {
	e.seedPolicyHolder()
	if err := e.createSuite(cfProject, "created", cfPolicy, cfTarget); err != nil {
		e.t.Fatal(err)
	}

	for name, attempt := range map[string]func() error{
		"another policy": func() error { return e.createSuite(cfProject, "created", cfOtherPolicy, cfTarget) },
		"another target": func() error { return e.createSuite(cfProject, "created", cfPolicy, "branch:other") },
	} {
		e.run(name, func(e *cfEnv) {
			if err := attempt(); !errors.Is(err, governance.ErrConflict) {
				e.t.Fatalf("CreateSuite = %v, want a conflict", err)
			}
		})
	}
	err := e.doIn(cfProject, "created", func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		if err != nil || state.Policy.RevisionID() != cfPolicy || state.Target != cfTarget {
			e.t.Errorf("the Suite changed: policy %q target %q, %v", state.Policy.RevisionID(), state.Target, err)
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
}

func cfCreateSuiteNeedsAnExistingPolicy(e *cfEnv) {
	e.seedPolicyHolder()

	for name, tc := range map[string]struct {
		project contract.ProjectID
		suite   contract.SuiteID
		policy  contract.PolicyRevisionID
		target  contract.IntegrationTargetID
		want    error
	}{
		"unknown revision": {cfProject, "created", "policy-9", cfTarget, governance.ErrNotFound},
		"another project":  {"project-b", "created", cfPolicy, cfTarget, governance.ErrNotFound},
		"blank policy":     {cfProject, "created", "", cfTarget, governance.ErrInvalidRequest},
		"blank target":     {cfProject, "created", cfPolicy, "", governance.ErrInvalidRequest},
		"blank suite":      {cfProject, "", cfPolicy, cfTarget, governance.ErrInvalidRequest},
		"blank project":    {"", "created", cfPolicy, cfTarget, governance.ErrInvalidRequest},
	} {
		e.run(name, func(e *cfEnv) {
			if err := e.createSuite(tc.project, tc.suite, tc.policy, tc.target); !errors.Is(err, tc.want) {
				e.t.Fatalf("CreateSuite = %v, want %v", err, tc.want)
			}
		})
	}
	for _, project := range []contract.ProjectID{cfProject, "project-b"} {
		if err := e.doIn(project, "created", func(context.Context, governance.Tx) error { return nil }); !errors.Is(err, governance.ErrNotFound) {
			e.t.Fatalf("Do on %q after the failures = %v, want not found", project, err)
		}
	}
}

func (e *cfEnv) revisionIn(project contract.ProjectID, suite contract.SuiteID) contract.StateRevision {
	e.t.Helper()
	var revision contract.StateRevision
	err := e.doIn(project, suite, func(ctx context.Context, tx governance.Tx) error {
		state, err := tx.Suite(ctx)
		revision = state.Canonical.Suite().Revision()
		return err
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return revision
}

// bare is a two-revision candidate whose assessments are not seeded, so the
// assessment cases write them.
func (e *cfEnv) bare() cfCandidate {
	e.t.Helper()
	c := e.candidate("p1", "", "v1", "revision-1", "revision-2")
	c.assessments = nil
	e.seedSimple(cfProject, cfSuite, c)
	return c
}

func (e *cfEnv) assessmentWrite(c cfCandidate, revision contract.ProposalRevisionID, source contract.SourceRevision, outcome contract.IntegrityOutcome) governance.AssessmentWrite {
	return governance.AssessmentWrite{Reference: c.reference(revision), Source: source,
		Evidence: cfMust(contract.NewIntegrityEvidence(governance.ServiceEmitter.ID(), source, c.bindings[revision], outcome))}
}

func (e *cfEnv) recordAssessment(w governance.AssessmentWrite) error {
	return e.do(func(ctx context.Context, tx governance.Tx) error { return tx.RecordAssessment(ctx, w) })
}

func (e *cfEnv) assessment(reference contract.ProposalReference, source contract.SourceRevision) (contract.IntegrityAssessment, bool) {
	e.t.Helper()
	var assessment contract.IntegrityAssessment
	var found bool
	e.read(func(ctx context.Context, tx governance.Tx) (err error) {
		assessment, found, err = tx.Assessment(ctx, reference, source)
		return err
	})
	return assessment, found
}

func cfRecordAssessmentStoresAndReadsBack(e *cfEnv) {
	c := e.bare()
	before := e.revision()
	write := e.assessmentWrite(c, "revision-2", "source-1", contract.IntegrityPassed)

	if err := e.recordAssessment(write); err != nil {
		e.t.Fatal(err)
	}

	got, found := e.assessment(write.Reference, "source-1")
	want := cfMust(contract.AssessIntegrity("source-1", c.bindings["revision-2"], &write.Evidence))
	if !found || !cfSameAssessment(got, want) || !got.Passed() {
		e.t.Fatalf("assessment = %+v found %v, want the passing one written", got, found)
	}
	if evidence, _ := got.Evidence(); evidence.EmitterID() != governance.ServiceEmitter.ID() || governance.ServiceEmitter.Kind() != contract.Service {
		e.t.Errorf("evidence emitter = %q, want the service principal %q", evidence.EmitterID(), governance.ServiceEmitter.ID())
	}
	e.requireWrites(before, 1)
	if _, other := e.assessment(write.Reference, "source-2"); other {
		e.t.Fatal("an assessment appeared for a source that was never written")
	}
}

func cfRecordAssessmentKeepsEvidenceOfAnotherRevision(e *cfEnv) {
	c := e.bare()
	write := e.assessmentWrite(c, "revision-2", "source-1", contract.IntegrityPassed)
	write.Evidence = cfMust(contract.NewIntegrityEvidence(governance.ServiceEmitter.ID(), "source-1", c.bindings["revision-1"], contract.IntegrityPassed))

	if err := e.recordAssessment(write); err != nil {
		e.t.Fatal(err)
	}

	got, found := e.assessment(write.Reference, "source-1")
	if !found || got.Passed() || got.Reason() != contract.IntegrityReasonMismatch || !got.Binding().Equal(c.bindings["revision-2"]) {
		e.t.Fatalf("assessment = %+v found %v, want a mismatch bound to revision-2", got, found)
	}
}

func cfRecordAssessmentIsIdempotent(e *cfEnv) {
	c := e.bare()
	write := e.assessmentWrite(c, "revision-2", "source-1", contract.IntegrityFailed)
	if err := e.recordAssessment(write); err != nil {
		e.t.Fatal(err)
	}
	before := e.revision()

	if err := e.recordAssessment(write); err != nil {
		e.t.Fatalf("identical RecordAssessment = %v, want success", err)
	}

	e.requireWrites(before, 0)
	if got, _ := e.assessment(write.Reference, "source-1"); got.Reason() != contract.IntegrityReasonFailed {
		e.t.Fatalf("stored reason = %v, want failed", got.Reason())
	}
}

func cfRecordAssessmentWithOtherEvidenceConflicts(e *cfEnv) {
	c := e.bare()
	stored := e.assessmentWrite(c, "revision-2", "source-1", contract.IntegrityPassed)
	if err := e.recordAssessment(stored); err != nil {
		e.t.Fatal(err)
	}
	before := e.revision()

	for name, evidence := range map[string]contract.IntegrityEvidence{
		"another outcome": cfMust(contract.NewIntegrityEvidence(governance.ServiceEmitter.ID(), "source-1", c.bindings["revision-2"], contract.IntegrityFailed)),
		"another binding": cfMust(contract.NewIntegrityEvidence(governance.ServiceEmitter.ID(), "source-1", c.bindings["revision-1"], contract.IntegrityPassed)),
	} {
		e.run(name, func(e *cfEnv) {
			err := e.recordAssessment(governance.AssessmentWrite{Reference: stored.Reference, Source: stored.Source, Evidence: evidence})
			if !errors.Is(err, governance.ErrConflict) {
				e.t.Fatalf("RecordAssessment = %v, want a conflict", err)
			}
		})
	}
	e.requireWrites(before, 0)
	if got, _ := e.assessment(stored.Reference, "source-1"); !got.Passed() {
		e.t.Fatal("the stored assessment changed")
	}
}

func cfRecordAssessmentNeedsAKnownRevisionOfThisProposal(e *cfEnv) {
	c := e.bare()
	other := cfNewCandidate(cfProject, cfSuite, "p2", "", "v2")
	good := e.assessmentWrite(c, "revision-2", "source-1", contract.IntegrityPassed)
	unknownRevision := e.assessmentWrite(c, "revision-2", "source-1", contract.IntegrityPassed)
	unknownRevision.Reference.RevisionID = "revision-9"
	ghost := cfNewCandidate(cfProject, cfSuite, "p9", "", "v1") // never seeded
	unknownProposal := e.assessmentWrite(ghost, "revision-1", "source-1", contract.IntegrityPassed)
	foreignEvidence := good
	foreignEvidence.Evidence = cfMust(contract.NewIntegrityEvidence(governance.ServiceEmitter.ID(), "source-1", other.bindings["revision-1"], contract.IntegrityPassed))
	otherSuite := good
	otherSuite.Reference.SuiteID = "suite-b"
	before := e.revision()

	for name, tc := range map[string]struct {
		write governance.AssessmentWrite
		want  error
	}{
		"unknown revision":             {unknownRevision, governance.ErrNotFound},
		"unknown proposal":             {unknownProposal, governance.ErrNotFound},
		"evidence of another proposal": {foreignEvidence, governance.ErrInvalidRequest},
		"reference of another Suite":   {otherSuite, governance.ErrInvalidRequest},
		"zero evidence":                {governance.AssessmentWrite{Reference: good.Reference, Source: "source-1"}, governance.ErrInvalidRequest},
		"blank source":                 {governance.AssessmentWrite{Reference: good.Reference, Evidence: good.Evidence}, governance.ErrInvalidRequest},
	} {
		e.run(name, func(e *cfEnv) {
			if err := e.recordAssessment(tc.write); !errors.Is(err, tc.want) {
				e.t.Fatalf("RecordAssessment = %v, want %v", err, tc.want)
			}
		})
	}
	e.requireWrites(before, 0)
	if _, found := e.assessment(good.Reference, "source-1"); found {
		e.t.Fatal("a rejected write left an assessment")
	}
}

func cfRecordAssessmentAdvancesTheRevisionAndRollsBack(e *cfEnv) {
	c := e.bare()
	before := e.revision()
	first := e.assessmentWrite(c, "revision-2", "source-1", contract.IntegrityPassed)
	second := e.assessmentWrite(c, "revision-2", "source-2", contract.IntegrityUnavailable)

	err := e.do(func(ctx context.Context, tx governance.Tx) error {
		for _, write := range []governance.AssessmentWrite{first, second} {
			if err := tx.RecordAssessment(ctx, write); err != nil {
				return err
			}
		}
		if got, found, err := tx.Assessment(ctx, second.Reference, "source-2"); err != nil || !found || got.Reason() != contract.IntegrityReasonUnavailable {
			e.t.Errorf("own write not visible: %v found %v, %v", got.Reason(), found, err)
		}
		return errCfAbort
	})
	if !errors.Is(err, errCfAbort) {
		e.t.Fatalf("error = %v, want the error of the unit of work", err)
	}
	e.requireWrites(before, 0)
	if _, found := e.assessment(first.Reference, "source-1"); found {
		e.t.Fatal("a rolled back assessment is visible")
	}

	if err := e.recordAssessment(first); err != nil {
		e.t.Fatal(err)
	}
	if err := e.recordAssessment(second); err != nil {
		e.t.Fatal(err)
	}
	e.requireWrites(before, 2)
}

func cfBootstrapNeedsTheRecordedAssessment(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	c.assessments = nil
	e.seedSimple(cfProject, cfSuite, c)
	e.approve(c)
	request := c.baselineRequest("bootstrap-1", "version-1")

	before := e.revision()
	blocked, err := governance.Bootstrap(context.Background(), e.store, request)
	if err != nil || blocked.Outcome != contract.PromotionBlocked {
		e.t.Fatalf("bootstrap without evidence = %+v, %v, want a block", blocked, err)
	}
	e.requireWrites(before, 0)
	if err := e.recordAssessment(e.assessmentWrite(c, "revision-1", c.origin, contract.IntegrityPassed)); err != nil {
		e.t.Fatal(err)
	}
	assessed := e.revision()

	result, err := governance.Bootstrap(context.Background(), e.store, request)

	if err != nil || result.Outcome != contract.PromotionProposed || result.Duplicate {
		e.t.Fatalf("bootstrap after the assessment = %+v, %v, want a new proposed promotion", result, err)
	}
	e.requireWrites(assessed, 1)
	if got := e.currentVersion(); got != "version-1" {
		e.t.Fatalf("current version = %q, want version-1", got)
	}
}

// Outbox messages of the use cases.

func cfMessages(keys ...string) []governance.OutboxMessage {
	messages := make([]governance.OutboxMessage, len(keys))
	for i, key := range keys {
		messages[i] = governance.OutboxMessage{Key: key, Kind: "probe", Payload: cfObject}
	}
	return messages
}

func cfConsentOutbox(e *cfEnv) {
	c := e.candidate("p1", "", "v1", "revision-1", "revision-2")
	e.seedSimple(cfProject, cfSuite, c)
	process := func(command contract.Command, keys ...string) {
		e.t.Helper()
		if _, err := governance.ProcessConsent(context.Background(), e.store, governance.ConsentRequest{Command: command, Outbox: cfMessages(keys...)}); err != nil {
			e.t.Fatalf("process consent %q: %v", command.OperationID(), err)
		}
	}
	approve := e.command(c, "revision-2", "approve-1", "comment-1", contract.ApproveConsent, 1)

	before := e.revision()
	process(approve, "ack-1", "ack-1b")
	e.requireQueued(nil, []string{"ack-1", "ack-1b"})
	e.requireWrites(before, 1) // the outbox does not advance the revision

	process(approve, "ack-replay")                                                                             // replay by operation id
	process(e.command(c, "revision-2", "approve-alias", "comment-1", contract.ApproveConsent, 1), "ack-alias") // replay by source
	e.requireQueued(nil, []string{"ack-1", "ack-1b"})

	// A rejected result is a new fact and gets its message.
	process(e.command(c, "revision-1", "approve-stale", "comment-2", contract.ApproveConsent, 2), "ack-rejected")
	e.requireQueued(nil, []string{"ack-1", "ack-1b", "ack-rejected"})
}

func cfConsentOutboxFailureRollsBackTheFact(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	approve := e.command(c, "revision-1", "approve-1", "comment-1", contract.ApproveConsent, 1)
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"approve-1"}, sources: []contract.SourceCommandID{"comment-1"}}
	before := e.view(probe)

	for name, messages := range map[string][]governance.OutboxMessage{
		"repeated key":  cfMessages("ack", "ack"),
		"invalid kind":  {{Key: "ack", Kind: "Bad Kind", Payload: cfObject}},
		"blank payload": {{Key: "ack", Kind: "probe"}},
	} {
		e.run(name, func(e *cfEnv) {
			_, err := governance.ProcessConsent(context.Background(), e.store, governance.ConsentRequest{Command: approve, Outbox: messages})
			if err == nil {
				e.t.Fatal("ProcessConsent succeeded, want the message write to fail")
			}
		})
	}
	e.requireSameView(before, e.view(probe))
	e.requireQueued(nil, nil)
}

func cfPromotionOutbox(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	request := c.baselineRequest("bootstrap-1", "version-1")
	request.Outbox = cfMessages("ack-bootstrap")

	unapproved := e.revision()
	blocked, err := governance.Bootstrap(context.Background(), e.store, request) // not approved yet
	if err != nil || blocked.Outcome != contract.PromotionBlocked || blocked.Reason != contract.PromotionReasonApprovalMissing {
		e.t.Fatalf("bootstrap without approval = %+v, %v, want a block for the missing approval", blocked, err)
	}
	e.requireWrites(unapproved, 0)
	e.requireQueued(nil, nil)
	e.approve(c)
	before := e.revision()

	result, err := governance.Bootstrap(context.Background(), e.store, request)

	if err != nil || result.Outcome != contract.PromotionProposed || result.Duplicate {
		e.t.Fatalf("bootstrap = %+v, %v, want a new proposed promotion", result, err)
	}
	e.requireQueued(nil, []string{"ack-bootstrap"})
	e.requireWrites(before, 1)

	replay := request
	replay.Outbox = cfMessages("ack-replay")
	again, err := governance.Bootstrap(context.Background(), e.store, replay)
	if err != nil || !again.Duplicate {
		e.t.Fatalf("replay = %+v, %v, want a duplicate", again, err)
	}
	e.requireQueued(nil, []string{"ack-bootstrap"})
	e.read(func(ctx context.Context, tx governance.Tx) error {
		receipt, found, err := tx.Receipt(ctx, "bootstrap-1")
		if err != nil || !found || len(receipt.Promotion.Identity.Request.Outbox) != 0 {
			e.t.Errorf("stored receipt = %+v found %v, %v, want a request without the outbox", receipt, found, err)
		}
		return nil
	})
}

func cfPromotionOutboxFailureRollsBackTheFact(e *cfEnv) {
	c := e.candidate("p1", "", "v1")
	e.seedSimple(cfProject, cfSuite, c)
	e.approve(c)
	request := c.baselineRequest("bootstrap-1", "version-1")
	request.Outbox = cfMessages("ack", "ack")
	probe := cfProbe{proposal: "p1", operations: []contract.OperationID{"bootstrap-1"}, versions: []contract.SuiteVersionID{"version-1"}, references: []contract.ProposalReference{c.current()}}
	before := e.view(probe)

	if _, err := governance.Bootstrap(context.Background(), e.store, request); err == nil {
		e.t.Fatal("bootstrap succeeded, want the message write to fail")
	}

	e.requireSameView(before, e.view(probe))
	e.requireQueued(nil, nil)
	request.Outbox = cfMessages("ack")
	if result, err := governance.Bootstrap(context.Background(), e.store, request); err != nil || result.Outcome != contract.PromotionProposed || result.Duplicate {
		e.t.Fatalf("retry = %+v, %v, want a new proposed promotion", result, err)
	}
	e.requireQueued(nil, []string{"ack"})
}

func cfRecordAssessmentRejectsAnotherEmitter(e *cfEnv) {
	c := e.bare()
	before := e.revision()
	write := e.assessmentWrite(c, "revision-2", "source-1", contract.IntegrityPassed)
	write.Evidence = cfMust(contract.NewIntegrityEvidence("verifier", "source-1", c.bindings["revision-2"], contract.IntegrityPassed))

	if err := e.recordAssessment(write); !errors.Is(err, governance.ErrInvalidRequest) {
		e.t.Fatalf("RecordAssessment = %v, want an invalid request for evidence not emitted by %q", err, governance.ServiceEmitter.ID())
	}

	e.requireWrites(before, 0)
	if _, found := e.assessment(write.Reference, "source-1"); found {
		e.t.Fatal("evidence from another emitter was stored")
	}
}
