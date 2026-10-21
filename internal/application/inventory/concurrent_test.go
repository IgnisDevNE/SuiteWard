package inventory_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/inventory"
)

// slowTree is a pinned tree of covered files whose reads take latency and are safe to overlap when concurrent says so. It counts
// the reads in flight, the reads that started and the reads that ended in an error.
type slowTree struct {
	paths      []string
	latency    time.Duration
	concurrent bool
	failOn     map[string]error

	mu                      sync.Mutex
	inFlight, peak, started int
}

func newSlowTree(files int, latency time.Duration, concurrent bool) *slowTree {
	paths := make([]string, files)
	for i := range paths {
		paths[i] = fmt.Sprintf("tests/f%03d_test.go", i)
	}
	return &slowTree{paths: paths, latency: latency, concurrent: concurrent}
}

func (s *slowTree) ConcurrentReads() bool { return s.concurrent }

func (s *slowTree) Entries(context.Context) ([]inventory.Entry, error) {
	entries := []inventory.Entry{{Path: declaration, Kind: inventory.File}}
	for _, path := range s.paths {
		entries = append(entries, inventory.Entry{Path: path, Kind: inventory.File})
	}
	return entries, nil
}

func (s *slowTree) Read(ctx context.Context, path string) ([]byte, error) {
	if path == declaration {
		return []byte("version: 1\ninclude: [/tests/**]\n"), nil
	}
	s.mu.Lock()
	s.started++
	s.inFlight++
	s.peak = max(s.peak, s.inFlight)
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()
	if err := s.failOn[path]; err != nil {
		return nil, err
	}
	select {
	case <-time.After(s.latency):
		return []byte("content of " + path), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// bound is the number of reads a cold assessment may have in flight at once.
const bound = 8

func TestAColdBuildOverlapsTheReadsOfATreeThatAllowsIt(t *testing.T) {
	for name, tc := range map[string]struct {
		concurrent bool
		check      func(peak int) bool
		want       string
	}{
		"a tree that allows it":         {true, func(peak int) bool { return peak > 1 && peak <= bound }, fmt.Sprintf("more than 1 and at most %d", bound)},
		"a tree that does not allow it": {false, func(peak int) bool { return peak == 1 }, "1"},
	} {
		t.Run(name, func(t *testing.T) {
			tree := newSlowTree(60, 10*time.Millisecond, tc.concurrent)
			if _, err := inventory.Build(context.Background(), tree); err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !tc.check(tree.peak) {
				t.Errorf("peak reads in flight = %d, want %s", tree.peak, tc.want)
			}
		})
	}
}

func TestAParallelBuildHasTheManifestOfASequentialOne(t *testing.T) {
	build := func(concurrent bool) []string {
		inv, err := inventory.Build(context.Background(), newSlowTree(60, time.Millisecond, concurrent))
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		var lines []string
		for _, e := range inv.Manifest().Entries() {
			lines = append(lines, fmt.Sprintf("%s %v", e.Path, e.Content))
		}
		return lines
	}
	sequential, parallel := build(false), build(true)
	if len(sequential) != 61 || !slices.Equal(sequential, parallel) {
		t.Errorf("the parallel manifest differs from the sequential one (%d and %d entries)", len(parallel), len(sequential))
	}
}

func TestAnEarlyReadErrorCancelsTheOtherReadsAndIsTheOneReturned(t *testing.T) {
	boom := errors.New("boom")
	tree := newSlowTree(200, 50*time.Millisecond, true)
	tree.failOn = map[string]error{tree.paths[3]: boom}

	_, err := inventory.Build(context.Background(), tree)

	if !errors.Is(err, boom) || errors.Is(err, context.Canceled) {
		t.Errorf("Build error = %v, want the failing read's error and not a cancellation", err)
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.started > 2*bound {
		t.Errorf("%d reads started after the first failed, want about %d at most", tree.started, bound)
	}
	if tree.inFlight != 0 {
		t.Errorf("%d reads still in flight when Build returned", tree.inFlight)
	}
}

func TestACancelledContextStopsAColdBuild(t *testing.T) {
	tree := newSlowTree(200, 50*time.Millisecond, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			tree.mu.Lock()
			started := tree.started
			tree.mu.Unlock()
			if started >= 3 {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	_, err := inventory.Build(ctx, tree)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Build error = %v, want a cancellation", err)
	}
	tree.mu.Lock()
	defer tree.mu.Unlock()
	if tree.inFlight != 0 {
		t.Errorf("%d reads still in flight when Build returned", tree.inFlight)
	}
	if tree.started >= len(tree.paths) {
		t.Errorf("%d reads started, want fewer than the %d files", tree.started, len(tree.paths))
	}
}
