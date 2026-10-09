package contract_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func promotionManifest(t *testing.T, contents ...string) artifact.Manifest {
	t.Helper()
	entries := make([]artifact.Entry, len(contents))
	for i, content := range contents {
		entries[i] = artifact.Entry{Path: string(rune('a'+i)) + "_test.go", Content: artifact.Hash([]byte(content))}
	}
	manifest, err := artifact.NewManifest(entries)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func promotionProtected(t *testing.T, manifest artifact.Manifest, scope string, inputs map[string]string) contract.ProtectedContract {
	t.Helper()
	protected, err := contract.NewProtectedContract(manifest, artifact.Hash([]byte(scope)), inputs)
	if err != nil {
		t.Fatal(err)
	}
	return protected
}

func TestProtectedContractPreservesExactImmutableInputs(t *testing.T) {
	manifest := promotionManifest(t, "test")
	inputs := map[string]string{" runner ": " version-1 "}
	protected := promotionProtected(t, manifest, "scope", inputs)
	if protected.IsZero() || protected.Manifest().Digest() != manifest.Digest() || protected.ScopeDigest() != artifact.Hash([]byte("scope")) || !reflect.DeepEqual(protected.CoveredInputs(), inputs) {
		t.Fatal("constructed protected contract lost exact manifest, scope or context")
	}
	inputs[" runner "] = "changed"
	returned := protected.CoveredInputs()
	returned[" runner "] = "changed again"
	if protected.CoveredInputs()[" runner "] != " version-1 " {
		t.Fatal("caller map mutated protected context")
	}
	entries := protected.Manifest().Entries()
	entries[0].Path = "changed"
	if protected.Manifest().Entries()[0].Path != "a_test.go" {
		t.Fatal("returned manifest mutated protected inventory")
	}
	equal := promotionProtected(t, manifest, "scope", map[string]string{" runner ": " version-1 "})
	if !protected.Equal(equal) || !equal.Equal(protected) {
		t.Fatal("equal exact protected contracts differ")
	}
	for name, other := range map[string]contract.ProtectedContract{
		"absent":          {},
		"manifest":        promotionProtected(t, promotionManifest(t, "changed"), "scope", map[string]string{" runner ": " version-1 "}),
		"scope":           promotionProtected(t, manifest, "other", map[string]string{" runner ": " version-1 "}),
		"context":         promotionProtected(t, manifest, "scope", map[string]string{" runner ": "version-1"}),
		"context removal": promotionProtected(t, manifest, "scope", nil),
	} {
		if protected.Equal(other) || other.Equal(protected) {
			t.Errorf("%s compared equal", name)
		}
	}
	if (contract.ProtectedContract{}).Equal(contract.ProtectedContract{}) {
		t.Fatal("absent contracts compared equal")
	}
	empty := promotionProtected(t, promotionManifest(t), "scope", nil)
	emptyMap := promotionProtected(t, promotionManifest(t), "scope", map[string]string{})
	if empty.IsZero() || !empty.Equal(emptyMap) {
		t.Fatal("constructed empty inventory or nil context equality lost")
	}
}

func TestProtectedContractRejectsMissingInputs(t *testing.T) {
	manifest := promotionManifest(t, "test")
	scope := artifact.Hash([]byte("scope"))
	for _, test := range []struct {
		name     string
		manifest artifact.Manifest
		scope    artifact.Digest
		inputs   map[string]string
	}{
		{"manifest", artifact.Manifest{}, scope, nil},
		{"scope", manifest, artifact.Digest{}, nil},
		{"empty key", manifest, scope, map[string]string{"": "v"}},
		{"blank key", manifest, scope, map[string]string{" \t": "v"}},
		{"empty value", manifest, scope, map[string]string{"key": ""}},
		{"blank value", manifest, scope, map[string]string{"key": "\n "}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := contract.NewProtectedContract(test.manifest, test.scope, test.inputs)
			if !errors.Is(err, contract.ErrInvalidProtectedContract) || !got.IsZero() {
				t.Fatalf("invalid protected input accepted: value=%v err=%v", got, err)
			}
		})
	}
}

