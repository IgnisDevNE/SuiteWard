package app

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/filesystem"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity"
	"github.com/IgnisDevNE/SuiteWard/internal/application/integrity/integritytest"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

// intWorld is a repository whose canonical V1 holds the protected files of bootFiles("v1"), stored in a real
// artifact store in a temporary directory.
type intWorld struct {
	*bootWorld
	dir      string
	files    *filesystem.Store
	verdicts *integritytest.Memory
	job      *integrityJob
	logs     *bytes.Buffer
	clock    time.Time
}

// object is the file of the object that holds content.
func (w *intWorld) object(content string) string {
	return filepath.Join(w.dir, "sha256-"+strings.TrimPrefix(artifact.Hash([]byte(content)).String(), "sha256:"))
}

func newIntWorld(t *testing.T, canonical bool) *intWorld {
	t.Helper()
	var b *bootWorld
	if canonical {
		b = approvedWorld(t)
		b.mustRun()
		if _, found := b.canonical(); !found {
			t.Fatalf("the setup did not bootstrap the canonical\nlogs: %s", b.logs)
		}
	} else {
		b = newBootWorld(t)
	}
	dir := t.TempDir()
	files, err := filesystem.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range bootFiles("v1") {
		if err := files.Put(t.Context(), artifact.Hash([]byte(content)), strings.NewReader(content)); err != nil {
			t.Fatal(err)
		}
	}
	w := &intWorld{bootWorld: b, dir: dir, files: files, verdicts: integritytest.NewMemory(), logs: &bytes.Buffer{}, clock: bootTime.Add(3 * time.Hour)}
	w.job = w.newJob(files)
	return w
}

func (w *intWorld) newJob(content contentVerifier) *integrityJob {
	return &integrityJob{owners: w.owners, suites: w.suites, content: content, store: w.verdicts, logger: slog.New(slog.NewJSONHandler(w.logs, nil)), now: func() time.Time { return w.clock }}
}

func (w *intWorld) run() error {
	w.clock = w.clock.Add(time.Hour)
	return w.job.run(w.t.Context())
}

func (w *intWorld) mustRun() {
	w.t.Helper()
	if err := w.run(); err != nil {
		w.t.Fatalf("integrity run: %v\nlogs: %s", err, w.logs)
	}
}

// latest returns the stored verdict of the main project's default Suite.
func (w *intWorld) latest() (integrity.Record, bool) {
	w.t.Helper()
	record, found, err := w.verdicts.Latest(w.t.Context(), w.project(), "default")
	if err != nil {
		w.t.Fatal(err)
	}
	return record, found
}

func (w *intWorld) want(verdict integrity.Verdict) integrity.Record {
	w.t.Helper()
	record, found := w.latest()
	if !found || record.Verdict != verdict || record.Version != "V1" || !record.CheckedAt.Equal(w.clock) {
		w.t.Fatalf("verdict %+v, found %v; want %s of V1 checked at %v\nlogs: %s", record, found, verdict, w.clock, w.logs)
	}
	return record
}

func (w *intWorld) wantNothing() {
	w.t.Helper()
	if record, found := w.latest(); found {
		w.t.Fatalf("a verdict was recorded: %+v", record)
	}
}

// faultyVerifier fails the verification of the digests of errs and passes the rest to the artifact store.
type faultyVerifier struct {
	contentVerifier
	errs map[artifact.Digest]error
}

func (f faultyVerifier) Verify(ctx context.Context, digest artifact.Digest) error {
	if err := f.errs[digest]; err != nil {
		return err
	}
	return f.contentVerifier.Verify(ctx, digest)
}

func TestIntegrityRecordsPassedWhenEveryDigestVerifies(t *testing.T) {
	w := newIntWorld(t, true)

	w.mustRun()

	w.want(integrity.Passed)
	if strings.Contains(w.logs.String(), `"level":"ERROR"`) {
		t.Fatalf("a passed verification logged an error: %s", w.logs)
	}
}

func TestIntegrityRecordsFailedForCorruptBytesAndLogsOnlyTheProjectAndTheVersion(t *testing.T) {
	w := newIntWorld(t, true)
	if err := os.WriteFile(w.object(bootFiles("v1")["tests/a_test.go"]), []byte(bootSecret), 0o600); err != nil {
		t.Fatal(err)
	}

	w.mustRun()

	w.want(integrity.Failed)
	logged := w.logs.String()
	if !strings.Contains(logged, `"level":"ERROR"`) || !strings.Contains(logged, `"project":"`+string(w.project())+`"`) || !strings.Contains(logged, `"version":"V1"`) {
		t.Fatalf("the failure is not logged at Error with the project and the version: %s", logged)
	}
	if strings.Contains(logged, bootSecret) || strings.Contains(logged, "sha256") {
		t.Fatalf("the log carries artifact bytes or a digest: %s", logged)
	}
}

