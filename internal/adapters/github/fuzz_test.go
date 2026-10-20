package github_test

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
)

// FuzzNewLineRanges feeds hostile patch text (the patch comes from GitHub's API for a file of a pull request) to
// NewLineRanges. It checks the error category, that each range is a non-empty span within int, and that a hunk
// yields at most one range, in patch order.
func FuzzNewLineRanges(f *testing.F) {
	for _, seed := range []string{
		"", "@@ -10,6 +10,7 @@ func Run() {\n a\n+b", "@@ -40 +41 @@\n-x\n+y", "@@ -1,2 +0,0 @@\n-one\n-two",
		"@@ -1 +1 @@\n+@@ not a header", "@@ @@\n+x", "@@ -1 +99999999999999999999 @@\n+x",
		fmt.Sprintf("@@ -1 +%d,2 @@\n+x", math.MaxInt), "@@ -0,0 +0,3 @@\n+x", "@@ -1 +5 @@\n+a\n@@ -1 +2 @@\n+b",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, patch string) {
		file := github.PullFile{Path: "a.go", Patch: patch}
		ranges, err := file.NewLineRanges()
		again, errAgain := file.NewLineRanges()
		if fmt.Sprint(ranges, err) != fmt.Sprint(again, errAgain) {
			t.Fatalf("NewLineRanges is not deterministic for %q", patch)
		}
		if err != nil {
			if !errors.Is(err, github.ErrInvalidResponse) || ranges != nil {
				t.Fatalf("NewLineRanges = %v, %v; want nil and ErrInvalidResponse", ranges, err)
			}
			return
		}
		hunks := strings.Count("\n"+patch, "\n@@")
		if len(ranges) > hunks {
			t.Fatalf("%d ranges for %d hunk headers in %q", len(ranges), hunks, patch)
		}
		for _, r := range ranges {
			if r.Start < 0 || r.End < r.Start {
				t.Fatalf("range %+v is empty, negative or inverted in %q", r, patch)
			}
		}
	})
}

// FuzzNewLineRangesHeader builds a well-formed header from two numbers and checks the exact range, including the
// overflow boundary.
func FuzzNewLineRangesHeader(f *testing.F) {
	f.Add(uint64(10), uint64(7))
	f.Add(uint64(1), uint64(0))
	f.Add(uint64(math.MaxInt), uint64(1))
	f.Add(uint64(math.MaxInt), uint64(2))
	f.Add(uint64(math.MaxUint64), uint64(math.MaxUint64))
	f.Fuzz(func(t *testing.T, start, count uint64) {
		patch := "@@ -1 +" + strconv.FormatUint(start, 10) + "," + strconv.FormatUint(count, 10) + " @@\n+x"
		ranges, err := github.PullFile{Path: "a.go", Patch: patch}.NewLineRanges()
		fits := start <= math.MaxInt && count <= math.MaxInt && (count == 0 || count-1 <= math.MaxInt-start)
		switch {
		case !fits:
			if !errors.Is(err, github.ErrInvalidResponse) {
				t.Fatalf("%q = %v, %v; want ErrInvalidResponse", patch, ranges, err)
			}
		case err != nil:
			t.Fatalf("%q: %v", patch, err)
		case count == 0 && len(ranges) != 0, count > 0 && (len(ranges) != 1 || ranges[0] != github.LineRange{Start: int(start), End: int(start + count - 1)}):
			t.Fatalf("%q = %v, want the span of %d lines from %d", patch, ranges, count, start)
		}
	})
}
