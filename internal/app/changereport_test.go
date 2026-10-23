package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/application/checks"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

func manifestOf(t *testing.T, paths ...string) artifact.Manifest {
	t.Helper()
	var entries []artifact.Entry
	for _, p := range paths {
		entries = append(entries, artifact.Entry{Path: p, Content: artifact.Hash([]byte("content of " + p))})
	}
	return bootMust(artifact.NewManifest(entries))
}

// The manifest of a revision is kept in the content store under its own digest, which the store checks the bytes against, and read back by it.
func TestAManifestIsKeptInTheContentStoreUnderItsOwnDigest(t *testing.T) {
	many := make([]string, 50)
	for i := range many {
		many[i] = fmt.Sprintf("tests/case_%02d_test.go", 49-i)
	}
	for name, m := range map[string]artifact.Manifest{
		"empty": manifestOf(t), "one file": manifestOf(t, "tests/a_test.go"), "unicode": manifestOf(t, "tests/日本語_test.go", "tests/a b.go"), "fifty files": manifestOf(t, many...),
	} {
		t.Run(name, func(t *testing.T) {
			store := bootMust(filesystem.NewStore(t.TempDir()))
			if err := storeManifest(t.Context(), store, m); err != nil {
				t.Fatalf("store: %v", err)
			}
			raw, err := store.Read(t.Context(), m.Digest())
			if err != nil {
				t.Fatalf("read it back by the digest of the manifest: %v", err)
			}
			back, err := artifact.ParseManifest(raw)
			if err != nil || back.Digest() != m.Digest() || fmt.Sprint(back.Entries()) != fmt.Sprint(m.Entries()) {
				t.Fatalf("parse = %v, %v; want the manifest back", back.Entries(), err)
			}
		})
	}
}

func revisionRef(proposal, revision string) contract.ProposalReference {
	return contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: defaultSuite,
		ProposalID: contract.ProposalID(proposal), RevisionID: contract.ProposalRevisionID(revision)}
}

// reportWorld is a canonical V1 and a pull request whose head is the revision P2-R1: tests/a_test.go changed from "v1" to "v2".
func reportWorld(t *testing.T) (*cjJobWorld, *changeReports) {
	t.Helper()
	w := newCJWorld(t, true)
	w.protectedPull()
	return w, newChangeReports(w.suites, w.content)
}

// manifestDigestOf is the digest of the manifest of a revision, as the proposal binds it.
func manifestDigestOf(t *testing.T, w *cjJobWorld, ref contract.ProposalReference) artifact.Digest {
	t.Helper()
	var digest artifact.Digest
	err := w.suites.Do(t.Context(), ref.ProjectID, ref.SuiteID, func(ctx context.Context, tx governance.Tx) error {
		proposal, _, err := tx.Proposal(ctx, ref.ProposalID)
		for _, revision := range proposal.Revisions() {
			if revision.Binding().Reference().RevisionID == ref.RevisionID {
				digest = revision.Binding().ManifestDigest()
			}
		}
		return err
	})
	if err != nil || digest.IsZero() {
		t.Fatalf("manifest digest of %v: %v, %v", ref, digest, err)
	}
	return digest
}

func hunkText(f report.File) string {
	var lines []string
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			lines = append(lines, string(rune(l.Kind))+l.Text)
		}
	}
	return strings.Join(lines, "\n")
}

func TestARevisionReportComparesTheManifestOfTheRevisionWithTheBaseCanonical(t *testing.T) {
	_, reports := reportWorld(t)

	got, err := reports.revisionReport(t.Context(), revisionRef("P2", "R1"))

	if err != nil {
		t.Fatalf("revisionReport: %v", err)
	}
	if got.Base != "V1" || len(got.Report.Files) != 1 {
		t.Fatalf("report = %+v, want the base V1 and one file", got)
	}
	f := got.Report.Files[0]
	if f.Path != "tests/a_test.go" || f.Status != report.Modified || !f.CountsKnown || f.LinesAdded != 1 || f.LinesRemoved != 1 || hunkText(f) != "-v1\n+v2" {
		t.Fatalf("file = %+v, want tests/a_test.go modified, +1/-1, from v1 to v2", f)
	}
}

