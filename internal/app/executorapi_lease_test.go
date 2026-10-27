package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/application/execution"
	domain "github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// execLease is the lease answer of the contract, decoded strictly: a field that the table does not list fails the decoding.
type execLease struct {
	JobID          int64  `json:"jobId"`
	LeaseID        string `json:"leaseId"`
	LeaseExpiresAt string `json:"leaseExpiresAt"`
	DeadlineAt     string `json:"deadlineAt"`
	Plan           string `json:"plan"`
	Pull           int    `json:"pull"`
	Profile        struct {
		ID    string `json:"id"`
		Image string `json:"image"`
	} `json:"profile"`
	Head     *execSide `json:"head"`
	Base     *execSide `json:"base"`
	Manifest struct {
		Source string `json:"source"`
		Digest string `json:"digest"`
		Files  []struct {
			Path   string `json:"path"`
			Digest string `json:"digest"`
		} `json:"files"`
	} `json:"manifest"`
	Declaration *string `json:"declaration"`
	Limits      struct {
		ArchiveBytes       int64 `json:"archiveBytes"`
		FileBytes          int64 `json:"fileBytes"`
		TestTimeoutSeconds int   `json:"testTimeoutSeconds"`
		MemoryMiB          int   `json:"memoryMiB"`
		CPUs               int   `json:"cpus"`
		PIDs               int   `json:"pids"`
		TmpfsMiB           int   `json:"tmpfsMiB"`
		PrefetchModules    int   `json:"prefetchModules"`
		PrefetchBytes      int64 `json:"prefetchBytes"`
		Reruns             int   `json:"reruns"`
	} `json:"limits"`
}

type execSide struct {
	SHA  string      `json:"sha"`
	Tree []treeEntry `json:"tree"`
}

