package app

import (
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// errNoPolicy is the impossible state of a run that assesses a head under a canonical without having read its execution policy.
var errNoPolicy = errors.New("the execution policy was not read")

// revisionReceipt is the receipt level that the comment of a new revision states (docs/contracts/runtime.md, "The revision comment"): the effective level of the canonical
// declaration when it is not off and the revision adds or changes a file that the canonical include matches, against the manifest of the canonical version it expects, and
// none (empty) otherwise. It reads nothing but what the run holds (the policy, the manifest of the canonical, the manifest of the notice), because it runs inside the unit of
// work of the assessment, which no store may be asked again from.
//
// A revision that was assessed against another canonical than the one the run read the policy from is not decided: the canonical moved, and the assessment is tried again.
func revisionReceipt(x *executionRun, suite suiteView, n governance.RevisionNotice) (string, error) {
	switch {
	case n.Canonical != suite.canonical:
		return "", governance.ErrCanonicalMoved
	case !x.known:
		return "", &opaque{what: "state the receipt of the revision", cause: errNoPolicy}
	case !x.policy.receiptOn():
		return "", nil
	}
	files, err := domain.EvaluatedFiles(n.Manifest, suite.base, x.policy.selection())
	if err != nil {
		return "", &opaque{what: "list the files of the receipt", cause: err}
	}
	if len(files) == 0 {
		return "", nil
	}
	return x.policy.receipt.String(), nil
}