func promotionSuite(t *testing.T, project contract.ProjectID, suite contract.SuiteID, version contract.SuiteVersionID, revision contract.StateRevision) contract.Suite {
	t.Helper()
	value, err := contract.NewSuite(project, suite, version, revision)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func promotionVersion(t *testing.T, project contract.ProjectID, suite contract.SuiteID, version contract.SuiteVersionID, manifest artifact.Manifest) contract.SuiteVersion {
	t.Helper()
	value, err := contract.NewSuiteVersion(project, suite, version, manifest)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func promotionCanonicalParts(t *testing.T) (contract.Suite, contract.SuiteVersion, contract.ProtectedContract, contract.PromotionRecord) {
	t.Helper()
	protected := promotionProtected(t, promotionManifest(t, "old"), "scope", map[string]string{"runner": "v1"})
	return promotionSuite(t, "project", "suite", "v1", 10), promotionVersion(t, "project", "suite", "v1", protected.Manifest()), protected, promotionRecord(t, promotionRecordInput(t))
}

func promotionCanonical(t *testing.T) contract.CanonicalSnapshot {
	t.Helper()
	suite, version, protected, record := promotionCanonicalParts(t)
	value, err := contract.NewCanonicalSnapshot(suite, version, protected, record)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func promotionAbsentCanonical(t *testing.T) contract.CanonicalSnapshot {
	t.Helper()
	value, err := contract.NewCanonicalSnapshot(promotionSuite(t, "project", "suite", "", 10), contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCanonicalSnapshotPreservesCompleteCurrentContract(t *testing.T) {
	current := promotionCanonical(t)
	suite, version, protected, record := promotionCanonicalParts(t)
	if current.IsZero() || current.Suite() != suite || current.Version().ID() != version.ID() || !current.Contract().Equal(protected) || current.Record().OperationID() != record.OperationID() || !current.Record().Binding().Equal(record.Binding()) {
		t.Fatal("canonical snapshot lost pointer, version, protected context or promotion provenance")
	}
	absent := promotionAbsentCanonical(t)
	if absent.IsZero() || absent.Suite().ID() != "suite" || absent.Version().ID() != "" || !absent.Contract().IsZero() || !absent.Record().IsZero() {
		t.Fatal("valid absent canonical was lost")
	}
	if !(contract.CanonicalSnapshot{}).IsZero() {
		t.Fatal("zero snapshot became a valid absent canonical")
	}
	context := current.Contract().CoveredInputs()
	context["runner"] = "changed"
	if current.Contract().CoveredInputs()["runner"] != "v1" {
		t.Fatal("snapshot protected context mutated")
	}
}

func TestCanonicalSnapshotRejectsPartialOrContradictoryFacts(t *testing.T) {
	suite, version, protected, record := promotionCanonicalParts(t)
	absent := promotionSuite(t, "project", "suite", "", 10)
	for _, test := range []struct {
		name      string
		suite     contract.Suite
		version   contract.SuiteVersion
		protected contract.ProtectedContract
		record    contract.PromotionRecord
	}{
		{"zero suite", contract.Suite{}, version, protected, record},
		{"zero all", contract.Suite{}, contract.SuiteVersion{}, contract.ProtectedContract{}, contract.PromotionRecord{}},
		{"pointer without version", suite, contract.SuiteVersion{}, protected, record},
		{"pointer without protected", suite, version, contract.ProtectedContract{}, record},
		{"pointer without record", suite, version, protected, contract.PromotionRecord{}},
		{"absent pointer with version", absent, version, contract.ProtectedContract{}, contract.PromotionRecord{}},
		{"absent pointer with protected", absent, contract.SuiteVersion{}, protected, contract.PromotionRecord{}},
		{"absent pointer with record", absent, contract.SuiteVersion{}, contract.ProtectedContract{}, record},
		{"version project", suite, promotionVersion(t, "other", "suite", "v1", protected.Manifest()), protected, record},
		{"version suite", suite, promotionVersion(t, "project", "other", "v1", protected.Manifest()), protected, record},
		{"version identity", suite, promotionVersion(t, "project", "suite", "other", protected.Manifest()), protected, record},
		{"version content", suite, promotionVersion(t, "project", "suite", "v1", promotionManifest(t, "changed")), protected, record},
		{"protected scope", suite, version, promotionProtected(t, protected.Manifest(), "changed", protected.CoveredInputs()), record},
		{"protected context", suite, version, promotionProtected(t, protected.Manifest(), "scope", nil), record},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := contract.NewCanonicalSnapshot(test.suite, test.version, test.protected, test.record)
			if !errors.Is(err, contract.ErrInvalidCanonicalSnapshot) || !got.IsZero() {
				t.Fatalf("contradictory canonical facts accepted: err=%v", err)
			}
		})
	}
	for name, change := range map[string]func(*contract.PromotionRecordInput, *contract.BindingInput){
		"record version": func(r *contract.PromotionRecordInput, b *contract.BindingInput) { r.VersionID = "other" },
		"record project": func(r *contract.PromotionRecordInput, b *contract.BindingInput) { b.Reference.ProjectID = "other" },
		"record suite":   func(r *contract.PromotionRecordInput, b *contract.BindingInput) { b.Reference.SuiteID = "other" },
		"record manifest": func(r *contract.PromotionRecordInput, b *contract.BindingInput) {
			b.Manifest = promotionManifest(t, "different").Digest()
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := promotionRecordInput(t)
			b := input.Binding
			binding := contract.BindingInput{Reference: b.Reference(), ExpectedCanonical: b.ExpectedCanonical(), Manifest: b.ManifestDigest(), Scope: b.ScopeDigest(), PolicyRevision: b.PolicyRevisionID(), CoveredInputs: b.CoveredInputs()}
			change(&input, &binding)
			var err error
			input.Binding, err = contract.NewApprovalBinding(binding)
			if err != nil {
				t.Fatal(err)
			}
			got, err := contract.NewCanonicalSnapshot(suite, version, protected, promotionRecord(t, input))
			if !errors.Is(err, contract.ErrInvalidCanonicalSnapshot) || !got.IsZero() {
				t.Fatalf("inconsistent record accepted: %v", err)
			}
		})
	}
}

func TestCanonicalContractClassificationUsesAllProtectedInputs(t *testing.T) {
	current := promotionCanonical(t)
	protected := promotionProtected(t, promotionManifest(t, "old"), "scope", map[string]string{"runner": "v1"})
	for _, test := range []struct {
		name     string
		current  contract.CanonicalSnapshot
		proposed contract.ProtectedContract
		want     contract.ContractChange
	}{
		{"same contract", current, protected, contract.ContractUnchanged},
		{"manifest changed", current, promotionProtected(t, promotionManifest(t, "changed"), "scope", map[string]string{"runner": "v1"}), contract.ContractChanged},
		{"scope changed", current, promotionProtected(t, protected.Manifest(), "changed", map[string]string{"runner": "v1"}), contract.ContractChanged},
		{"context changed", current, promotionProtected(t, protected.Manifest(), "scope", map[string]string{"runner": "v2"}), contract.ContractChanged},
		{"context removed", current, promotionProtected(t, protected.Manifest(), "scope", nil), contract.ContractChanged},
		{"no canonical", promotionAbsentCanonical(t), protected, contract.ContractChanged},
		{"empty initial inventory", promotionAbsentCanonical(t), promotionProtected(t, promotionManifest(t), "scope", nil), contract.ContractChanged},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := contract.ClassifyContractChange(test.current, test.proposed)
			if err != nil || got != test.want {
				t.Fatalf("change=%v err=%v, want %v", got, err, test.want)
			}
		})
	}
	if _, err := contract.ClassifyContractChange(contract.CanonicalSnapshot{}, protected); !errors.Is(err, contract.ErrInvalidCanonicalSnapshot) {
		t.Fatalf("absent snapshot accepted: %v", err)
	}
	if _, err := contract.ClassifyContractChange(current, contract.ProtectedContract{}); !errors.Is(err, contract.ErrInvalidProtectedContract) {
		t.Fatalf("absent proposed contract accepted: %v", err)
	}
}