func decodeLease(t *testing.T, r execReply) execLease {
	t.Helper()
	var l execLease
	dec := json.NewDecoder(bytes.NewReader(r.body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		t.Fatalf("lease answer: %v", err)
	}
	return l
}

func (e *execTestEnv) lease(mods ...execMod) execReply {
	e.t.Helper()
	return e.send(http.MethodPost, "/lease", nil, mods...)
}

func execUTC(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil || !strings.HasSuffix(s, "Z") {
		t.Fatalf("time %q = %v; want RFC 3339 in UTC", s, err)
	}
	return at
}

func TestLeaseAnswers204WhenNoJobIsReady(t *testing.T) {
	e := newExecEnv(t)

	got := e.lease()

	if got.status != http.StatusNoContent || len(got.body) != 0 {
		t.Fatalf("lease = %d %q; want 204 with no body", got.status, got.body)
	}
}

func TestLeaseGrantsACanonicalJobWithEveryFieldOfTheContract(t *testing.T) {
	e := newExecEnv(t)
	job := e.canonicalJob(1, defaultFiles)
	head := []treeEntry{{Path: "contract.go", Mode: "100644", SHA: strings.Repeat("c", 40)}, {Path: "run.sh", Mode: "100755", SHA: strings.Repeat("d", 40)}, {Path: "link", Mode: "120000", SHA: strings.Repeat("e", 40)}}
	e.trees.set(execSHA(1), head)

	got := e.lease()

	if got.status != http.StatusOK || !strings.HasPrefix(got.header.Get("Content-Type"), "application/json") {
		t.Fatalf("lease = %d %s (%s); want 200 JSON", got.status, got.text(), got.header.Get("Content-Type"))
	}
	l := decodeLease(t, got)
	if l.JobID != job.ID || l.Plan != "canonical" || l.Pull != 3 || l.Profile.ID != "go" || l.Profile.Image != goprofile.Image {
		t.Errorf("lease = %+v; want job %d, canonical, pull 3, the go profile with the pinned image", l, job.ID)
	}
	e.wantLeaseTimes(l)
	if l.Head == nil || l.Head.SHA != execSHA(1) || !slices.Equal(l.Head.Tree, head) {
		t.Errorf("head = %+v; want the commit and its three entries", l.Head)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(got.body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, present := raw["base"]; present {
		t.Error("a canonical job has a base in its lease answer")
	}
	e.wantLeaseManifest(l, defaultFiles)
	wantLimits := `{"archiveBytes":268435456,"fileBytes":8388608,"testTimeoutSeconds":600,"memoryMiB":3072,"cpus":4,"pids":512,"tmpfsMiB":1024,"prefetchModules":2000,"prefetchBytes":536870912,"reruns":2}`
	if !jsonEqual(t, string(execJSON(t, l.Limits)), wantLimits) {
		t.Errorf("limits = %s; want %s", execJSON(t, l.Limits), wantLimits)
	}
	stored := e.job(job.ID)
	if stored.State != execution.StateLeased || stored.Attempts != 1 || stored.LeaseID != l.LeaseID {
		t.Errorf("stored job = %+v; want it leased once with the lease of the answer", stored)
	}
	if asked := e.trees.asked(); len(asked) != 1 || asked[0].repo != execRepoName || asked[0].sha != execSHA(1) {
		t.Errorf("tree reads = %+v; want one, of the head of %s", asked, execRepoName)
	}
}

// wantLeaseTimes checks the lease id and its two times against the clock.
func (e *execTestEnv) wantLeaseTimes(l execLease) {
	e.t.Helper()
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(l.LeaseID) {
		e.t.Errorf("leaseId = %q; want 32 lower-case hexadecimal digits", l.LeaseID)
	}
	if want := e.clock.Now().Add(execution.LeaseTTL); !execUTC(e.t, l.LeaseExpiresAt).Equal(want) {
		e.t.Errorf("leaseExpiresAt = %s; want %v", l.LeaseExpiresAt, want)
	}
	if want := e.clock.Now().Add(e.times.JobTimeout); !execUTC(e.t, l.DeadlineAt).Equal(want) {
		e.t.Errorf("deadlineAt = %s; want %v, the job timeout from now", l.DeadlineAt, want)
	}
}

// wantLeaseManifest checks the manifest of the answer and the declaration it carries against the files of the job.
func (e *execTestEnv) wantLeaseManifest(l execLease, files map[string]string) {
	e.t.Helper()
	m := e.manifest(files)
	if l.Manifest.Source != "V1" || l.Manifest.Digest != m.Digest().String() || len(l.Manifest.Files) != len(m.Entries()) {
		e.t.Fatalf("manifest = %+v; want V1, %s and its %d files", l.Manifest, m.Digest(), len(m.Entries()))
	}
	for i, entry := range m.Entries() {
		if l.Manifest.Files[i].Path != entry.Path || l.Manifest.Files[i].Digest != entry.Content.String() {
			e.t.Errorf("manifest.files[%d] = %+v; want %s %s", i, l.Manifest.Files[i], entry.Path, entry.Content)
		}
	}
	if l.Declaration == nil || *l.Declaration != base64.StdEncoding.EncodeToString([]byte(files[".suiteward.yml"])) {
		e.t.Errorf("declaration = %v; want the base64 of the .suiteward.yml the manifest names", l.Declaration)
	}
}

func TestLeaseGrantsAReceiptJobWithItsBase(t *testing.T) {
	e := newExecEnv(t)
	job := e.receiptJob(2, defaultFiles)
	e.trees.set(execSHA(2), []treeEntry{{Path: "head.go", Mode: "100644", SHA: strings.Repeat("1", 40)}})
	e.trees.set(execSHA(1002), []treeEntry{{Path: "base.go", Mode: "100664", SHA: strings.Repeat("2", 40)}, {Path: "sub", Mode: "160000", SHA: strings.Repeat("3", 40)}})

	got := e.lease()

	l := decodeLease(t, got)
	if got.status != http.StatusOK || l.JobID != job.ID || l.Plan != "receipt" || l.Manifest.Source != "P1-R2" {
		t.Fatalf("lease = %d %s; want the receipt job over the manifest of its revision", got.status, got.text())
	}
	if l.Head == nil || l.Head.SHA != execSHA(2) || len(l.Head.Tree) != 1 || l.Head.Tree[0].Path != "head.go" {
		t.Errorf("head = %+v; want the head and its tree", l.Head)
	}
	if l.Base == nil || l.Base.SHA != execSHA(1002) || len(l.Base.Tree) != 2 || l.Base.Tree[0].Mode != "100664" || l.Base.Tree[1].Mode != "160000" {
		t.Errorf("base = %+v; want the merge base and its tree, legacy mode and submodule included", l.Base)
	}
	if asked := e.trees.asked(); len(asked) != 2 {
		t.Errorf("tree reads = %+v; want the head and the base", asked)
	}
}

func TestLeaseAnswerHasNoDeclarationWhenTheManifestHasNone(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, map[string]string{"contract.go": "package contract\n"})

	got := e.lease()

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(got.body, &raw); err != nil || got.status != http.StatusOK {
		t.Fatalf("lease = %d %s (%v)", got.status, got.text(), err)
	}
	if declaration, present := raw["declaration"]; present {
		t.Errorf("declaration = %s; want the field absent, so that the executor uses the scope of no declaration", declaration)
	}
}

// An empty .suiteward.yml is a declaration (it names no execution block), not an absent one.
func TestLeaseAnswerKeepsAnEmptyDeclaration(t *testing.T) {
	e := newExecEnv(t)
	e.canonicalJob(1, map[string]string{".suiteward.yml": ""})

	got := e.lease()

	if l := decodeLease(t, got); got.status != http.StatusOK || l.Declaration == nil || *l.Declaration != "" {
		t.Errorf("lease = %d, declaration %v; want 200 with an empty declaration present", got.status, l.Declaration)
	}
}

func TestLeaseGrantsTheOldestJobFirstAndANewLeaseEachTime(t *testing.T) {
	e := newExecEnv(t)
	first, second := e.canonicalJob(1, defaultFiles), e.canonicalJob(2, defaultFiles)

	a, b := decodeLease(t, e.lease()), decodeLease(t, e.lease())

	if a.JobID != first.ID || b.JobID != second.ID || a.LeaseID == b.LeaseID {
		t.Errorf("leases = %d %s and %d %s; want the jobs in order, each with a lease of its own", a.JobID, a.LeaseID, b.JobID, b.LeaseID)
	}
	if got := e.lease(); got.status != http.StatusNoContent {
		t.Errorf("a third lease = %d %s; want 204, both jobs are leased", got.status, got.text())
	}
}

// A lost lease brings the job back: the lease runs out, the attempt is a failure, and the job is leased again after its backoff with a new lease.
func TestLeaseGrantsALostJobAgainWithANewLease(t *testing.T) {
	e := newExecEnv(t)
	job := e.canonicalJob(1, defaultFiles)
	first := decodeLease(t, e.lease())
	e.clock.Advance(execution.LeaseTTL)
	if got := e.lease(); got.status != http.StatusNoContent { // the lapse is a failure and the job waits 30 s
		t.Fatalf("lease right after the expiry = %d %s; want 204, the job waits for its backoff", got.status, got.text())
	}
	e.clock.Advance(30 * time.Second)

	second := decodeLease(t, e.lease())

	if second.JobID != job.ID || second.LeaseID == first.LeaseID {
		t.Errorf("second lease = %+v; want the same job with a new lease id", second)
	}
	if stored := e.job(job.ID); stored.Attempts != 2 || stored.Failures != 1 {
		t.Errorf("job = %+v; want two attempts and one failure", stored)
	}
}

func TestLeaseAcceptsNoBodyOrAnEmptyObject(t *testing.T) {
	for name, body := range map[string][]byte{"no body": nil, "empty object": []byte(`{}`), "spaces around": []byte(" \n{ }\t\n")} {
		t.Run(name, func(t *testing.T) {
			e := newExecEnv(t)
			e.canonicalJob(1, defaultFiles)
			if got := e.send(http.MethodPost, "/lease", body); got.status != http.StatusOK {
				t.Errorf("lease with %q = %d %s; want 200", body, got.status, got.text())
			}
		})
	}
}

func TestLeaseRefusesAnyOtherBody(t *testing.T) {
	for name, body := range map[string]string{
		"an unknown field": `{"jobId":1}`, "an array": `[]`, "null": `null`, "a string": `"lease"`, "two objects": `{}{}`, "a trailing value": `{} 1`,
		"trailing text": `{}x`, "not JSON": `lease please`, "a large body": strings.Repeat(" ", 4096) + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			e := newExecEnv(t)
			e.canonicalJob(1, defaultFiles)
			e.wantError(e.send(http.MethodPost, "/lease", []byte(body)), http.StatusBadRequest, "invalid")
			if job := e.job(1); job.State != execution.StatePending || job.Attempts != 0 {
				t.Errorf("job = %+v; want it untouched by a refused request", job)
			}
		})
	}
}

