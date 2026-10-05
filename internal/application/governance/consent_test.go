package governance

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestProcessConsentCommitsOriginalApproval(t *testing.T) {
	f := newStoreFixture(t)
	store := newReferenceStore(t, f.snapshot)
	before := store.inspect()
	response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command})
	if err != nil || !response.Committed || response.Duplicate {
		t.Fatalf("original command = %+v, %v; want committed original outcome", response, err)
	}
	receipt := response.Receipt
	if receipt.Result.Command() != f.command || receipt.Result.Outcome() != contract.ConsentApproved || receipt.Result.Duplicate() ||
		receipt.EvaluatedReference != f.command.Reference() || receipt.PolicyRevisionID != f.snapshot.Policy.RevisionID() ||
		!receipt.CurrentApprovalEligible || receipt.PromotedVersionID != "" {
		t.Fatalf("receipt lost original outcome or evaluated authority: %+v", receipt)
	}
	after := store.inspect()
	currentAfter, hasAfter := after.canonical.Suite().CurrentVersionID()
	currentBefore, hasBefore := before.canonical.Suite().CurrentVersionID()
	if !after.consents[f.command.Reference().ProposalID].HasApproval(f.snapshot.Proposal, f.snapshot.Policy) {
		t.Fatal("committed consent does not authorize the exact current proposal")
	}
	if after.canonical.Suite().Revision() != before.canonical.Suite().Revision()+1 ||
		currentAfter != currentBefore || hasAfter != hasBefore ||
		!reflect.DeepEqual(after.promotions, before.promotions) || !reflect.DeepEqual(after.scheduling, before.scheduling) {
		t.Fatal("consent must advance the whole authority fence without promoting or rescheduling")
	}
	want := OperationReceipt{Kind: OperationConsent, Consent: receipt}
	if !reflect.DeepEqual(after.operations[f.command.OperationID()], want) || !reflect.DeepEqual(after.sources[f.command.SourceCommandID()], want) ||
		len(after.audits) != 1 || !reflect.DeepEqual(after.audits[0], want) || len(after.acknowledgments) != 1 || after.acknowledgments[0] != receipt || len(after.publications) != 0 {
		t.Fatal("original outcome, audit and acknowledgment were not stored together")
	}
	if before.consents[f.command.Reference().ProposalID].HasApproval(f.snapshot.Proposal, f.snapshot.Policy) || len(before.operations) != 0 {
		t.Fatal("processing mutated the earlier immutable snapshot")
	}
}

// consentStoreProbe retains the real store and only controls observed boundaries.
type consentStoreProbe struct {
	Store
	changeSnapshot func(*Snapshot)
	query          ReadRequest
	loads, commits int
	commitError    error
}

func (s *consentStoreProbe) Load(ctx context.Context, query ReadRequest) (Snapshot, error) {
	s.loads++
	s.query = query
	snapshot, err := s.Store.Load(ctx, query)
	if err == nil && s.changeSnapshot != nil {
		s.changeSnapshot(&snapshot)
	}
	return snapshot, err
}

func (s *consentStoreProbe) CommitConsent(ctx context.Context, fence AuthorityFence, write ConsentWrite) error {
	s.commits++
	if s.commitError != nil {
		return s.commitError
	}
	return s.Store.CommitConsent(ctx, fence, write)
}

func TestProcessConsentRejectsInvalidContext(t *testing.T) {
	f := newStoreFixture(t)
	t.Run("zero command before load", func(t *testing.T) {
		store := &consentStoreProbe{Store: newReferenceStore(t, f.snapshot)}
		response, err := ProcessConsent(context.Background(), store, ConsentRequest{})
		if !errors.Is(err, ErrInvalidRequest) || response != (ConsentResponse{}) || store.loads != 0 || store.commits != 0 {
			t.Fatalf("invalid request = %+v, %v, loads=%d commits=%d", response, err, store.loads, store.commits)
		}
	})
	for name, mutate := range map[string]func(*Snapshot){
		"zero canonical":           func(s *Snapshot) { s.Canonical = contract.CanonicalSnapshot{} },
		"foreign project fence":    func(s *Snapshot) { s.Fence.ProjectID = "other" },
		"foreign suite fence":      func(s *Snapshot) { s.Fence.SuiteID = "other" },
		"different revision fence": func(s *Snapshot) { s.Fence.Revision++ },
		"zero proposal":            func(s *Snapshot) { s.Proposal = contract.Proposal{} },
		"zero policy":              func(s *Snapshot) { s.Policy = contract.Policy{} },
		"zero consent":             func(s *Snapshot) { s.Consent = contract.Consent{} },
	} {
		t.Run(name, func(t *testing.T) {
			actual := newReferenceStore(t, f.snapshot)
			before := actual.inspect()
			store := &consentStoreProbe{Store: actual, changeSnapshot: mutate}
			response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command})
			if !errors.Is(err, ErrInvalidSnapshot) || response != (ConsentResponse{}) || store.commits != 0 || !reflect.DeepEqual(before, actual.inspect()) {
				t.Fatalf("invalid snapshot = %+v, %v, commits=%d", response, err, store.commits)
			}
		})
	}
}