func promotionIntegration(t *testing.T, source contract.SourceRevision, carrier contract.ApprovalCarrierID, kind contract.IntegrationKind) contract.Integration {
	t.Helper()
	value, err := contract.NewIntegration("project", "main", source, carrier, kind)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestIntegrationPreservesExactTrustedObservations(t *testing.T) {
	merged, err := contract.NewIntegration(" project ", " main ", " source ", " carrier ", contract.IntegrationMergedChange)
	if err != nil {
		t.Fatal(err)
	}
	if merged.IsZero() || merged.ProjectID() != " project " || merged.Target() != " main " || merged.Source() != " source " || merged.Carrier() != " carrier " || merged.Kind() != contract.IntegrationMergedChange {
		t.Fatal("merged observation lost exact supplied facts")
	}
	baseline := promotionIntegration(t, "baseline", "", contract.IntegrationExistingBaseline)
	if baseline.IsZero() || baseline.Carrier() != "" || baseline.Source() != "baseline" || baseline.Kind() != contract.IntegrationExistingBaseline {
		t.Fatal("baseline observation invented an approval carrier")
	}
	if !(contract.Integration{}).IsZero() {
		t.Fatal("absent integration became confirmation")
	}
}

func TestIntegrationRejectsIncompleteObservations(t *testing.T) {
	for _, test := range []struct {
		name    string
		project contract.ProjectID
		target  contract.IntegrationTargetID
		source  contract.SourceRevision
		carrier contract.ApprovalCarrierID
		kind    contract.IntegrationKind
	}{
		{"project absent", "", "main", "source", "carrier", contract.IntegrationMergedChange},
		{"project blank", " \t", "main", "source", "carrier", contract.IntegrationMergedChange},
		{"target absent", "project", "", "source", "carrier", contract.IntegrationMergedChange},
		{"target blank", "project", " ", "source", "carrier", contract.IntegrationMergedChange},
		{"source absent", "project", "main", "", "carrier", contract.IntegrationMergedChange},
		{"source blank", "project", "main", "\n", "carrier", contract.IntegrationMergedChange},
		{"merged carrier absent", "project", "main", "source", "", contract.IntegrationMergedChange},
		{"merged carrier blank", "project", "main", "source", "\t", contract.IntegrationMergedChange},
		{"baseline carrier", "project", "main", "source", "carrier", contract.IntegrationExistingBaseline},
		{"baseline blank carrier", "project", "main", "source", " ", contract.IntegrationExistingBaseline},
		{"kind absent", "project", "main", "source", "carrier", 0},
		{"kind unknown", "project", "main", "source", "carrier", 99},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := contract.NewIntegration(test.project, test.target, test.source, test.carrier, test.kind)
			if !errors.Is(err, contract.ErrInvalidIntegration) || !got.IsZero() {
				t.Fatalf("invalid integration accepted: %v", err)
			}
		})
	}
}