// A lease request is the executor being seen whether it is answered with a job, 204, 502 or a refusal of its body.
func TestLeaseCountsAsTheExecutorBeingSeenWhateverTheAnswer(t *testing.T) {
	e := newExecEnv(t)
	for _, step := range []struct {
		name    string
		run     func()
		counted bool
	}{
		{"nothing", func() {}, false},
		{"204", func() { e.lease() }, true},
		{"200", func() { e.canonicalJob(1, defaultFiles); e.lease() }, true},
		{"502", func() { e.canonicalJob(2, defaultFiles); e.trees.fail(execSHA(2), errors.New("down")); e.lease() }, true},
		{"a refused body", func() { e.send(http.MethodPost, "/lease", []byte(`[]`)) }, true},
	} {
		before, _ := e.liveness.LastRequest()
		e.clock.Advance(time.Second)
		step.run()
		at, requested := e.liveness.LastRequest()
		if counted := requested && at.After(before); counted != step.counted {
			t.Errorf("%s: counted = %v (last request %v, before %v); want %v", step.name, counted, at, before, step.counted)
		}
	}
}

// A GitHub failure at lease time is a 502: the job stays pending with its reason, consumes no attempt and waits a minute.
func TestLeaseAnswers502AndKeepsTheJobWhenGitHubCannotBeRead(t *testing.T) {
	for name, fail := range map[string]struct {
		sha int
		err error
	}{
		"the head":           {1, errors.New("GitHub says: secret detail of the head")},
		"the base":           {1001, errors.New("GitHub says: secret detail of the base")},
		"a refused budget":   {1, errors.New("budget exhausted")},
		"a wrapped sentinel": {1001, fmt.Errorf("tree: %w", github.ErrInvalidResponse)},
	} {
		t.Run(name, func(t *testing.T) {
			e := newExecEnv(t)
			job := e.receiptJob(1, defaultFiles)
			e.trees.fail(execSHA(fail.sha), fail.err)

			got := e.lease()

			e.wantError(got, http.StatusBadGateway, "github")
			stored := e.job(job.ID)
			if stored.State != execution.StatePending || stored.Attempts != 0 || stored.Failures != 0 || stored.Reason != execution.ReasonGitHubUnavailable {
				t.Errorf("job = %+v; want pending with github-unavailable and no attempt or failure", stored)
			}
			if !stored.GitHubSince.Equal(e.clock.Now()) || !stored.NotBefore.Equal(e.clock.Now().Add(execution.GitHubBackoff)) {
				t.Errorf("github since %v, not before %v; want now and a minute from now", stored.GitHubSince, stored.NotBefore)
			}
			if strings.Contains(e.logs.String(), "secret detail") || strings.Contains(got.text(), "secret detail") {
				t.Errorf("GitHub's text reached the log or the answer: %s", e.logs.String())
			}
		})
	}
}