func TestProcessConsentFailureHasNoAcknowledgment(t *testing.T) {
	for _, stage := range []string{"load", "consent", "receipt", "audit", "acknowledgment"} {
		t.Run(stage, func(t *testing.T) {
			f := newStoreFixture(t)
			store := newReferenceStore(t, f.snapshot)
			before := store.inspect()
			store.failAt = stage
			response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command})
			if !errors.Is(err, errReferenceFailure) || response != (ConsentResponse{}) || !reflect.DeepEqual(before, store.inspect()) {
				t.Fatalf("failed %s leaked success or state: %+v, %v", stage, response, err)
			}
		})
	}
	f := newStoreFixture(t)
	actual := newReferenceStore(t, f.snapshot)
	store := &consentStoreProbe{Store: actual, commitError: ErrAuthorityConflict}
	response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command})
	if !errors.Is(err, ErrAuthorityConflict) || response != (ConsentResponse{}) || store.commits != 1 || len(actual.inspect().acknowledgments) != 0 {
		t.Fatalf("conflicted commit = %+v, %v", response, err)
	}
	wantQuery := ReadRequest{Reference: f.command.Reference(), OperationID: f.command.OperationID(), SourceCommandID: f.command.SourceCommandID()}
	if store.query != wantQuery {
		t.Fatalf("load query = %+v, want %+v", store.query, wantQuery)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response, err = ProcessConsent(ctx, actual, ConsentRequest{Command: f.command})
	if !errors.Is(err, context.Canceled) || response != (ConsentResponse{}) {
		t.Fatalf("canceled = %+v, %v", response, err)
	}
}

func changedConsentCommand(t *testing.T, original contract.Command, change func(*contract.CommandInput)) contract.Command {
	t.Helper()
	input := contract.CommandInput{OperationID: original.OperationID(), SourceCommandID: original.SourceCommandID(), Actor: original.Actor(), Reference: original.Reference(), Carrier: original.Carrier(), Action: original.Action(), Order: original.Order()}
	change(&input)
	command, err := contract.NewCommand(input)
	return mustValue(t, command, err)
}

func processConsentForTest(t *testing.T, store Store, command contract.Command) ConsentResponse {
	t.Helper()
	response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: command})
	if err != nil || !response.Committed {
		t.Fatalf("process consent = %+v, %v", response, err)
	}
	return response
}

func TestProcessConsentReplaysOriginalOutcome(t *testing.T) {
	f := newStoreFixture(t)
	actual := newReferenceStore(t, f.snapshot)
	approved := processConsentForTest(t, actual, f.command)
	revoke := changedConsentCommand(t, f.command, func(c *contract.CommandInput) {
		c.OperationID = "revoke"
		c.SourceCommandID = "comment-revoke"
		c.Action = contract.RevokeConsent
		c.Order = 20
	})
	processConsentForTest(t, actual, revoke)
	before := actual.inspect()
	store := &consentStoreProbe{Store: actual}
	// The same authenticated source is immutable even when its observed body changes.
	edited := changedConsentCommand(t, f.command, func(c *contract.CommandInput) {
		c.Action = contract.RevokeConsent
		c.Order = 99
		c.Reference.RevisionID = "unknown"
		c.Carrier = "edited-carrier"
	})
	response := processConsentForTest(t, store, edited)
	if !response.Duplicate || response.Receipt != approved.Receipt || store.commits != 0 || !reflect.DeepEqual(before, actual.inspect()) {
		t.Fatal("pure replay changed original acknowledgment, consent or stored state")
	}
	if actual.inspect().consents[f.command.Reference().ProposalID].HasApproval(f.snapshot.Proposal, f.snapshot.Policy) {
		t.Fatal("historical approved result revived revoked consent")
	}

	alias := changedConsentCommand(t, edited, func(c *contract.CommandInput) { c.OperationID = "alias" })
	response = processConsentForTest(t, store, alias)
	after := actual.inspect()
	if !response.Duplicate || response.Receipt != approved.Receipt || store.commits != 1 || after.canonical.Suite().Revision() != before.canonical.Suite().Revision()+1 || len(after.operations) != len(before.operations)+1 || len(after.sources) != len(before.sources) || len(after.audits) != len(before.audits) || len(after.acknowledgments) != len(before.acknowledgments) {
		t.Fatal("alias must reserve identity without repeating the original audit or acknowledgment")
	}
	if after.operations[alias.OperationID()].Consent != approved.Receipt {
		t.Fatal("alias lost original receipt")
	}
	conflicting := changedConsentCommand(t, alias, func(c *contract.CommandInput) { c.SourceCommandID = "unrelated" })
	_, result, err := after.consents[alias.Reference().ProposalID].Apply(f.snapshot.Proposal, f.snapshot.Policy, conflicting)
	if err != nil || result.Reason() != contract.ConsentReasonCommandConflict {
		t.Fatal("domain operation alias was not persisted with global alias")
	}
	response = processConsentForTest(t, store, alias)
	if !response.Duplicate || store.commits != 1 || !reflect.DeepEqual(after, actual.inspect()) {
		t.Fatal("known alias wrote again")
	}
}