func promotionPolicy(t *testing.T, project contract.ProjectID, revision contract.PolicyRevisionID, owner contract.PrincipalID) contract.Policy {
	t.Helper()
	principal, err := contract.NewPrincipal(owner, contract.Human)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := contract.NewPolicy(project, revision, principal)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func promotionBinding(t *testing.T, input contract.BindingInput) contract.ApprovalBinding {
	t.Helper()
	value, err := contract.NewApprovalBinding(input)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func promotionProposal(t *testing.T, input contract.BindingInput, origin contract.SourceRevision, carrier contract.ApprovalCarrierID) contract.Proposal {
	t.Helper()
	revision, err := contract.NewProposalRevision(promotionBinding(t, input), origin, carrier)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := contract.NewProposal(revision)
	if err != nil {
		t.Fatal(err)
	}
	return proposal
}

func promotionContext(t *testing.T) contract.PromotionContext {
	t.Helper()
	canonical := promotionCanonical(t)
	proposed := promotionProtected(t, promotionManifest(t, "new"), "scope", map[string]string{"runner": "v1"})
	return promotionContextWith(t, canonical, proposed)
}

func promotionContextWith(t *testing.T, canonical contract.CanonicalSnapshot, proposed contract.ProtectedContract) contract.PromotionContext {
	t.Helper()
	ref := contract.ProposalReference{ProjectID: "project", SuiteID: "suite", ProposalID: "proposal", RevisionID: "r1"}
	baseline, _ := canonical.Suite().CurrentVersionID()
	proposal := promotionProposal(t, contract.BindingInput{Reference: ref, ExpectedCanonical: baseline, Manifest: proposed.Manifest().Digest(), Scope: proposed.ScopeDigest(), PolicyRevision: "policy", CoveredInputs: proposed.CoveredInputs()}, "candidate-source", "carrier")
	policy := promotionPolicy(t, "project", "policy", "owner")
	consent, err := contract.NewConsent("project", "suite", "proposal")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := contract.NewPrincipal("owner", contract.Human)
	if err != nil {
		t.Fatal(err)
	}
	command, err := contract.NewCommand(contract.CommandInput{OperationID: "approve-op", SourceCommandID: "approve-source", Actor: owner, Reference: ref, Carrier: "carrier", Action: contract.ApproveConsent, Order: 1})
	if err != nil {
		t.Fatal(err)
	}
	consent, result, err := consent.Apply(proposal, policy, command)
	if err != nil || result.Outcome() != contract.ConsentApproved {
		t.Fatalf("approval fixture: %v %v", result, err)
	}
	evidence, err := contract.NewIntegrityEvidence("emitter", "integrated-source", proposal.Current().Binding(), contract.IntegrityPassed)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := contract.AssessIntegrity("integrated-source", proposal.Current().Binding(), &evidence)
	if err != nil {
		t.Fatal(err)
	}
	return contract.PromotionContext{Canonical: canonical, Proposed: proposed, Proposal: proposal, Reference: ref, Carrier: "carrier", Policy: policy, Consent: consent, Assessment: assessment}
}

func promotionRevoke(t *testing.T, c *contract.PromotionContext) {
	t.Helper()
	owner, err := contract.NewPrincipal("owner", contract.Human)
	if err != nil {
		t.Fatal(err)
	}
	command, err := contract.NewCommand(contract.CommandInput{OperationID: "revoke-op", SourceCommandID: "revoke-source", Actor: owner, Reference: c.Reference, Carrier: c.Carrier, Action: contract.RevokeConsent, Order: 2})
	if err != nil {
		t.Fatal(err)
	}
	c.Consent, _, err = c.Consent.Apply(c.Proposal, c.Policy, command)
	if err != nil {
		t.Fatal(err)
	}
}

func promotionNewerCanonical(t *testing.T) contract.CanonicalSnapshot {
	t.Helper()
	_, _, protected, _ := promotionCanonicalParts(t)
	recordInput := promotionRecordInput(t)
	recordInput.VersionID = "v2"
	value, err := contract.NewCanonicalSnapshot(promotionSuite(t, "project", "suite", "v2", 10), promotionVersion(t, "project", "suite", "v2", protected.Manifest()), protected, promotionRecord(t, recordInput))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPromotionReadinessCombinedAuthorityMatrix(t *testing.T) {
	for _, baseline := range []string{"current", "stale"} {
		for _, approval := range []string{"active", "missing", "revoked"} {
			t.Run(baseline+"/"+approval, func(t *testing.T) {
				c := promotionContext(t)
				if baseline == "stale" {
					c.Canonical = promotionNewerCanonical(t)
				}
				switch approval {
				case "missing":
					c.Consent = contract.Consent{}
				case "revoked":
					promotionRevoke(t, &c)
				}
				beforeSuite := c.Canonical.Suite()
				beforeConsent := c.Consent.Results()
				outcome, reason := contract.PromotionReady, contract.PromotionReasonNone
				if baseline == "stale" {
					outcome, reason = contract.PromotionBlocked, contract.PromotionReasonCanonicalChanged
				} else if approval != "active" {
					outcome, reason = contract.PromotionBlocked, contract.PromotionReasonApprovalMissing
				}
				decision, err := contract.CheckPromotionReadiness(c, "integrated-source")
				requirePromotionDecision(t, decision, err, outcome, reason)
				if c.Canonical.Suite() != beforeSuite || !reflect.DeepEqual(c.Consent.Results(), beforeConsent) {
					t.Fatal("readiness changed original authority snapshots")
				}
			})
		}
	}
}

func TestPromotionReadinessDistinguishesInitialEmptyInventory(t *testing.T) {
	empty := promotionProtected(t, promotionManifest(t), "scope", map[string]string{"runner": "v1"})
	initial := promotionContextWith(t, promotionAbsentCanonical(t), empty)
	decision, err := contract.CheckPromotionReadiness(initial, "integrated-source")
	requirePromotionDecision(t, decision, err, contract.PromotionBlocked, contract.PromotionReasonEmptyInventory)
	established := promotionContextWith(t, promotionCanonical(t), empty)
	decision, err = contract.CheckPromotionReadiness(established, "integrated-source")
	requirePromotionDecision(t, decision, err, contract.PromotionReady, contract.PromotionReasonNone)
}

func requirePromotionDecision(t *testing.T, decision contract.PromotionDecision, err error, outcome contract.PromotionOutcome, reason contract.PromotionReason) {
	t.Helper()
	if err != nil || decision.Outcome() != outcome || decision.Reason() != reason {
		t.Fatalf("decision outcome=%v reason=%v err=%v; want outcome=%v reason=%v", decision.Outcome(), decision.Reason(), err, outcome, reason)
	}
	if effect, present := decision.Effect(); present || !effect.IsZero() {
		t.Fatal("non-promoting decision carried canonical effects")
	}
}

func TestPromotionReadinessRequiresActualCurrentConsent(t *testing.T) {
	context := promotionContext(t)
	decision, err := contract.CheckPromotionReadiness(context, "integrated-source")
	requirePromotionDecision(t, decision, err, contract.PromotionReady, contract.PromotionReasonNone)
	if context.Canonical.Suite().Revision() != 10 || !context.Consent.HasApproval(context.Proposal, context.Policy) {
		t.Fatal("readiness mutated authority")
	}
	for name, change := range map[string]func(*contract.PromotionContext){
		"absent consent": func(c *contract.PromotionContext) { c.Consent = contract.Consent{} },
		"empty consent": func(c *contract.PromotionContext) {
			var err error
			c.Consent, err = contract.NewConsent("project", "suite", "proposal")
			if err != nil {
				t.Fatal(err)
			}
		},
		"different owner": func(c *contract.PromotionContext) { c.Policy = promotionPolicy(t, "project", "policy", "new-owner") },
		"revoked consent": func(c *contract.PromotionContext) {
			owner, err := contract.NewPrincipal("owner", contract.Human)
			if err != nil {
				t.Fatal(err)
			}
			command, err := contract.NewCommand(contract.CommandInput{OperationID: "revoke-op", SourceCommandID: "revoke-source", Actor: owner, Reference: c.Reference, Carrier: c.Carrier, Action: contract.RevokeConsent, Order: 2})
			if err != nil {
				t.Fatal(err)
			}
			c.Consent, _, err = c.Consent.Apply(c.Proposal, c.Policy, command)
			if err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := promotionContext(t)
			change(&c)
			decision, err := contract.CheckPromotionReadiness(c, "integrated-source")
			requirePromotionDecision(t, decision, err, contract.PromotionBlocked, contract.PromotionReasonApprovalMissing)
		})
	}
	context.Policy = promotionPolicy(t, "project", "next-policy", "owner")
	decision, err = contract.CheckPromotionReadiness(context, "integrated-source")
	requirePromotionDecision(t, decision, err, contract.PromotionBlocked, contract.PromotionReasonPolicyChanged)
	context = promotionContext(t)
	context.Policy = promotionPolicy(t, "foreign", "policy", "owner")
	decision, err = contract.CheckPromotionReadiness(context, "integrated-source")
	requirePromotionDecision(t, decision, err, contract.PromotionBlocked, contract.PromotionReasonContextMismatch)
}

func promotionCopyBindingInput(binding contract.ApprovalBinding) contract.BindingInput {
	return contract.BindingInput{Reference: binding.Reference(), ExpectedCanonical: binding.ExpectedCanonical(), Manifest: binding.ManifestDigest(), Scope: binding.ScopeDigest(), PolicyRevision: binding.PolicyRevisionID(), CoveredInputs: binding.CoveredInputs()}
}

func promotionAssessment(t *testing.T, source contract.SourceRevision, binding contract.ApprovalBinding, outcome contract.IntegrityOutcome) contract.IntegrityAssessment {
	t.Helper()
	var evidence *contract.IntegrityEvidence
	if outcome != 0 {
		value, err := contract.NewIntegrityEvidence("emitter", source, binding, outcome)
		if err != nil {
			t.Fatal(err)
		}
		evidence = &value
	}
	value, err := contract.AssessIntegrity(source, binding, evidence)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPromotionReadinessRejectsInvalidStructure(t *testing.T) {
	for name, change := range map[string]func(*contract.PromotionContext){
		"canonical":          func(c *contract.PromotionContext) { c.Canonical = contract.CanonicalSnapshot{} },
		"protected":          func(c *contract.PromotionContext) { c.Proposed = contract.ProtectedContract{} },
		"proposal":           func(c *contract.PromotionContext) { c.Proposal = contract.Proposal{} },
		"reference project":  func(c *contract.PromotionContext) { c.Reference.ProjectID = "" },
		"reference suite":    func(c *contract.PromotionContext) { c.Reference.SuiteID = "\t" },
		"reference proposal": func(c *contract.PromotionContext) { c.Reference.ProposalID = "" },
		"reference revision": func(c *contract.PromotionContext) { c.Reference.RevisionID = " " },
		"carrier":            func(c *contract.PromotionContext) { c.Carrier = "" },
		"blank carrier":      func(c *contract.PromotionContext) { c.Carrier = " \n" },
		"policy":             func(c *contract.PromotionContext) { c.Policy = contract.Policy{} },
	} {
		t.Run(name, func(t *testing.T) {
			c := promotionContext(t)
			change(&c)
			decision, err := contract.CheckPromotionReadiness(c, "integrated-source")
			if !errors.Is(err, contract.ErrInvalidPromotion) || decision.Outcome() != 0 {
				t.Fatalf("invalid structure accepted: outcome=%v err=%v", decision.Outcome(), err)
			}
		})
	}
	for _, source := range []contract.SourceRevision{"", " \n"} {
		decision, err := contract.CheckPromotionReadiness(promotionContext(t), source)
		if !errors.Is(err, contract.ErrInvalidPromotion) || decision.Outcome() != 0 {
			t.Fatalf("invalid required source accepted: %v", err)
		}
	}
}

func TestPromotionReadinessRequiresExactCurrentContext(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*contract.PromotionContext)
		reason contract.PromotionReason
	}{
		{"wrong reference project", func(c *contract.PromotionContext) { c.Reference.ProjectID = "other" }, contract.PromotionReasonContextMismatch},
		{"wrong reference suite", func(c *contract.PromotionContext) { c.Reference.SuiteID = "other" }, contract.PromotionReasonContextMismatch},
		{"wrong reference proposal", func(c *contract.PromotionContext) { c.Reference.ProposalID = "other" }, contract.PromotionReasonContextMismatch},
		{"wrong carrier", func(c *contract.PromotionContext) { c.Carrier = "other" }, contract.PromotionReasonContextMismatch},
		{"unknown revision", func(c *contract.PromotionContext) { c.Reference.RevisionID = "unknown" }, contract.PromotionReasonProposalNotCurrent},
		{"superseded revision", func(c *contract.PromotionContext) {
			input := promotionCopyBindingInput(c.Proposal.Current().Binding())
			input.Reference.RevisionID = "r2"
			revision, err := contract.NewProposalRevision(promotionBinding(t, input), "new-origin", c.Carrier)
			if err != nil {
				t.Fatal(err)
			}
			c.Proposal, err = c.Proposal.Revise(revision)
			if err != nil {
				t.Fatal(err)
			}
		}, contract.PromotionReasonProposalNotCurrent},
		{"foreign actual proposal", func(c *contract.PromotionContext) {
			input := promotionCopyBindingInput(c.Proposal.Current().Binding())
			input.Reference.ProjectID = "other"
			c.Proposal = promotionProposal(t, input, "candidate-source", c.Carrier)
			c.Reference = input.Reference
		}, contract.PromotionReasonContextMismatch},
		{"proposed manifest", func(c *contract.PromotionContext) {
			c.Proposed = promotionProtected(t, promotionManifest(t, "other"), "scope", c.Proposed.CoveredInputs())
		}, contract.PromotionReasonContextMismatch},
		{"proposed scope", func(c *contract.PromotionContext) {
			c.Proposed = promotionProtected(t, c.Proposed.Manifest(), "other", c.Proposed.CoveredInputs())
		}, contract.PromotionReasonContextMismatch},
		{"proposed context", func(c *contract.PromotionContext) {
			c.Proposed = promotionProtected(t, c.Proposed.Manifest(), "scope", map[string]string{"runner": "v2"})
		}, contract.PromotionReasonContextMismatch},
		{"proposed context removed", func(c *contract.PromotionContext) {
			c.Proposed = promotionProtected(t, c.Proposed.Manifest(), "scope", nil)
		}, contract.PromotionReasonContextMismatch},
		{"absent baseline against established", func(c *contract.PromotionContext) {
			input := promotionCopyBindingInput(c.Proposal.Current().Binding())
			input.ExpectedCanonical = ""
			c.Proposal = promotionProposal(t, input, "candidate-source", c.Carrier)
		}, contract.PromotionReasonCanonicalChanged},
		{"established baseline against absence", func(c *contract.PromotionContext) { c.Canonical = promotionAbsentCanonical(t) }, contract.PromotionReasonCanonicalChanged},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := promotionContext(t)
			test.change(&c)
			decision, err := contract.CheckPromotionReadiness(c, "integrated-source")
			requirePromotionDecision(t, decision, err, contract.PromotionBlocked, test.reason)
		})
	}
}

