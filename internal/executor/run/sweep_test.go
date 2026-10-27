package run

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// recorder is a commandRunner that answers `ps` with a list of names and records every command.
type recorder struct {
	listing string
	psErr   error
	rmErr   error
	calls   [][]string
}

func (r *recorder) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	switch args[0] {
	case "ps":
		return []byte(r.listing), r.psErr
	case "rm":
		return nil, r.rmErr
	}
	return nil, fmt.Errorf("unexpected command %v", args)
}

func TestSweepRemovesTheLeftoverSandboxContainersAndNothingElse(t *testing.T) {
	r := &recorder{listing: "suiteward-sandbox-ABCDEF234567\nsuiteward-sandbox-QWERTY\nsuiteward-sandbox-\nmy-database\nsuiteward-sandbox-x y\n--force\nsuiteward-sandbox-A/../B\n\n"}
	removed, err := sweep(context.Background(), r.run, "docker")
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	want := []string{"suiteward-sandbox-ABCDEF234567", "suiteward-sandbox-QWERTY"}
	if !slices.Equal(removed, want) {
		t.Errorf("sweep removed %v; want %v", removed, want)
	}
	if len(r.calls) != 2 {
		t.Fatalf("the commands were %v", r.calls)
	}
	if got, want := r.calls[0], []string{"docker", "ps", "--all", "--filter", "name=^suiteward-sandbox-", "--format", "{{.Names}}"}; !slices.Equal(got, want) {
		t.Errorf("the listing was %q; want %q", got, want)
	}
	if got, want := r.calls[1], []string{"docker", "rm", "--force", "suiteward-sandbox-ABCDEF234567", "suiteward-sandbox-QWERTY"}; !slices.Equal(got, want) {
		t.Errorf("the removal was %q; want %q", got, want)
	}
}

func TestSweepWithNothingLeftRemovesNothing(t *testing.T) {
	r := &recorder{}
	if removed, err := sweep(context.Background(), r.run, "podman"); err != nil || len(removed) != 0 || len(r.calls) != 1 {
		t.Errorf("sweep = %v, %v after %v; want one listing and no removal", removed, err, r.calls)
	}
}

func TestSweepRemovesManyContainersInGroupsThatFitACommandLine(t *testing.T) {
	var names []string
	for i := range 120 {
		names = append(names, fmt.Sprintf("suiteward-sandbox-%026d", i))
	}
	r := &recorder{listing: strings.Join(names, "\n")}
	removed, err := sweep(context.Background(), r.run, "docker")
	if err != nil || len(removed) != 120 {
		t.Fatalf("sweep = %d names, %v", len(removed), err)
	}
	if len(r.calls) != 4 {
		t.Fatalf("%d commands for 120 containers; want a listing and three removals", len(r.calls))
	}
	for _, c := range r.calls[1:] {
		if n := len(c) - 3; n > 50 {
			t.Errorf("a removal names %d containers", n)
		}
	}
}

func TestSweepReportsAListingOrARemovalThatFails(t *testing.T) {
	boom := errors.New("the engine would not answer")
	if _, err := sweep(context.Background(), (&recorder{psErr: boom}).run, "docker"); !errors.Is(err, boom) {
		t.Errorf("a failed listing gave %v", err)
	}
	r := &recorder{listing: "suiteward-sandbox-ABC\n", rmErr: boom}
	if _, err := sweep(context.Background(), r.run, "docker"); !errors.Is(err, boom) {
		t.Errorf("a failed removal gave %v", err)
	}
}