// Past EXECUTOR_GITHUB_WAIT from the first failure the job is final, by the store, and the lease goes on without it.
func TestLeaseEndsAJobWhoseGitHubReadsFailForTooLong(t *testing.T) {
	e := newExecEnv(t)
	job := e.canonicalJob(1, defaultFiles)
	e.trees.fail(execSHA(1), errors.New("down"))
	e.wantError(e.lease(), http.StatusBadGateway, "github")
	e.clock.Advance(e.times.GitHubWait)

	e.wantError(e.lease(), http.StatusBadGateway, "github") // the failure that ends it is still a 502 for this request

	if stored := e.job(job.ID); stored.State != execution.StateUnavailable || stored.Reason != execution.ReasonGitHubUnavailable {
		t.Errorf("job = %+v; want unavailable for github-unavailable", stored)
	}
}

func TestLeaseMakesATruncatedTreeFinalAndGoesOnToTheNextJob(t *testing.T) {
	e := newExecEnv(t)
	big, next := e.canonicalJob(1, defaultFiles), e.canonicalJob(2, defaultFiles)
	e.trees.fail(execSHA(1), fmt.Errorf("tree of %s: %w", execRepoName, github.ErrTreeTruncated))
	e.trees.set(execSHA(2), []treeEntry{{Path: "second.go", Mode: "100644", SHA: strings.Repeat("9", 40)}})

	got := e.lease()

	l := decodeLease(t, got)
	if got.status != http.StatusOK || l.JobID != next.ID || l.Head == nil || len(l.Head.Tree) != 1 || l.Head.Tree[0].Path != "second.go" {
		t.Fatalf("lease = %d %s; want the second job, with only its own tree", got.status, got.text())
	}
	stored := e.job(big.ID)
	if stored.State != execution.StateUnavailable || stored.Reason != execution.ReasonTooLarge || stored.Attempts != 0 {
		t.Errorf("first job = %+v; want unavailable for too-large with no attempt", stored)
	}
	if _, found, err := e.store.Result(t.Context(), big.ID); err != nil || found {
		t.Errorf("result of the too-large job = found %v, %v; want none, the service writes no row for it", found, err)
	}
}

