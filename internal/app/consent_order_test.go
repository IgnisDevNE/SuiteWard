package app

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commands"
	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/githubsync"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func TestConsentRequestRefusesANegativeCommentIdentifier(t *testing.T) {
	enrollment := owner.State{Owner: owner.Owner{AccountID: 77, Login: "owner"}}
	command := commandtext.Command{Verb: commandtext.Approve, Proposal: 1, Revision: 1}
	for _, id := range []int64{1, 1 << 40} {
		c := commands.Candidate{RepositoryID: 5, Repo: "acme/widgets", PullNumber: 3, Comment: githubsync.CommentVersion{CommentID: id}}
		if _, err := consentRequest(enrollment, c, command, "github-comment:1", contract.OperationID("op")); err != nil {
			t.Fatalf("comment %d: %v", id, err)
		}
	}
	c := commands.Candidate{RepositoryID: 5, Repo: "acme/widgets", PullNumber: 3, Comment: githubsync.CommentVersion{CommentID: -1}}
	if request, err := consentRequest(enrollment, c, command, "github-comment:-1", contract.OperationID("op")); err == nil {
		t.Fatalf("a negative comment identifier became the command order %d", request.Command.Order())
	}
}