func TestPromotionReadinessRequiresExactSourceBoundEvidence(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*contract.PromotionContext)
		reason contract.PromotionReason
	}{
		{"absent assessment", func(c *contract.PromotionContext) { c.Assessment = contract.IntegrityAssessment{} }, contract.PromotionReasonIntegrityNotPassed},
		{"missing evidence", func(c *contract.PromotionContext) {
			c.Assessment = promotionAssessment(t, "integrated-source", c.Proposal.Current().Binding(), 0)
		}, contract.PromotionReasonIntegrityNotPassed},
		{"failed evidence", func(c *contract.PromotionContext) {
			c.Assessment = promotionAssessment(t, "integrated-source", c.Proposal.Current().Binding(), contract.IntegrityFailed)
		}, contract.PromotionReasonIntegrityNotPassed},
		{"unavailable evidence", func(c *contract.PromotionContext) {
			c.Assessment = promotionAssessment(t, "integrated-source", c.Proposal.Current().Binding(), contract.IntegrityUnavailable)
		}, contract.PromotionReasonIntegrityNotPassed},
		{"premerge source", func(c *contract.PromotionContext) {
			c.Assessment = promotionAssessment(t, "candidate-source", c.Proposal.Current().Binding(), contract.IntegrityPassed)
		}, contract.PromotionReasonAssessmentMismatch},
		{"different assessment binding", func(c *contract.PromotionContext) {
			input := promotionCopyBindingInput(c.Proposal.Current().Binding())
			input.Reference.RevisionID = "other"
			c.Assessment = promotionAssessment(t, "integrated-source", promotionBinding(t, input), contract.IntegrityPassed)
		}, contract.PromotionReasonAssessmentMismatch},
		{"mismatched observed evidence", func(c *contract.PromotionContext) {
			evidence, err := contract.NewIntegrityEvidence("emitter", "wrong-source", c.Proposal.Current().Binding(), contract.IntegrityPassed)
			if err != nil {
				t.Fatal(err)
			}
			c.Assessment, err = contract.AssessIntegrity("integrated-source", c.Proposal.Current().Binding(), &evidence)
			if err != nil {
				t.Fatal(err)
			}
		}, contract.PromotionReasonAssessmentMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := promotionContext(t)
			test.change(&c)
			decision, err := contract.CheckPromotionReadiness(c, "integrated-source")
			requirePromotionDecision(t, decision, err, contract.PromotionBlocked, test.reason)
		})
	}
	c := promotionContext(t)
	c.Assessment = promotionAssessment(t, "candidate-source", c.Proposal.Current().Binding(), contract.IntegrityPassed)
	decision, err := contract.CheckPromotionReadiness(c, "candidate-source")
	requirePromotionDecision(t, decision, err, contract.PromotionReady, contract.PromotionReasonNone)
}