func TestProcessConsentReplaysRejectedReceiptAfterPolicyChange(t *testing.T) {
	f := newStoreFixture(t)
	actual := newReferenceStore(t, f.snapshot)
	other, err := contract.NewPrincipal("other", contract.Human)
	if err != nil {
		t.Fatal(err)
	}
	command := changedConsentCommand(t, f.command, func(c *contract.CommandInput) { c.Actor = other })
	original := processConsentForTest(t, actual, command)
	if original.Receipt.Result.Reason() != contract.ConsentReasonUnauthorized {
		t.Fatal("fixture must reject original actor")
	}
	policy, err := contract.NewPolicy("project", "policy-2", other)
	if err != nil {
		t.Fatal(err)
	}
	state := actual.inspect()
	fence := AuthorityFence{ProjectID: "project", SuiteID: "suite", Revision: state.canonical.Suite().Revision()}
	if err := actual.updateAuthority(context.Background(), fence, func(s *referenceState) error { s.policy = policy; return nil }); err != nil {
		t.Fatal(err)
	}
	before := actual.inspect()
	replayed := processConsentForTest(t, actual, command)
	if !replayed.Duplicate || replayed.Receipt != original.Receipt || !reflect.DeepEqual(before, actual.inspect()) {
		t.Fatal("replay reevaluated an immutable rejected receipt")
	}
}

func TestProcessConsentAliasFailureCanRetry(t *testing.T) {
	f, store := approvedStoreFixture(t)
	before := store.inspect()
	alias := changedConsentCommand(t, f.command, func(c *contract.CommandInput) { c.OperationID = "alias" })
	store.failAt = "alias"
	response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: alias})
	if !errors.Is(err, errReferenceFailure) || response != (ConsentResponse{}) || !reflect.DeepEqual(before, store.inspect()) {
		t.Fatalf("alias failure = %+v, %v", response, err)
	}
	store.failAt = ""
	response = processConsentForTest(t, store, alias)
	if !response.Duplicate || len(store.inspect().acknowledgments) != 1 {
		t.Fatal("alias retry duplicated original acknowledgment")
	}
}

func TestProcessConsentConflictsBeforeWriting(t *testing.T) {
	for name, change := range map[string]func(*contract.CommandInput){
		"operation reused for another source": func(c *contract.CommandInput) { c.SourceCommandID = "different-source" },
		"source reused by another actor": func(c *contract.CommandInput) {
			c.OperationID = "other-op"
			c.Actor, _ = contract.NewPrincipal("someone-else", contract.Human)
		},
		"source reused by another actor kind": func(c *contract.CommandInput) {
			c.OperationID = "other-op"
			c.Actor, _ = contract.NewPrincipal("owner", contract.Agent)
		},
	} {
		t.Run(name, func(t *testing.T) {
			f, actual := approvedStoreFixture(t)
			before := actual.inspect()
			store := &consentStoreProbe{Store: actual}
			command := changedConsentCommand(t, f.command, change)
			for range 2 {
				response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: command})
				if !errors.Is(err, ErrOperationConflict) || response != (ConsentResponse{}) || store.commits != 0 || !reflect.DeepEqual(before, actual.inspect()) {
					t.Fatalf("collision = %+v, %v, commits=%d", response, err, store.commits)
				}
			}
			processConsentForTest(t, actual, f.command)
		})
	}
	t.Run("promotion owns operation", func(t *testing.T) {
		f, actual := approvedStoreFixture(t)
		if err := actual.CommitPromotion(context.Background(), f.snapshot.Fence, f.promotionWrite(t)); err != nil {
			t.Fatal(err)
		}
		before := actual.inspect()
		store := &consentStoreProbe{Store: actual}
		command := changedConsentCommand(t, f.command, func(c *contract.CommandInput) {
			c.OperationID = f.request.OperationID
			c.SourceCommandID = "another-source"
		})
		response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: command})
		if !errors.Is(err, ErrOperationConflict) || response != (ConsentResponse{}) || store.commits != 0 || !reflect.DeepEqual(before, actual.inspect()) {
			t.Fatalf("cross-kind collision = %+v, %v, commits=%d", response, err, store.commits)
		}
	})
}

