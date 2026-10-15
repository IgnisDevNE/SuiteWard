package commandtext

// MergeKind is the outcome of a merged pull request that gets a comment.
type MergeKind uint8

const (
	MergePromoted MergeKind = iota + 1
	MergeCorrected
	MergeBlocked
)

// Merge is what the outcome comment of a merged pull request states.
type Merge struct {
	Kind               MergeKind
	Reason             string // blocked: the stable reason code
	Source             string // the merge commit (40 lowercase hex); empty only for the reason integration-unreadable
	Version            int    // V<n>: the new version, or the canonical version a blocked merge leaves in force
	Corrects           int    // V<n> corrected; corrected only
	Proposal, Revision int    // the contract change revision; both zero when there is none
	Tier               Tier
}

// MergeComment renders the outcome comment of a merged pull request.
func MergeComment(Merge) string { return "" }