func TestLeaseAnswers204WhenTheOnlyJobIsTooLarge(t *testing.T) {
	e := newExecEnv(t)
	job := e.canonicalJob(1, defaultFiles)
	e.trees.fail(execSHA(1), github.ErrTreeTruncated)

	if got := e.lease(); got.status != http.StatusNoContent {
		t.Errorf("lease = %d %s; want 204", got.status, got.text())
	}
	if stored := e.job(job.ID); stored.Reason != execution.ReasonTooLarge {
		t.Errorf("job = %+v; want too-large", stored)
	}
}

func execEntries(n, pathBytes int) []treeEntry {
	entries := make([]treeEntry, n)
	for i := range entries {
		path := fmt.Sprintf("%0*d", pathBytes, i)
		entries[i] = treeEntry{Path: path, Mode: "100644", SHA: strings.Repeat("a", 40)}
	}
	return entries
}

func TestLeaseRefusesATreeOfMoreThanFiftyThousandEntries(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries int
		wantOK  bool
	}{{"the limit", 50000, true}, {"one over", 50001, false}} {
		t.Run(test.name, func(t *testing.T) {
			e := newExecEnv(t)
			job := e.canonicalJob(1, defaultFiles)
			e.trees.set(execSHA(1), execEntries(test.entries, 8))

			got := e.lease()

			stored := e.job(job.ID)
			if test.wantOK && (got.status != http.StatusOK || stored.State != execution.StateLeased) {
				t.Errorf("lease = %d, job %+v; want a lease for %d entries", got.status, stored, test.entries)
			}
			if !test.wantOK && (got.status != http.StatusNoContent || stored.Reason != execution.ReasonTooLarge) {
				t.Errorf("lease = %d, job %+v; want too-large for %d entries", got.status, stored, test.entries)
			}
		})
	}
}