func promotionInput(t *testing.T) contract.PromotionInput {
	t.Helper()
	return contract.PromotionInput{Context: promotionContext(t), Integration: promotionIntegration(t, "integrated-source", "carrier", contract.IntegrationMergedChange), Target: "main", OperationID: "promote-op", NewVersionID: "v2", RecordedAt: time.Date(2026, 9, 30, 13, 0, 0, 0, time.UTC), CorrectsVersionID: "v0"}
}

func TestPromotionProposesOneConsistentImmutableEffect(t *testing.T) {
	input := promotionInput(t)
	beforeSuite := input.Context.Canonical.Suite()
	beforeConsent := input.Context.Consent.Results()
	decision, err := contract.DecidePromotion(input)
	if err != nil || decision.Outcome() != contract.PromotionProposed || decision.Reason() != contract.PromotionReasonNone {
		t.Fatalf("promotion not proposed: outcome=%v reason=%v err=%v", decision.Outcome(), decision.Reason(), err)
	}
	effect, present := decision.Effect()
	if !present || effect.IsZero() {
		t.Fatal("proposed promotion omitted grouped effect")
	}
	if effect.ExpectedCanonicalID() != "v1" {
		t.Fatal("effect lost its expected canonical baseline")
	}
	current, exists := effect.Suite().CurrentVersionID()
	if !exists || current != "v2" || effect.Suite().ProjectID() != "project" || effect.Suite().ID() != "suite" || effect.Suite().Revision() != 11 || effect.Version().ID() != "v2" || effect.Version().ProjectID() != "project" || effect.Version().SuiteID() != "suite" || effect.Version().Manifest().Digest() != input.Context.Proposed.Manifest().Digest() {
		t.Fatal("grouped proposed version and pointer disagree")
	}
	record := effect.Promotion()
	if record.IsZero() || record.OperationID() != input.OperationID || record.VersionID() != input.NewVersionID || !record.Binding().Equal(input.Context.Proposal.Current().Binding()) || record.Carrier() != input.Context.Carrier || record.Source() != input.Integration.Source() || record.Target() != input.Target || record.RecordedAt() != input.RecordedAt || record.CorrectsVersionID() != input.CorrectsVersionID {
		t.Fatal("effect promotion record lost exact facts")
	}
	if _, err := contract.NewCanonicalSnapshot(effect.Suite(), effect.Version(), input.Context.Proposed, record); err != nil {
		t.Fatalf("proposed canonical is internally contradictory: %v", err)
	}
	context := record.Binding().CoveredInputs()
	context["runner"] = "changed"
	entries := effect.Version().Manifest().Entries()
	entries[0].Path = "changed"
	if effect.Promotion().Binding().CoveredInputs()["runner"] != "v1" || effect.Version().Manifest().Entries()[0].Path != "a_test.go" {
		t.Fatal("grouped effect aliases mutable returned data")
	}
	if input.Context.Canonical.Suite() != beforeSuite || !reflect.DeepEqual(input.Context.Consent.Results(), beforeConsent) {
		t.Fatal("proposed effect changed input authority snapshots")
	}
	promotionRevoke(t, &input.Context)
	if effect.Promotion().OperationID() != "promote-op" || effect.Suite().Revision() != 11 {
		t.Fatal("subsequent consent changed already produced immutable facts")
	}
	if !(contract.PromotionEffect{}).IsZero() {
		t.Fatal("zero effect claimed history")
	}
}