func TestProcessConsentRejectsMalformedReceipt(t *testing.T) {
	for name, mutate := range map[string]func(*Snapshot){
		"missing source":             func(s *Snapshot) { s.Source = OperationReceipt{} },
		"absent kind with payload":   func(s *Snapshot) { s.Source.Kind = 0 },
		"unknown kind":               func(s *Snapshot) { s.Source.Kind = 99 },
		"zero result":                func(s *Snapshot) { s.Source.Consent.Result = contract.CommandResult{} },
		"missing evaluated revision": func(s *Snapshot) { s.Source.Consent.EvaluatedReference.RevisionID = ""; s.Operation = s.Source },
		"foreign evaluated proposal": func(s *Snapshot) { s.Source.Consent.EvaluatedReference.ProposalID = "other"; s.Operation = s.Source },
		"missing policy revision":    func(s *Snapshot) { s.Source.Consent.PolicyRevisionID = ""; s.Operation = s.Source },
		"mixed payloads":             func(s *Snapshot) { s.Source.Promotion.Identity.Kind = OperationPromote; s.Operation = s.Source },
		"disagreeing indexes":        func(s *Snapshot) { s.Operation.Consent.CurrentApprovalEligible = false },
	} {
		t.Run(name, func(t *testing.T) {
			f, actual := approvedStoreFixture(t)
			before := actual.inspect()
			store := &consentStoreProbe{Store: actual, changeSnapshot: mutate}
			response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command})
			if !errors.Is(err, ErrInvalidSnapshot) || response != (ConsentResponse{}) || store.commits != 0 || !reflect.DeepEqual(before, actual.inspect()) {
				t.Fatalf("malformed receipt = %+v, %v, commits=%d", response, err, store.commits)
			}
		})
	}
}

func TestProcessConsentHistoricalReplayNeedsNoCurrentAuthority(t *testing.T) {
	f, actual := approvedStoreFixture(t)
	original := actual.inspect().sources[f.command.SourceCommandID()].Consent
	store := &consentStoreProbe{Store: actual, changeSnapshot: func(s *Snapshot) {
		s.Canonical = contract.CanonicalSnapshot{}
		s.Proposal = contract.Proposal{}
		s.Policy = contract.Policy{}
		s.Consent = contract.Consent{}
	}}
	response := processConsentForTest(t, store, f.command)
	if !response.Duplicate || response.Receipt != original || store.commits != 0 {
		t.Fatal("historical replay reevaluated absent current authority")
	}
	alias := changedConsentCommand(t, f.command, func(c *contract.CommandInput) { c.OperationID = "alias" })
	response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: alias})
	if !errors.Is(err, ErrInvalidSnapshot) || response != (ConsentResponse{}) || store.commits != 0 {
		t.Fatalf("alias without current aggregate = %+v, %v", response, err)
	}
}

func consentRevisionForTest(t *testing.T, protected contract.ProtectedContract, reference contract.ProposalReference, baseline contract.SuiteVersionID, carrier contract.ApprovalCarrierID) contract.ProposalRevision {
	t.Helper()
	binding, err := contract.NewApprovalBinding(contract.BindingInput{Reference: reference, ExpectedCanonical: baseline, Manifest: protected.Manifest().Digest(), Scope: protected.ScopeDigest(), PolicyRevision: "policy-1", CoveredInputs: protected.CoveredInputs()})
	if err != nil {
		t.Fatal(err)
	}
	revision, err := contract.NewProposalRevision(binding, "candidate-2", carrier)
	return mustValue(t, revision, err)
}