func TestIntegrityRecordsFailedForAMissingObject(t *testing.T) {
	w := newIntWorld(t, true)
	if err := os.Remove(w.object(bootFiles("v1")[".github/workflows/ci.yml"])); err != nil {
		t.Fatal(err)
	}

	w.mustRun()

	w.want(integrity.Failed)
}

func TestIntegrityRecordsUnavailableForAReadErrorAndNeverLogsItsText(t *testing.T) {
	w := newIntWorld(t, true)
	digest := artifact.Hash([]byte(bootFiles("v1")[".suiteward.yml"]))
	w.job = w.newJob(faultyVerifier{w.files, map[artifact.Digest]error{digest: errors.New("disk: " + bootSecret)}})

	err := w.run()

	if err != nil {
		t.Fatalf("an unreadable store failed the run: %v", err)
	}
	w.want(integrity.Unavailable)
	if strings.Contains(w.logs.String(), bootSecret) {
		t.Fatalf("the log carries the text of the read error: %s", w.logs)
	}
}

func TestIntegrityFailedOutweighsAnUnreadableEntry(t *testing.T) {
	w := newIntWorld(t, true)
	unreadable := artifact.Hash([]byte(bootFiles("v1")[".suiteward.yml"]))
	if err := os.Remove(w.object(bootFiles("v1")["tests/a_test.go"])); err != nil {
		t.Fatal(err)
	}
	w.job = w.newJob(faultyVerifier{w.files, map[artifact.Digest]error{unreadable: errors.New("busy")}})

	w.mustRun()

	w.want(integrity.Failed)
}

func TestIntegrityWritesNothingWithoutASuiteOrACanonical(t *testing.T) {
	t.Run("no suite", func(t *testing.T) {
		w := newIntWorld(t, false)

		w.mustRun()

		w.wantNothing()
	})
	t.Run("a suite without a canonical", func(t *testing.T) {
		w := newIntWorld(t, false)
		w.seedSuite(bootRepoID, "branch:main")

		w.mustRun()

		w.wantNothing()
	})
	t.Run("no enrolled owner", func(t *testing.T) {
		w := newIntWorld(t, true)
		w.job.owners = newBootWorld(t, notEnrolled).owners

		w.mustRun()

		w.wantNothing()
	})
}

func TestIntegrityFailedSurvivesARestartAndAnUnreadableStore(t *testing.T) {
	w := newIntWorld(t, true)
	object := w.object(bootFiles("v1")["tests/a_test.go"])
	if err := os.WriteFile(object, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	w.mustRun()
	w.want(integrity.Failed)

	digest := artifact.Hash([]byte(bootFiles("v1")["tests/a_test.go"]))
	w.job = w.newJob(faultyVerifier{w.files, map[artifact.Digest]error{digest: errors.New("busy")}}) // a new process, whose store is unreadable
	w.mustRun()

	record, _ := w.latest()
	if record.Verdict != integrity.Failed || !record.CheckedAt.Equal(w.clock) {
		t.Fatalf("verdict %+v after an unreadable run; want failed, checked at %v", record, w.clock)
	}
}

func TestIntegrityPassedClearsAFailedVerdictOnceTheBytesAreBack(t *testing.T) {
	w := newIntWorld(t, true)
	content := bootFiles("v1")["tests/a_test.go"]
	object := w.object(content)
	if err := os.Remove(object); err != nil {
		t.Fatal(err)
	}
	w.mustRun()
	w.want(integrity.Failed)

	if err := w.files.Put(t.Context(), artifact.Hash([]byte(content)), strings.NewReader(content)); err != nil {
		t.Fatal(err)
	}
	w.mustRun()

	w.want(integrity.Passed)
}

func TestIntegrityRecordsNothingWhenTheRunIsCancelled(t *testing.T) {
	w := newIntWorld(t, true)
	ctx, cancel := context.WithCancel(t.Context())
	digest := artifact.Hash([]byte(bootFiles("v1")["tests/a_test.go"]))
	w.job = w.newJob(cancellingVerifier{cancel: cancel, digest: digest})

	err := w.job.run(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("run error %v, want the cancellation", err)
	}
	w.wantNothing() // a shutdown must not turn a confirmed verdict into unavailable
}

// cancellingVerifier cancels the run while verifying digest and then reports what a cancelled read reports.
type cancellingVerifier struct {
	cancel func()
	digest artifact.Digest
}

func (c cancellingVerifier) Verify(ctx context.Context, digest artifact.Digest) error {
	if digest == c.digest {
		c.cancel()
		return ctx.Err()
	}
	return nil
}

func TestIntegrityFailsTheRunWhenTheVerdictCannotBeRecorded(t *testing.T) {
	w := newIntWorld(t, true)
	w.job.store = recordFailing{w.verdicts}

	err := w.run()

	if !errors.Is(err, errBootStore) {
		t.Fatalf("run error %v, want the store error", err)
	}
}

// recordFailing fails every write.
type recordFailing struct{ *integritytest.Memory }

func (recordFailing) Record(context.Context, integrity.Record) error { return errBootStore }