func TestPromotionNoChangeHasNoAuthorityOrEffect(t *testing.T) {
	current := promotionCanonical(t)
	input := contract.PromotionInput{Context: contract.PromotionContext{Canonical: current, Proposed: current.Contract()}}
	decision, err := contract.DecidePromotion(input)
	requirePromotionDecision(t, decision, err, contract.PromotionNoChange, contract.PromotionReasonNone)
	if current.Suite().Revision() != 10 {
		t.Fatal("no-change moved canonical")
	}
	for _, proposed := range []contract.ProtectedContract{
		promotionProtected(t, current.Contract().Manifest(), "new-scope", current.Contract().CoveredInputs()),
		promotionProtected(t, current.Contract().Manifest(), "scope", map[string]string{"runner": "v2"}),
	} {
		input.Context.Proposed = proposed
		decision, _ = contract.DecidePromotion(input)
		if decision.Outcome() == contract.PromotionNoChange {
			t.Fatal("same manifest concealed protected scope/context change")
		}
	}
}

func TestPromotionDelegatesCurrentAuthorityAndInitialEmptyGuards(t *testing.T) {
	input := promotionInput(t)
	input.Context.Consent = contract.Consent{}
	decision, err := contract.DecidePromotion(input)
	requirePromotionDecision(t, decision, err, contract.PromotionBlocked, contract.PromotionReasonApprovalMissing)
	input = promotionInput(t)
	input.Integration = contract.Integration{}
	decision, err = contract.DecidePromotion(input)
	requirePromotionDecision(t, decision, err, contract.PromotionBlocked, contract.PromotionReasonIntegrationMissing)
	empty := promotionProtected(t, promotionManifest(t), "scope", map[string]string{"runner": "v1"})
	input = promotionInput(t)
	input.Context = promotionContextWith(t, promotionAbsentCanonical(t), empty)
	input.Integration = promotionIntegration(t, "candidate-source", "", contract.IntegrationExistingBaseline)
	input.Context.Assessment = promotionAssessment(t, "candidate-source", input.Context.Proposal.Current().Binding(), contract.IntegrityPassed)
	decision, err = contract.DecidePromotion(input)
	requirePromotionDecision(t, decision, err, contract.PromotionBlocked, contract.PromotionReasonEmptyInventory)
	input = promotionInput(t)
	input.Context = promotionContextWith(t, promotionCanonical(t), empty)
	decision, err = contract.DecidePromotion(input)
	if err != nil || decision.Outcome() != contract.PromotionProposed {
		t.Fatalf("approved established empty contract blocked: outcome=%v reason=%v err=%v", decision.Outcome(), decision.Reason(), err)
	}
}

