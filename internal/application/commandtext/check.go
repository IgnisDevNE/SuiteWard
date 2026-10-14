package commandtext

// CheckState is the conclusion of the SuiteWard / Contract check (D-CHECKS).
type CheckState uint8

const (
	CheckSuccess CheckState = iota + 1
	CheckInProgress
	CheckFailure
)

// Check is what the output of a check run states. Reason is the stable code the check job stored, and State must
// be the one the code belongs to. Proposal and Revision name the revision the check concerns and are both zero
// when there is none; approval-missing requires them. Version (V<n>) is the canonical version, zero when there is
// none.
type Check struct {
	State              CheckState
	Reason             string
	Proposal, Revision int
	Version            int
	Tier               Tier
}

// CheckSummary renders the output title and summary of a check run.
func CheckSummary(c Check) (title, summary string) {
	panic("not implemented")
}