func TestLeaseRefusesABaseTreeOfMoreThanFiftyThousandEntries(t *testing.T) {
	e := newExecEnv(t)
	job := e.receiptJob(1, defaultFiles)
	e.trees.set(execSHA(1001), execEntries(50001, 8))

	got := e.lease()

	if stored := e.job(job.ID); got.status != http.StatusNoContent || stored.Reason != execution.ReasonTooLarge {
		t.Errorf("lease = %d, job %+v; want too-large, the limit holds for each commit", got.status, stored)
	}
}

func TestLeaseRefusesADeclarationOverOneMiB(t *testing.T) {
	for _, test := range []struct {
		name   string
		size   int
		wantOK bool
	}{{"one MiB", 1 << 20, true}, {"one byte over", 1<<20 + 1, false}} {
		t.Run(test.name, func(t *testing.T) {
			e := newExecEnv(t)
			job := e.canonicalJob(1, map[string]string{".suiteward.yml": "#" + strings.Repeat("a", test.size-1)})

			got := e.lease()

			stored := e.job(job.ID)
			if test.wantOK && (got.status != http.StatusOK || stored.State != execution.StateLeased) {
				t.Errorf("lease = %d, job %+v; want a lease for a declaration of %d bytes", got.status, stored, test.size)
			}
			if !test.wantOK && (got.status != http.StatusNoContent || stored.Reason != execution.ReasonTooLarge) {
				t.Errorf("lease = %d, job %+v; want too-large for a declaration of %d bytes", got.status, stored, test.size)
			}
		})
	}
}

// The answer is bounded as a whole: two trees that each pass the entry limit still make an answer over 16 MiB.
func TestLeaseRefusesAnAnswerOverSixteenMiB(t *testing.T) {
	for _, test := range []struct {
		name    string
		entries int
		wantOK  bool
	}{{"under", 25000, true}, {"over", 40000, false}} {
		t.Run(test.name, func(t *testing.T) {
			e := newExecEnv(t)
			job := e.receiptJob(1, defaultFiles)
			e.trees.set(execSHA(1), execEntries(test.entries, 200))
			e.trees.set(execSHA(1001), execEntries(test.entries, 200))

			got := e.lease()

			stored := e.job(job.ID)
			if test.wantOK && (got.status != http.StatusOK || stored.State != execution.StateLeased || len(got.body) > 16<<20) {
				t.Errorf("lease = %d (%d bytes), job %+v; want a lease under 16 MiB", got.status, len(got.body), stored)
			}
			if !test.wantOK && (got.status != http.StatusNoContent || stored.Reason != execution.ReasonTooLarge) {
				t.Errorf("lease = %d, job %+v; want too-large for an answer over 16 MiB", got.status, stored)
			}
		})
	}
}