func TestProcessConsentAcknowledgesExactHistoricalPromotion(t *testing.T) {
	f, store := approvedStoreFixture(t)
	promoted, err := Promote(context.Background(), store, f.request)
	if err != nil || !promoted.Committed {
		t.Fatalf("initial promotion = %+v, %v", promoted, err)
	}

	// A newer revision in the original proposal has independent current consent.
	currentReference := f.command.Reference()
	currentReference.RevisionID = "revision-2"
	currentProposal, err := f.snapshot.Proposal.Revise(consentRevisionForTest(t, f.proposed, currentReference, "version-1", f.command.Carrier()))
	if err != nil {
		t.Fatal(err)
	}
	state := store.inspect()
	fence := AuthorityFence{ProjectID: "project", SuiteID: "suite", Revision: state.canonical.Suite().Revision()}
	if err := store.updateAuthority(context.Background(), fence, func(s *referenceState) error { s.proposals[currentReference.ProposalID] = currentProposal; return nil }); err != nil {
		t.Fatal(err)
	}
	currentCommand := changedConsentCommand(t, f.command, func(c *contract.CommandInput) {
		c.OperationID = "approve-current"
		c.SourceCommandID = "comment-current"
		c.Reference = currentReference
		c.Order = 2
	})
	processConsentForTest(t, store, currentCommand)

	// A separate, fully approved proposal promotes another canonical version.
	manifest, err := artifact.NewManifest([]artifact.Entry{{Path: "tests/contract.txt", Content: artifact.Hash([]byte("updated test"))}})
	if err != nil {
		t.Fatal(err)
	}
	protected, err := contract.NewProtectedContract(manifest, f.proposed.ScopeDigest(), f.proposed.CoveredInputs())
	if err != nil {
		t.Fatal(err)
	}
	laterReference := contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "later-proposal", RevisionID: "later-revision"}
	laterProposal, err := contract.NewProposal(consentRevisionForTest(t, protected, laterReference, "version-1", "later-carrier"))
	if err != nil {
		t.Fatal(err)
	}
	laterConsent, err := contract.NewConsent("project", "suite", laterReference.ProposalID)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := contract.NewIntegrityEvidence("verifier", "later-merged", laterProposal.Current().Binding(), contract.IntegrityPassed)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := contract.AssessIntegrity("later-merged", laterProposal.Current().Binding(), &evidence)
	if err != nil {
		t.Fatal(err)
	}
	fence.Revision = store.inspect().canonical.Suite().Revision()
	if err := store.updateAuthority(context.Background(), fence, func(s *referenceState) error {
		s.proposals[laterReference.ProposalID] = laterProposal
		s.consents[laterReference.ProposalID] = laterConsent
		s.assessments[assessmentKey{laterReference, "later-merged"}] = assessment
		var err error
		s.scheduling, err = s.scheduling.Admit(laterProposal, true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	laterCommand := changedConsentCommand(t, f.command, func(c *contract.CommandInput) {
		c.OperationID = "approve-later"
		c.SourceCommandID = "comment-later"
		c.Reference = laterReference
		c.Carrier = "later-carrier"
	})
	processConsentForTest(t, store, laterCommand)
	integration, err := contract.NewIntegration("project", "default", "later-merged", "later-carrier", contract.IntegrationMergedChange)
	if err != nil {
		t.Fatal(err)
	}
	laterRequest := PromoteRequest{OperationID: "promote-later", Reference: laterReference, Carrier: "later-carrier", Proposed: protected, AssessmentSource: "later-merged", Integration: integration, NewVersionID: "version-2", RecordedAt: f.request.RecordedAt}
	promoted, err = Promote(context.Background(), store, laterRequest)
	if err != nil || !promoted.Committed {
		t.Fatalf("later promotion = %+v, %v", promoted, err)
	}
	before := store.inspect()

	revoke := changedConsentCommand(t, f.command, func(c *contract.CommandInput) {
		c.OperationID = "revoke-history"
		c.SourceCommandID = "comment-revoke-history"
		c.Action = contract.RevokeConsent
		c.Order = 3
	})
	response := processConsentForTest(t, store, revoke)
	if response.Receipt.Result.Outcome() != contract.ConsentRevoked || response.Receipt.Result.Command().Reference() != f.command.Reference() || response.Receipt.PromotedVersionID != "version-1" || response.Receipt.EvaluatedReference != currentReference || !response.Receipt.CurrentApprovalEligible {
		t.Fatalf("historical acknowledgment = %+v", response.Receipt)
	}
	after := store.inspect()
	if after.canonical.Version().ID() != "version-2" || !reflect.DeepEqual(before.versions, after.versions) || !reflect.DeepEqual(before.promotions, after.promotions) || !reflect.DeepEqual(before.scheduling, after.scheduling) || !reflect.DeepEqual(before.publications, after.publications) {
		t.Fatal("revocation changed canonical history or scheduling")
	}
	if after.acknowledgments[len(after.acknowledgments)-1] != response.Receipt {
		t.Fatal("historical acknowledgment was not committed")
	}
	noOp := changedConsentCommand(t, revoke, func(c *contract.CommandInput) {
		c.OperationID = "noop-history"
		c.SourceCommandID = "comment-noop-history"
		c.Order++
	})
	response = processConsentForTest(t, store, noOp)
	if response.Receipt.Result.Outcome() != contract.ConsentNoActiveApproval || response.Receipt.PromotedVersionID != "version-1" || !response.Receipt.CurrentApprovalEligible {
		t.Fatal("no-op historical revoke lost current or historical facts")
	}
}

func TestProcessConsentRejectsWrongHistoricalPromotion(t *testing.T) {
	for _, changed := range []string{"reference", "binding", "carrier"} {
		t.Run(changed, func(t *testing.T) {
			f, actual := approvedStoreFixture(t)
			promoted, err := Promote(context.Background(), actual, f.request)
			if err != nil || !promoted.Committed {
				t.Fatal(err)
			}
			before := actual.inspect()
			record := before.canonical.Record()
			binding, carrier := record.Binding(), record.Carrier()
			if changed == "carrier" {
				carrier = "wrong-carrier"
			} else {
				ref := binding.Reference()
				baseline := binding.ExpectedCanonical()
				if changed == "reference" {
					ref.RevisionID = "different"
				} else {
					baseline = "different-baseline"
				}
				binding = consentRevisionForTest(t, f.proposed, ref, baseline, carrier).Binding()
			}
			wrong, err := contract.NewPromotionRecord(contract.PromotionRecordInput{OperationID: record.OperationID(), VersionID: record.VersionID(), Binding: binding, Carrier: carrier, Source: record.Source(), Target: record.Target(), RecordedAt: record.RecordedAt()})
			if err != nil {
				t.Fatal(err)
			}
			store := &consentStoreProbe{Store: actual, changeSnapshot: func(s *Snapshot) { s.HistoricalPromotion = wrong }}
			revoke := changedConsentCommand(t, f.command, func(c *contract.CommandInput) {
				c.OperationID = "revoke"
				c.SourceCommandID = "revoke-comment"
				c.Action = contract.RevokeConsent
				c.Order = 2
			})
			response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: revoke})
			if !errors.Is(err, ErrInvalidSnapshot) || response != (ConsentResponse{}) || store.commits != 0 || !reflect.DeepEqual(before, actual.inspect()) {
				t.Fatalf("wrong history = %+v, %v, commits=%d", response, err, store.commits)
			}
		})
	}
}