func TestAnOldRevisionIsRebuiltAfterALaterOneArrived(t *testing.T) {
	w, reports := reportWorld(t)
	w.tree(cjHead3, bootFiles("v3"))
	w.sync(map[int]string{4: bootHead, 5: cjHead3})
	w.mustCheck()
	if keys := w.revisionKeys(); len(keys) != 2 {
		t.Fatalf("revision comments %v, want the one of each revision", keys)
	}

	first, err := reports.revisionReport(t.Context(), revisionRef("P2", "R1"))
	if err != nil {
		t.Fatalf("revisionReport of R1: %v", err)
	}
	second, err := reports.revisionReport(t.Context(), revisionRef("P2", "R2"))
	if err != nil {
		t.Fatalf("revisionReport of R2: %v", err)
	}

	if len(first.Report.Files) != 1 || len(second.Report.Files) != 1 {
		t.Fatalf("reports %+v and %+v, want one file each", first, second)
	}
	if got := hunkText(first.Report.Files[0]); got != "-v1\n+v2" {
		t.Errorf("R1 = %q, want the change to v2 that R1 proposed", got)
	}
	if got := hunkText(second.Report.Files[0]); got != "-v1\n+v3" {
		t.Errorf("R2 = %q, want the change to v3", got)
	}
}

func TestARevisionReportIsNotFoundForWhatIsNotAChangeProposalRevisionWithAStoredManifest(t *testing.T) {
	w, reports := reportWorld(t)
	legacy := manifestDigestOf(t, w, revisionRef("P2", "R1"))
	for name, tc := range map[string]struct {
		ref    contract.ProposalReference
		forget bool
	}{
		"an unknown proposal":                      {ref: revisionRef("P9", "R1")},
		"an unknown revision":                      {ref: revisionRef("P2", "R9")},
		"the baseline, which has no base":          {ref: revisionRef("P1", "R1")},
		"a revision whose manifest was not stored": {ref: revisionRef("P2", "R1"), forget: true},
		"an unknown suite":                         {ref: contract.ProposalReference{ProjectID: owner.ProjectID(bootRepoID), SuiteID: "other", ProposalID: "P2", RevisionID: "R1"}},
		"an unknown project":                       {ref: contract.ProposalReference{ProjectID: "github-repo:77", SuiteID: defaultSuite, ProposalID: "P2", RevisionID: "R1"}},
	} {
		t.Run(name, func(t *testing.T) {
			if tc.forget {
				saved := w.content.bytes[legacy.String()]
				delete(w.content.bytes, legacy.String())
				t.Cleanup(func() { w.content.bytes[legacy.String()] = saved })
			}
			if _, err := reports.revisionReport(t.Context(), tc.ref); !errors.Is(err, governance.ErrNotFound) {
				t.Fatalf("revisionReport = %v, want governance.ErrNotFound", err)
			}
		})
	}
}

func TestARevisionReportFailsWhenAFileOfTheRevisionIsMissing(t *testing.T) {
	w, reports := reportWorld(t)
	delete(w.content.bytes, artifact.Hash([]byte("v2")).String())

	_, err := reports.revisionReport(t.Context(), revisionRef("P2", "R1"))

	if !errors.Is(err, fs.ErrNotExist) || errors.Is(err, governance.ErrNotFound) {
		t.Fatalf("revisionReport = %v, want an error that wraps fs.ErrNotExist and is not ErrNotFound: the revision exists, its bytes do not", err)
	}
}

func TestARevisionReportReadsNoMoreThanItsBudget(t *testing.T) {
	_, reports := reportWorld(t)
	reports.budget = len("v1") + len("v2") - 1

	if _, err := reports.revisionReport(t.Context(), revisionRef("P2", "R1")); !errors.Is(err, errReportTooLarge) {
		t.Fatalf("revisionReport = %v, want errReportTooLarge", err)
	}
	reports.budget = len("v1") + len("v2")
	if _, err := reports.revisionReport(t.Context(), revisionRef("P2", "R1")); err != nil {
		t.Fatalf("revisionReport with exactly the bytes of both sides = %v, want a report", err)
	}
}

func TestARevisionReportReadsOnlyTheFilesThatDiffer(t *testing.T) {
	w, _ := reportWorld(t)
	var read []artifact.Digest
	reports := newChangeReports(w.suites, readFunc(func(ctx context.Context, d artifact.Digest) ([]byte, error) {
		read = append(read, d)
		return w.content.Read(ctx, d)
	}))
	manifest := manifestDigestOf(t, w, revisionRef("P2", "R1"))

	if _, err := reports.revisionReport(t.Context(), revisionRef("P2", "R1")); err != nil {
		t.Fatal(err)
	}

	want := map[artifact.Digest]bool{manifest: true, artifact.Hash([]byte("v1")): true, artifact.Hash([]byte("v2")): true}
	if len(read) != len(want) {
		t.Fatalf("read %v, want the manifest and the two sides of the one file that differs", read)
	}
	for _, d := range read {
		if !want[d] {
			t.Errorf("read %s, which is neither the manifest nor a side of the changed file", d)
		}
	}
}

type readFunc func(context.Context, artifact.Digest) ([]byte, error)

func (f readFunc) Read(ctx context.Context, d artifact.Digest) ([]byte, error) { return f(ctx, d) }

