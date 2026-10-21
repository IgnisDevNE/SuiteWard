package inventory_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

// casings returns n distinct spellings of word that differ only in case.
func casings(word string, n int) []string {
	spellings := make([]string, n)
	for i := range spellings {
		var b strings.Builder
		for j, r := range word {
			if i>>j&1 == 1 {
				r -= 'a' - 'A'
			}
			b.WriteRune(r)
		}
		spellings[i] = b.String()
	}
	return spellings
}

// One folded name may have a few spellings, not thousands: every spelling is a path to rewrite, so an untrusted tree could make the
// work grow with the square of its size. A tree beyond the cap is invalid, which keeps the governing scope in force.
func TestATreeWithManySpellingsOfOneDirectoryIsInvalid(t *testing.T) {
	for name, tc := range map[string]struct {
		spellings int
		want      bool
	}{"8 spellings": {8, true}, "9 spellings": {9, false}, "4096 spellings": {1 << 12, false}} {
		t.Run(name, func(t *testing.T) {
			files := baseFiles()
			for _, dir := range casings("abcdefghijkl", tc.spellings) {
				files[dir+"/f"] = regular(dir+"/f", "x")
			}
			start := time.Now()
			_, err := inventory.Build(context.Background(), treeOf(t, files))
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Errorf("Build took %v", elapsed)
			}
			if tc.want && err != nil || !tc.want && !errors.Is(err, inventory.ErrInvalidTree) {
				t.Fatalf("Build error = %v, want valid: %v", err, tc.want)
			}
		})
	}
}

func TestATreeWithManySpellingsOfOneFileIsInvalid(t *testing.T) {
	for name, tc := range map[string]struct {
		spellings int
		want      bool
	}{"8 spellings": {8, true}, "9 spellings": {9, false}} {
		t.Run(name, func(t *testing.T) {
			files := baseFiles()
			for _, name := range casings("abcd", tc.spellings) {
				files["docs/"+name] = regular("docs/"+name, "x")
			}
			_, err := inventory.Build(context.Background(), treeOf(t, files))
			if tc.want && err != nil || !tc.want && !errors.Is(err, inventory.ErrInvalidTree) {
				t.Fatalf("Build error = %v, want valid: %v", err, tc.want)
			}
		})
	}
}

// Trees without case twins, however large, stay fast.
func TestALargeTreeWithoutCaseTwinsIsFast(t *testing.T) {
	files := baseFiles()
	for i := range 100_000 {
		path := fmt.Sprintf("pkg%d/sub%d/file%d.go", i%500, i%97, i)
		files[path] = regular(path, "x")
	}
	tree := treeOf(t, files)
	start := time.Now()
	if _, err := inventory.Build(context.Background(), tree); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("Build of %d entries took %v", len(files), elapsed)
	}
}

// The directory spellings are bounded on their own too: files with different names under nine casings of one directory
// stay under the cap of each file name and still reach nine spellings of the directory.
func TestATreeWithManySpellingsOfOneDirectoryOfDistinctFilesIsInvalid(t *testing.T) {
	files := baseFiles()
	for i, dir := range casings("abcdefghijkl", 9) {
		name := fmt.Sprintf("%s/f%d", dir, i)
		files[name] = regular(name, "x")
	}
	if _, err := inventory.Build(context.Background(), treeOf(t, files)); !errors.Is(err, inventory.ErrInvalidTree) {
		t.Fatalf("Build error = %v, want ErrInvalidTree", err)
	}
}
