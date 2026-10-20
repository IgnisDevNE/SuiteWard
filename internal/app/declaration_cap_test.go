package app

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// declarationOfSize is a valid declaration of exactly size bytes: a comment pads it.
func declarationOfSize(size int) string {
	const head = "version: 1\n# "
	return head + strings.Repeat("x", size-len(head)-1) + "\n"
}

// The declaration of an untrusted tree is read in memory and parsed as YAML, so its size is capped.
func TestGitHubTreeCapsTheUntrustedDeclaration(t *testing.T) {
	read := func(declaration string) ([]byte, error) {
		files := bootFiles("v1")
		files[scope.DeclarationPath] = declaration
		files["docs/big.md"] = strings.Repeat("y", 3*maxDeclarationBytes) // only the declaration is capped
		tree := &githubTree{tree: &bootTree{files: files}, files: map[string][]byte{}}
		if _, err := tree.Read(t.Context(), "docs/big.md"); err != nil {
			t.Fatalf("another file was refused: %v", err)
		}
		return tree.Read(t.Context(), scope.DeclarationPath)
	}

	if _, err := read(declarationOfSize(maxDeclarationBytes)); err != nil {
		t.Errorf("a declaration of exactly %d bytes was refused: %v", maxDeclarationBytes, err)
	}
	_, err := read(declarationOfSize(maxDeclarationBytes + 1))
	if err == nil || !invalidSource(err) {
		t.Errorf("a declaration over the cap: err = %v; want an invalid source", err)
	}
}

func TestChecksFailAHeadWhoseDeclarationExceedsTheCap(t *testing.T) {
	w := newCJBootWorld(t)
	files := bootFiles("v1")
	files[scope.DeclarationPath] = declarationOfSize(maxDeclarationBytes + 1)
	w.tree(cjHead2, files)
	w.sync(map[int]string{5: cjHead2})
	w.pullProposal(5, cjHead2, bootFiles("v1"))

	w.mustCheck()

	w.want(5, checks.Failure, "invalid-source", 1)
}