func TestPromotionRequiresExactConfirmedIntegration(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*contract.PromotionInput)
		reason contract.PromotionReason
	}{
		{"foreign project", func(i *contract.PromotionInput) {
			value, err := contract.NewIntegration("other", "main", "integrated-source", "carrier", contract.IntegrationMergedChange)
			if err != nil {
				t.Fatal(err)
			}
			i.Integration = value
		}, contract.PromotionReasonIntegrationMismatch},
		{"different target", func(i *contract.PromotionInput) {
			value, err := contract.NewIntegration("project", "other", "integrated-source", "carrier", contract.IntegrationMergedChange)
			if err != nil {
				t.Fatal(err)
			}
			i.Integration = value
		}, contract.PromotionReasonIntegrationMismatch},
		{"different configured target", func(i *contract.PromotionInput) { i.Target = "other" }, contract.PromotionReasonIntegrationMismatch},
		{"different carrier", func(i *contract.PromotionInput) {
			i.Integration = promotionIntegration(t, "integrated-source", "other", contract.IntegrationMergedChange)
		}, contract.PromotionReasonIntegrationMismatch},
		{"assessment only premerge", func(i *contract.PromotionInput) {
			i.Context.Assessment = promotionAssessment(t, "candidate-source", i.Context.Proposal.Current().Binding(), contract.IntegrityPassed)
		}, contract.PromotionReasonAssessmentMismatch},
		{"baseline when canonical exists", func(i *contract.PromotionInput) {
			i.Integration = promotionIntegration(t, "candidate-source", "", contract.IntegrationExistingBaseline)
			i.Context.Assessment = promotionAssessment(t, "candidate-source", i.Context.Proposal.Current().Binding(), contract.IntegrityPassed)
		}, contract.PromotionReasonIntegrationMismatch},
		{"baseline source differs from pinned origin", func(i *contract.PromotionInput) {
			i.Context = promotionContextWith(t, promotionAbsentCanonical(t), i.Context.Proposed)
			i.Integration = promotionIntegration(t, "integrated-source", "", contract.IntegrationExistingBaseline)
		}, contract.PromotionReasonIntegrationMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := promotionInput(t)
			test.change(&input)
			decision, err := contract.DecidePromotion(input)
			requirePromotionDecision(t, decision, err, contract.PromotionBlocked, test.reason)
		})
	}
	input := promotionInput(t)
	input.Context = promotionContextWith(t, promotionAbsentCanonical(t), input.Context.Proposed)
	input.Integration = promotionIntegration(t, "candidate-source", "", contract.IntegrationExistingBaseline)
	input.Context.Assessment = promotionAssessment(t, "candidate-source", input.Context.Proposal.Current().Binding(), contract.IntegrityPassed)
	input.NewVersionID = "v1"
	input.CorrectsVersionID = ""
	decision, err := contract.DecidePromotion(input)
	if err != nil || decision.Outcome() != contract.PromotionProposed {
		t.Fatalf("exact independently hosted baseline could not promote: outcome=%v reason=%v err=%v", decision.Outcome(), decision.Reason(), err)
	}
	effect, present := decision.Effect()
	if !present || effect.ExpectedCanonicalID() != "" || effect.Promotion().Carrier() != "carrier" || effect.Promotion().Source() != "candidate-source" {
		t.Fatal("baseline effect conflated independent approval carrier and integrated source")
	}
}

func TestPromotionRejectsInvalidEffectIdentity(t *testing.T) {
	for name, change := range map[string]func(*contract.PromotionInput){
		"canonical absent":       func(i *contract.PromotionInput) { i.Context.Canonical = contract.CanonicalSnapshot{} },
		"protected absent":       func(i *contract.PromotionInput) { i.Context.Proposed = contract.ProtectedContract{} },
		"proposal absent":        func(i *contract.PromotionInput) { i.Context.Proposal = contract.Proposal{} },
		"operation absent":       func(i *contract.PromotionInput) { i.OperationID = "" },
		"operation blank":        func(i *contract.PromotionInput) { i.OperationID = " " },
		"version absent":         func(i *contract.PromotionInput) { i.NewVersionID = "" },
		"version blank":          func(i *contract.PromotionInput) { i.NewVersionID = "\n" },
		"current version reused": func(i *contract.PromotionInput) { i.NewVersionID = "v1" },
		"target absent":          func(i *contract.PromotionInput) { i.Target = "" },
		"target blank":           func(i *contract.PromotionInput) { i.Target = "\t" },
		"timestamp absent":       func(i *contract.PromotionInput) { i.RecordedAt = time.Time{} },
		"correction blank":       func(i *contract.PromotionInput) { i.CorrectsVersionID = "\t" },
		"self correction":        func(i *contract.PromotionInput) { i.CorrectsVersionID = i.NewVersionID },
	} {
		t.Run(name, func(t *testing.T) {
			input := promotionInput(t)
			change(&input)
			decision, err := contract.DecidePromotion(input)
			if !errors.Is(err, contract.ErrInvalidPromotion) || decision.Outcome() != 0 {
				t.Fatalf("invalid effect accepted: outcome=%v reason=%v err=%v", decision.Outcome(), decision.Reason(), err)
			}
			if effect, present := decision.Effect(); present || !effect.IsZero() {
				t.Fatal("invalid effect escaped")
			}
		})
	}
}

func TestPromotionDecisionKeepsCauseOfInvalidPromotion(t *testing.T) {
	input := promotionInput(t)
	input.Context.Proposed = contract.ProtectedContract{}
	_, err := contract.DecidePromotion(input)
	if !errors.Is(err, contract.ErrInvalidPromotion) || !errors.Is(err, contract.ErrInvalidProtectedContract) {
		t.Errorf("classification failure lost its cause: %v", err)
	}
	input = promotionInput(t)
	input.RecordedAt = time.Time{}
	_, err = contract.DecidePromotion(input)
	if !errors.Is(err, contract.ErrInvalidPromotion) || !errors.Is(err, contract.ErrInvalidPromotionRecord) {
		t.Errorf("record failure lost its cause: %v", err)
	}
}

func TestPromotionReadinessKeepsCauseOfInvalidProposalInputs(t *testing.T) {
	for name, test := range map[string]struct {
		change func(*contract.PromotionContext)
		cause  error
	}{
		"zero proposal":     {func(c *contract.PromotionContext) { c.Proposal = contract.Proposal{} }, contract.ErrInvalidProposal},
		"invalid reference": {func(c *contract.PromotionContext) { c.Reference.RevisionID = " " }, contract.ErrInvalidReference},
		"blank carrier":     {func(c *contract.PromotionContext) { c.Carrier = " \n" }, contract.ErrInvalidReference},
	} {
		t.Run(name, func(t *testing.T) {
			c := promotionContext(t)
			test.change(&c)
			decision, err := contract.CheckPromotionReadiness(c, "integrated-source")
			if !errors.Is(err, contract.ErrInvalidPromotion) || !errors.Is(err, test.cause) || decision.Outcome() != 0 {
				t.Fatalf("outcome=%v err=%v, want ErrInvalidPromotion caused by %v", decision.Outcome(), err, test.cause)
			}
			input := promotionInput(t)
			test.change(&input.Context)
			if _, err := contract.DecidePromotion(input); !errors.Is(err, contract.ErrInvalidPromotion) || !errors.Is(err, test.cause) {
				t.Fatalf("promotion err=%v, want ErrInvalidPromotion caused by %v", err, test.cause)
			}
		})
	}
}
