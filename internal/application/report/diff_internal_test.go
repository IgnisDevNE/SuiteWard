package report

import "testing"

func TestDiffStopsWhenTheWorkIsSpent(t *testing.T) {
	t.Parallel()
	a, b := []int{1, 2, 3, 4, 5, 6, 7, 8}, []int{8, 7, 6, 5, 4, 3, 2, 1}
	work := 10
	if _, _, ok := diff(a, b, &work); ok {
		t.Fatal("a diff that needs more than the work left must give up")
	}
	work = 1 << 20
	del, ins, ok := diff(a, b, &work)
	if !ok {
		t.Fatal("a diff within the work left must succeed")
	}
	if work >= 1<<20 {
		t.Fatal("the diff must spend the work it was given, so that the budget is shared by the files of a report")
	}
	if countTrue(del)+countTrue(ins) != 14 { // the longest common subsequence of a reversal has one element
		t.Fatalf("got %d deleted and %d inserted lines, want 7 and 7", countTrue(del), countTrue(ins))
	}
}

func TestDiffTrimsWhatIsCommonWithoutWork(t *testing.T) {
	t.Parallel()
	work := 0
	del, ins, ok := diff([]int{1, 2, 3}, []int{1, 2, 3}, &work)
	if !ok || countTrue(del) != 0 || countTrue(ins) != 0 {
		t.Fatalf("identical sequences: ok=%v, %d deleted, %d inserted", ok, countTrue(del), countTrue(ins))
	}
	del, ins, ok = diff([]int{1, 2, 3}, []int{1, 3}, &work)
	if !ok || countTrue(del) != 1 || !del[1] || countTrue(ins) != 0 {
		t.Fatalf("one deletion: ok=%v, del=%v, ins=%v", ok, del, ins)
	}
}

func countTrue(v []bool) int {
	n := 0
	for _, b := range v {
		if b {
			n++
		}
	}
	return n
}