func TestProcessConsentGlobalScopeConflicts(t *testing.T) {
	for _, field := range []string{"project", "suite", "proposal"} {
		for _, alias := range []bool{false, true} {
			t.Run(field+map[bool]string{false: " operation", true: " source alias"}[alias], func(t *testing.T) {
				f, actual := approvedStoreFixture(t)
				before := actual.inspect()
				store := &consentStoreProbe{Store: actual}
				command := changedConsentCommand(t, f.command, func(c *contract.CommandInput) {
					if alias {
						c.OperationID = "new-alias"
					}
					switch field {
					case "project":
						c.Reference.ProjectID = "foreign"
					case "suite":
						c.Reference.SuiteID = "foreign"
					case "proposal":
						c.Reference.ProposalID = "absent"
					}
				})
				response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: command})
				if !errors.Is(err, ErrOperationConflict) || response != (ConsentResponse{}) || store.commits != 0 || !reflect.DeepEqual(before, actual.inspect()) {
					t.Fatalf("foreign identity = %+v, %v", response, err)
				}
			})
		}
	}
	f := newStoreFixture(t)
	store := newReferenceStore(t, f.snapshot)
	missing := changedConsentCommand(t, f.command, func(c *contract.CommandInput) { c.Reference.ProposalID = "missing" })
	response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: missing})
	if !errors.Is(err, ErrNotFound) || response != (ConsentResponse{}) || len(store.inspect().acknowledgments) != 0 {
		t.Fatalf("unknown aggregate = %+v, %v", response, err)
	}
}