func TestOneReportIsBuiltAtATimeAndAWaitingBuildHonorsItsContext(t *testing.T) {
	w, _ := reportWorld(t)
	entered, release := make(chan struct{}), make(chan struct{})
	reports := newChangeReports(w.suites, readFunc(func(ctx context.Context, d artifact.Digest) ([]byte, error) {
		select {
		case entered <- struct{}{}:
			<-release
		default:
		}
		return w.content.Read(ctx, d)
	}))
	first := make(chan error, 1)
	go func() {
		_, err := reports.revisionReport(context.Background(), revisionRef("P2", "R1"))
		first <- err
	}()
	select { // the first build holds the gate, blocked in its first read
	case <-entered:
	case err := <-first:
		t.Fatalf("the first build ended without reading anything: %v", err)
	}

	waiting, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() {
		_, err := reports.revisionReport(waiting, revisionRef("P2", "R1"))
		second <- err
	}()
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Fatalf("the waiting build = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("the first build = %v, want its report", err)
	}
	if _, err := reports.revisionReport(context.Background(), revisionRef("P2", "R1")); err != nil {
		t.Fatalf("a build after the gate was released = %v", err)
	}
}

// The manifest is stored before the revision commits: a store that refuses it leaves no revision, no comment and no check of it behind, and the next run records them.
func TestARevisionIsNotRecordedWhenItsManifestCannotBeStored(t *testing.T) {
	w := newCJWorld(t, true)
	w.tree(cjHead2, bootFiles("v2"))
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	errDisk := errors.New("the disk is full")
	w.content.putErr = errDisk

	if err := w.runChecks(); !errors.Is(err, errDisk) {
		t.Fatalf("checks run = %v, want the failure to store the manifest", err)
	}
	if keys := w.revisionKeys(); len(keys) != 0 {
		t.Fatalf("revision comments %v after a manifest that could not be stored, want none", keys)
	}
	if record, found := w.record(5); found && record.Reason == "approval-missing" {
		t.Fatalf("check %+v after a manifest that could not be stored, want no revision to approve", record.Result)
	}

	w.content.putErr = nil
	w.mustCheck()
	w.want(5, checks.InProgress, "approval-missing", 1)
	if _, err := newChangeReports(w.suites, w.content).revisionReport(t.Context(), revisionRef("P2", "R1")); err != nil {
		t.Fatalf("revisionReport after the retry: %v", err)
	}
}

func TestARevisionReportCountsEveryKindOfChange(t *testing.T) {
	w := newCJWorld(t, true)
	files := bootFiles("v1")
	delete(files, "tests/a_test.go")
	files["tests/b_test.go"] = "package b\n"
	w.tree(cjHead2, files)
	w.sync(map[int]string{4: bootHead, 5: cjHead2})
	w.mustCheck()

	got, err := newChangeReports(w.suites, w.content).revisionReport(t.Context(), revisionRef("P2", "R1"))

	if err != nil {
		t.Fatalf("revisionReport: %v", err)
	}
	var summary []string
	for _, f := range got.Report.Files {
		summary = append(summary, fmt.Sprintf("%s %s +%d/-%d", f.Status, f.Path, f.LinesAdded, f.LinesRemoved))
	}
	if want := "removed tests/a_test.go +0/-1, added tests/b_test.go +1/-0"; strings.Join(summary, ", ") != want {
		t.Fatalf("report %q, want %q", strings.Join(summary, ", "), want)
	}
}

func TestARevisionReportRefusesAManifestThatIsNotTheOneItsDigestNames(t *testing.T) {
	w, reports := reportWorld(t)
	digest := manifestDigestOf(t, w, revisionRef("P2", "R1"))
	w.content.bytes[digest.String()] = []byte("not a manifest")

	_, err := reports.revisionReport(t.Context(), revisionRef("P2", "R1"))

	if !errors.Is(err, artifact.ErrInvalidEncoding) || errors.Is(err, governance.ErrNotFound) {
		t.Fatalf("revisionReport = %v, want an invalid encoding: the revision exists, its manifest is damaged", err)
	}
}

func TestARevisionReportRefusesTheManifestOfAnotherRevision(t *testing.T) {
	w, reports := reportWorld(t)
	digest := manifestDigestOf(t, w, revisionRef("P2", "R1"))
	w.content.bytes[digest.String()] = manifestOf(t, "tests/other_test.go").Encoding()

	_, err := reports.revisionReport(t.Context(), revisionRef("P2", "R1"))

	if !errors.Is(err, artifact.ErrInvalidEncoding) {
		t.Fatalf("revisionReport = %v, want an invalid encoding: the bytes are the manifest of another revision", err)
	}
}
