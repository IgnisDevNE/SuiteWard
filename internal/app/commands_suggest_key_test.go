package app

import (
	"testing"
	"time"
)

// A review is keyed by what it posts, the head's file and the suggestions, and not by the paths that brought them: the same suggestion on the
// same file is one review, whichever head or command produced it, and a different suggestion is another.
func TestSuggestWritesOneReviewForTheSameSuggestionWhateverPathsBroughtIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		second  []string // the paths the head adds the second time
		reviews int
	}{
		{"other paths, the same suggestion", []string{"pkg/other_test.go"}, 1},
		{"another suggestion", []string{cjUtilTst, "pkg/a.spec.js"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newCjWorld(t)
			w.trees(map[string]string{"README.md": "x"}, reviewFiles(reviewDecl, cjUtilTst))
			w.pullFiles(changed("@@ -1,3 +1,3 @@"))
			w.syncAll([]cjComment{cjSuggest(500, cjAt(10))})
			w.mustRun()
			w.trees(map[string]string{"README.md": "x"}, reviewFiles(reviewDecl, tc.second...))
			w.observe(cjID1, cjSync.Add(time.Minute), cjSuggest(501, cjAt(11)))
			w.mustRun()

			if keys := w.scopeMessagesKeys("scope-review:"); len(keys) != tc.reviews {
				t.Fatalf("review keys %v; want %d", keys, tc.reviews)
			}
		})
	}
}