// What the store cannot read is the service's own fault: a 500 with a fixed code, nothing written, and the job still ready.
func TestLeaseAnswers500WhenTheServiceCannotReadWhatTheJobNames(t *testing.T) {
	t.Run("the manifest is not in the store", func(t *testing.T) {
		e := newExecEnv(t)
		job := e.enqueue(execution.Spec{RepositoryID: execRepoID, PullNumber: 3, Plan: execution.PlanCanonical, Profile: "go", HeadSHA: execSHA(1),
			GoverningDigest: execDigest("a manifest nobody stored"), GoverningSource: "V1", DeclarationDigest: execDigest("d"),
			CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff})
		e.wantError(e.lease(), http.StatusInternalServerError, "internal")
		if stored := e.job(job.ID); stored.State != execution.StatePending || stored.Attempts != 0 || stored.Reason != "" {
			t.Errorf("job = %+v; want it untouched", stored)
		}
		e.wantErrorLoggedForJob(job.ID)
	})
	t.Run("the repository is not enrolled", func(t *testing.T) {
		e := newExecEnv(t)
		m := e.manifest(defaultFiles)
		job := e.enqueue(execution.Spec{RepositoryID: 99, PullNumber: 3, Plan: execution.PlanCanonical, Profile: "go", HeadSHA: execSHA(1),
			GoverningDigest: m.Digest(), GoverningSource: "V1", DeclarationDigest: execDigest("d"), CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff})
		e.wantError(e.lease(), http.StatusInternalServerError, "internal")
		if stored := e.job(job.ID); stored.State != execution.StatePending || stored.Attempts != 0 {
			t.Errorf("job = %+v; want it untouched", stored)
		}
		e.wantErrorLoggedForJob(job.ID)
	})
	t.Run("the manifest is not a manifest", func(t *testing.T) {
		e := newExecEnv(t)
		junk := e.put("this is not the encoding of a manifest")
		job := e.enqueue(execution.Spec{RepositoryID: execRepoID, PullNumber: 3, Plan: execution.PlanCanonical, Profile: "go", HeadSHA: execSHA(1),
			GoverningDigest: junk, GoverningSource: "V1", DeclarationDigest: execDigest("d"), CanonicalLevel: domain.CanonicalLevelRequired, ReceiptLevel: domain.ReceiptLevelOff})
		e.wantError(e.lease(), http.StatusInternalServerError, "internal")
		if stored := e.job(job.ID); stored.State != execution.StatePending {
			t.Errorf("job = %+v; want it pending", stored)
		}
		e.wantErrorLoggedForJob(job.ID)
	})
}

// wantErrorLoggedForJob requires exactly one Error line that carries the job's id as its job attribute: the queue is stuck on that job, and the line is what
// says which. The same failure is not logged twice at Error.
func (e *execTestEnv) wantErrorLoggedForJob(id int64) {
	e.t.Helper()
	logged := 0
	for line := range strings.SplitSeq(e.logs.String(), "\n") {
		if strings.Contains(line, `"level":"ERROR"`) {
			logged++
			if !strings.Contains(line, fmt.Sprintf(`"job":%d`, id)) {
				e.t.Errorf("an Error line does not carry the job %d: %s", id, line)
			}
		}
	}
	if logged != 1 {
		e.t.Errorf("%d Error lines; want one for the job that cannot be prepared:\n%s", logged, e.logs.String())
	}
}

// The tree reads run inside the lease transaction: each has a deadline.
func TestLeaseBoundsTheTreeReads(t *testing.T) {
	e := newExecEnv(t)
	e.receiptJob(1, defaultFiles)
	e.lease()

	asked := e.trees.asked()
	if len(asked) != 2 {
		t.Fatalf("tree reads = %+v; want two", asked)
	}
	for _, call := range asked {
		if !call.deadline {
			t.Errorf("the read of %s has no deadline", call.sha)
		}
	}
}

// A tree entry's path is at most 4096 bytes on the wire: a tree with a longer one is a tree past its limit, final for the job.
func TestLeaseRefusesATreeEntryWithAPathOver4096Bytes(t *testing.T) {
	for _, test := range []struct {
		name   string
		size   int
		wantOK bool
	}{{"4096 bytes", 4096, true}, {"4097 bytes", 4097, false}} {
		t.Run(test.name, func(t *testing.T) {
			e := newExecEnv(t)
			job := e.canonicalJob(1, defaultFiles)
			e.trees.set(execSHA(1), []treeEntry{{Path: "a.go", Mode: "100644", SHA: strings.Repeat("a", 40)}, {Path: strings.Repeat("p", test.size), Mode: "100644", SHA: strings.Repeat("b", 40)}})

			got := e.lease()

			stored := e.job(job.ID)
			if test.wantOK && (got.status != http.StatusOK || stored.State != execution.StateLeased) {
				t.Errorf("lease = %d, job %+v; want a lease for a path of %d bytes", got.status, stored, test.size)
			}
			if !test.wantOK && (got.status != http.StatusNoContent || stored.Reason != execution.ReasonTooLarge) {
				t.Errorf("lease = %d, job %+v; want too-large for a path of %d bytes", got.status, stored, test.size)
			}
		})
	}
}