func TestProcessConsentOrderedTerminalOutcomes(t *testing.T) {
	for _, initiallyApproved := range []bool{false, true} {
		t.Run(map[bool]string{false: "revocation tombstone", true: "active revocation"}[initiallyApproved], func(t *testing.T) {
			f := newStoreFixture(t)
			store := newReferenceStore(t, f.snapshot)
			if initiallyApproved {
				processConsentForTest(t, store, f.command)
			}
			steps := []struct {
				id      string
				action  contract.ConsentAction
				order   contract.CommandOrder
				outcome contract.ConsentOutcome
				reason  contract.ConsentReason
				active  bool
			}{
				{"revoke", contract.RevokeConsent, 20, contract.ConsentNoActiveApproval, contract.ConsentReasonNone, false},
				{"delayed", contract.ApproveConsent, 15, contract.ConsentRejected, contract.ConsentReasonObsoleteCommand, false},
				{"equal", contract.ApproveConsent, 20, contract.ConsentRejected, contract.ConsentReasonObsoleteCommand, false},
				{"fresh", contract.ApproveConsent, 21, contract.ConsentApproved, contract.ConsentReasonNone, true},
			}
			if initiallyApproved {
				steps[0].outcome = contract.ConsentRevoked
			}
			for _, step := range steps {
				before := store.inspect()
				command := changedConsentCommand(t, f.command, func(c *contract.CommandInput) {
					c.OperationID = contract.OperationID(step.id)
					c.SourceCommandID = contract.SourceCommandID(step.id)
					c.Action = step.action
					c.Order = step.order
				})
				response := processConsentForTest(t, store, command)
				after := store.inspect()
				if response.Duplicate || response.Receipt.Result.Outcome() != step.outcome || response.Receipt.Result.Reason() != step.reason || response.Receipt.CurrentApprovalEligible != step.active || len(after.acknowledgments) != len(before.acknowledgments)+1 || after.canonical.Suite().Revision() != before.canonical.Suite().Revision()+1 {
					t.Fatalf("%s = %+v", step.id, response)
				}
			}
			before := store.inspect()
			agent, err := contract.NewPrincipal("owner", contract.Agent)
			if err != nil {
				t.Fatal(err)
			}
			unauthorized := changedConsentCommand(t, f.command, func(c *contract.CommandInput) {
				c.OperationID = "unauthorized-revoke"
				c.SourceCommandID = "unauthorized-revoke"
				c.Actor = agent
				c.Action = contract.RevokeConsent
				c.Order = 100
			})
			response := processConsentForTest(t, store, unauthorized)
			if response.Receipt.Result.Reason() != contract.ConsentReasonUnauthorized || !response.Receipt.CurrentApprovalEligible || len(store.inspect().acknowledgments) != len(before.acknowledgments)+1 {
				t.Fatal("unauthorized revoke affected current owner consent or lost rejection receipt")
			}
		})
	}
}

func TestProcessConsentRecordsScopedRejections(t *testing.T) {
	for _, kind := range []string{"unknown revision", "superseded revision", "carrier mismatch", "policy mismatch"} {
		t.Run(kind, func(t *testing.T) {
			f := newStoreFixture(t)
			command := f.command
			want := contract.ConsentReasonUnknownRevision
			switch kind {
			case "unknown revision":
				command = changedConsentCommand(t, command, func(c *contract.CommandInput) { c.Reference.RevisionID = "unknown" })
			case "superseded revision":
				ref := f.command.Reference()
				ref.RevisionID = "revision-2"
				proposal, err := f.snapshot.Proposal.Revise(consentRevisionForTest(t, f.proposed, ref, "", f.command.Carrier()))
				if err != nil {
					t.Fatal(err)
				}
				f.snapshot.Proposal = proposal
				want = contract.ConsentReasonSupersededRevision
			case "carrier mismatch":
				command = changedConsentCommand(t, command, func(c *contract.CommandInput) { c.Carrier = "wrong" })
				want = contract.ConsentReasonContextMismatch
			case "policy mismatch":
				policy, err := contract.NewPolicy("project", "policy-2", f.owner)
				if err != nil {
					t.Fatal(err)
				}
				f.snapshot.Policy = policy
				want = contract.ConsentReasonPolicyMismatch
			}
			store := newReferenceStore(t, f.snapshot)
			response := processConsentForTest(t, store, command)
			if response.Receipt.Result.Outcome() != contract.ConsentRejected || response.Receipt.Result.Reason() != want || response.Receipt.CurrentApprovalEligible || len(store.inspect().acknowledgments) != 1 {
				t.Fatalf("rejection = %+v", response)
			}
			replay := processConsentForTest(t, store, command)
			if !replay.Duplicate || replay.Receipt != response.Receipt || len(store.inspect().acknowledgments) != 1 {
				t.Fatal("terminal rejection failed original replay")
			}
		})
	}
}

