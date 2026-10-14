package commandtext

// Outcome is the processed result an acknowledgment reports.
type Outcome uint8

const (
	Approved Outcome = iota + 1
	Revoked
	NoActiveApproval
	Rejected
	MalformedCommand
	InvalidCode
	ReplayedCode
	NoSuite
	EditedFirst
	BootstrapPromoted
)

// Rejection is the domain reason of a Rejected outcome.
type Rejection uint8

const (
	Unauthorized Rejection = iota + 1
	UnknownRevision
	SupersededRevision
	ContextMismatch
	PolicyMismatch
	ObsoleteCommand
)

// Ack is what an acknowledgment states.
//   - Proposal and Revision name the revision. They are required, except for
//     MalformedCommand and EditedFirst, which have no trustworthy reference and
//     must leave both zero.
//   - Rejection is set only for Rejected, Malformed only for MalformedCommand,
//     Version (V<n>) only for BootstrapPromoted.
//   - Owner is the owner's GitHub login.
//   - Eligible is the revision's promotion eligibility after this outcome. It
//     is rendered for Approved, Revoked, NoActiveApproval and Rejected, and must
//     be false for the other outcomes, which never evaluate it.
type Ack struct {
	Outcome   Outcome
	Rejection Rejection
	Malformed Reason
	Proposal  int
	Revision  int
	Version   int
	Owner     string
	Eligible  bool
}

// Tier is the installation tier (ADR 0027, D-TIER).
type Tier uint8

const (
	Isolated Tier = iota + 1
	CoLocated
)

// Summary is what a bootstrap summary states (ADR 0010). Paths holds at most
// 20 of the Protected files; Origin is the 40-hex origin commit SHA.
type Summary struct {
	Proposal  int
	Revision  int
	Origin    string
	Branch    string
	Protected int
	Paths     []string
	Include   []string
	Exclude   []string
	Tier      Tier
}

// Acknowledge renders the acknowledgment body of a processed command.
func Acknowledge(a Ack) string {
	panic("not implemented")
}

// BootstrapSummary renders the bootstrap summary body.
func BootstrapSummary(s Summary) string {
	panic("not implemented")
}

// Marker returns the hidden marker <!-- suiteward:<instance>:<key> -->.
func Marker(instance, key string) string {
	panic("not implemented")
}

// HasMarker reports whether body ends with Marker(instance, key).
func HasMarker(body, instance, key string) bool {
	panic("not implemented")
}