func TestProcessConsentCannotCommitStaleAuthority(t *testing.T) {
	t.Run("policy changes after load", func(t *testing.T) {
		f := newStoreFixture(t)
		actual := newReferenceStore(t, f.snapshot)
		policy, err := contract.NewPolicy("project", "policy-2", f.owner)
		if err != nil {
			t.Fatal(err)
		}
		store := &consentStoreProbe{Store: actual, changeSnapshot: func(s *Snapshot) {
			if err := actual.updateAuthority(context.Background(), s.Fence, func(next *referenceState) error { next.policy = policy; return nil }); err != nil {
				t.Fatal(err)
			}
		}}
		response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: f.command})
		after := actual.inspect()
		if !errors.Is(err, ErrAuthorityConflict) || response != (ConsentResponse{}) || len(after.operations) != 0 || len(after.acknowledgments) != 0 || after.consents[f.command.Reference().ProposalID].HasApproval(f.snapshot.Proposal, f.snapshot.Policy) {
			t.Fatalf("stale consent = %+v, %v", response, err)
		}
		// An explicit retry uses the new policy and records its real rejection.
		retry := processConsentForTest(t, actual, f.command)
		if retry.Receipt.Result.Reason() != contract.ConsentReasonPolicyMismatch || retry.Receipt.PolicyRevisionID != policy.RevisionID() {
			t.Fatal("retry silently reused stale policy")
		}
	})
}

func TestProcessConsentRejectedCarrierStillAcknowledgesHistory(t *testing.T) {
	f, store := approvedStoreFixture(t)
	promoted, err := Promote(context.Background(), store, f.request)
	if err != nil || !promoted.Committed {
		t.Fatal(err)
	}
	wrongCarrier := changedConsentCommand(t, f.command, func(c *contract.CommandInput) {
		c.OperationID = "wrong-carrier"
		c.SourceCommandID = "wrong-carrier"
		c.Carrier = "wrong"
		c.Action = contract.RevokeConsent
		c.Order = 2
	})
	response := processConsentForTest(t, store, wrongCarrier)
	if response.Receipt.Result.Reason() != contract.ConsentReasonContextMismatch || response.Receipt.PromotedVersionID != f.request.NewVersionID || !response.Receipt.CurrentApprovalEligible {
		t.Fatalf("wrong carrier conflated command and historical context: %+v", response)
	}
}

func TestProcessConsentRejectsInconsistentStoredHistories(t *testing.T) {
	for _, corruption := range []string{"source outcome differs from aggregate", "source absent from aggregate history", "malformed promotion operation", "promotion stored as source command"} {
		t.Run(corruption, func(t *testing.T) {
			f, actual := approvedStoreFixture(t)
			before := actual.inspect()
			alias := changedConsentCommand(t, f.command, func(c *contract.CommandInput) { c.OperationID = "alias" })
			emptyConsent, err := contract.NewConsent("project", "suite", f.command.Reference().ProposalID)
			if err != nil {
				t.Fatal(err)
			}
			observedRevoke := changedConsentCommand(t, f.command, func(c *contract.CommandInput) { c.Action = contract.RevokeConsent })
			_, revokedResult, err := emptyConsent.Apply(f.snapshot.Proposal, f.snapshot.Policy, observedRevoke)
			if err != nil || revokedResult.Outcome() != contract.ConsentNoActiveApproval {
				t.Fatalf("alternate original outcome fixture: %+v, %v", revokedResult, err)
			}
			promotion := OperationReceipt{Kind: OperationPromote, Promotion: f.promotionWrite(t).Receipt}
			store := &consentStoreProbe{Store: actual, changeSnapshot: func(s *Snapshot) {
				switch corruption {
				case "source outcome differs from aggregate":
					// Both original results are well-formed, but they cannot describe
					// the same immutable source observation in one stored snapshot.
					s.Source.Consent.Result = revokedResult
					s.Source.Consent.CurrentApprovalEligible = false
				case "source absent from aggregate history":
					s.Consent = emptyConsent
				case "malformed promotion operation":
					s.Operation = promotion
					s.Operation.Promotion.Decision = contract.PromotionDecision{}
				case "promotion stored as source command":
					s.Source = promotion
				}
			}}
			response, err := ProcessConsent(context.Background(), store, ConsentRequest{Command: alias})
			if !errors.Is(err, ErrInvalidSnapshot) || response != (ConsentResponse{}) || store.commits != 0 || !reflect.DeepEqual(before, actual.inspect()) {
				t.Fatalf("inconsistent stored history = %+v, %v, commits=%d", response, err, store.commits)
			}
		})
	}
}
