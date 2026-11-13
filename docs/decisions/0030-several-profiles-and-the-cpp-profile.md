# ADR 0030: Several profiles in one repository, and the C/C++ profile

- **Date:** 2026-11-13
- **Status:** Accepted for phase M2.2 (SW-40), designed and not implemented. It applies the owner's decisions on the phase plan (decisions 1 to 6, 2026-11-11), decision 8 (2026-11-13, nested folders) and decisions 9 to 12 (2026-11-13, after the first review of this record: downloads, what a part sees, who owns a test file, redirect hosts). Decision 11 replaces the wording of decision 8. The rules that no decision settles are listed at the end as gap rules, and one of them needs the owner's confirmation. The phase page (task D3) will carry the decisions.
- **Product:** SuiteWard
- **Scope:** How one repository names several execution profiles, each in its own folder; the C/C++ profile (CMake, CTest and GoogleTest); a profile that is protected only; the end of a run as unavailable, in plain words, when the image of a declared profile is not installed; the sandbox images that an installation chooses; and what the lease, the result, the schema and the installer need for these. About forty code tasks consume this record and the M2.2 sections of the contracts.
- **Amends:** [ADR 0028](0028-canonical-execution-and-the-receipt.md) where it says that `go` is the only profile, that one image serves a run, and how dependencies are fetched; and [ADR 0029](0029-results-communication.md) only where the weakened-test signal gains removed tests and build failures (SW-62). The list is in [What this amends](#what-this-amends).
- **Related:** [ADR 0008](0008-progressive-integrity-and-execution.md), [ADR 0027](0027-agent-human-trust-separation.md), D-ROADMAP in the [decision register](../plan/decisions.md#d-roadmap-the-order-after-m21), the [runtime contract](../contracts/runtime.md#execution-with-several-profiles-m22-normative), the [persistence contract](../contracts/persistence.md#migration-00023-several-profiles-m22-normative) and the [threat model](../security/threat-model.md).

A † marks a choice of task D0 of M2.2 (a name, a size, a number, a word or a rule that the design leaves out), made inside this decision. The integrator may change it under the register's rule on implementation choices. Gap rule 5 lists the choices that go beyond the design.

## Context

M2.1 runs the tests of a Go repository. It leaves four things out.

- The declaration accepts one profile, `go`, for the whole repository. The owner's repository `magalz/Borealis` has a C++ core under `core/` (CMake, CTest, GoogleTest) and a Kotlin Android app under `apps/android` (phase M2.3). This repository is Go, and may get a C++ folder.
- The engine assumes one image, one toolchain and one folder for a run, from the lease to the results page.
- An image that is not installed ends a run as `sandbox-unavailable`, after three attempts, with a generic sentence. The runner cannot ask whether an image exists.
- The installer pulls the Go image for every installation, whatever the repositories need.

The owner decided, for M2.2 ([D-ROADMAP](../plan/decisions.md#d-roadmap-the-order-after-m21), and decisions 1 to 6 of the plan):

- Each profile has its own image, pinned by digest in the release. An installation chooses which images it holds. No image holds every language: an Android image alone is several GB.
- A profile can be declared protected only. Its tests stay under the contract, and nothing runs.
- When an approved declaration names a profile whose image is not installed, the runs read `unavailable` and say, in plain words, how to add the image or how to declare the profile protected only. The test runner never pulls an image.
- There is one `canonical` level and one `receipt` level for the whole repository.
- A C/C++ dependency is downloaded only from an `https` URL on `github.com` that carries a `URL_HASH SHA256`. Any other form reads as an unavailable run, in plain words. A wider governed list is M3.
- Decision 8: running folders may nest. The owner chose this over refusing nested folders, so that Go at the root with C/C++ in a subfolder works. Decision 11 below replaced its wording on who owns a file.

A spike on a copy of `core/` of Borealis, in the real image and under the sandbox's flags, corrected the first design in several places. They are marked "spike" below, and section 7 gives its numbers.

## Decided by the owner after the first review

The first review of this record found that three of its rules cost more than they said, and the owner answered on 2026-11-13. The sections below state these decisions as decided.

- **Decision 9, downloads.** A `cpp` dependency is downloaded while its part's input streams into the container, as Go's modules are today. A `fetch-failed` after an earlier part has run makes the next attempt run that part again. The runner buffers nothing. The first design's claim that a C++ fetch failure does not cost Go's run is withdrawn (sections 1 and 6).
- **Decision 10, what a part sees.** Each part's container gets the whole tree, as the Go run does today, with the part's folder as the working directory. Only the tests of the files that the part owns are expected. A build that reaches outside its folder works. The cost is that each part's container streams and unpacks the whole tree (sections 1 and 7).
- **Decision 11, who owns a test file** (it replaces the wording of decision 8). A test file belongs to the profile that recognizes it (Go: `_test.go`; `cpp`: `.c`, `.cc`, `.cpp`, `.cxx`), at that profile's deepest running folder above the file. A folder of another profile takes nothing away, so no recognized test is out of reach. One directory may therefore hold tests of two profiles. Test identity stays unique because a Go name never holds a `.` and a CTest name always does, and the service's per-package build state is keyed by `(profile, package)`. A protected-only entry takes its profile's test files under its folder away from the enclosing running entry of the same profile, so adding one checks less and is a reduction. That two profiles may name the same folder, for example Go and `cpp` both at the root, is the orchestrator's reading of this decision and **needs the owner's confirmation** (gap rule 1).
- **Decision 12, redirect hosts.** The declared URL stays `https://github.com/…` with a SHA-256 (decision 5). Its redirects may reach any of GitHub's website domains from the official meta API (`api.github.com/meta`, `domains.website`, read 2026-11-13): `*.github.com`, `*.githubusercontent.com`, `*.githubassets.com`, `*.github.io` and `*.github.dev`, as a fixed list in the code. The owner will revisit it when other platforms are supported (section 6).

## Decision

The sections follow the design of the phase. Section 7 is evidence and section 9 is the cost the owner accepted.

### 1. One job runs every running profile

The job stays the unit: one for each pull request, head and plan, for one binding ([ADR 0028, section 5](0028-canonical-execution-and-the-receipt.md#5-observations-states-and-their-precedence)). It runs every **running** profile of the canonical declaration, each in its image and its folder. One such run is a **part**. The results comment, both checks, the approve gate, `pr_receipt_checks`, `approval_receipts` and the weakened signal keep their shape. A test is still `(package, name)`, and `package` is the directory from the repository root. It stays unique even when two parts share a directory (decision 11).

- **The label of a job.** The job's `profile` is the sorted **unique** ids of its running profiles, joined by `-`: `go`, `cpp`, `cpp-go`. Two Go folders give `go`, never `go-go`. It is at most 32 bytes.
- **Who owns a test file (decision 11).** A test file belongs to the profile that recognizes it, at that profile's deepest entry above the file; the root contains every file. A protected-only entry is an entry too: when it is the deepest, the file belongs to no part and runs nowhere. An entry of another profile takes nothing away, so Go at the root and `cpp` in `core/` each keep the test files of their own kind in `core/`. One directory may hold tests of two profiles. That is safe because `(package, name)` stays unique (a Go name never holds a `.`, a CTest name always does) and because the NEW API rule, which reads a package as built or not (`unbuilt` in the service), is keyed by `(profile, package)`. A file that no entry of its profile contains belongs to no part.
- **How ownership cuts the work.**
  - *The canonical tree* is composed once for the whole job, as ADR 0028 section 2 says. Every part is given the whole tree, and its commands run in its folder (decision 10).
  - *The declared tests* of a part come only from the test files it owns, by the rules of its profile. Only those are expected.
  - *The receipt's evaluated files* (the files that the canonical `include` matches and the revision adds or changes) are split by the same rule. A part evaluates the test files it owns. A file that no part owns evaluates nothing. The base side of a part is the base commit's whole tree, with the head's version laid over every file that the canonical `include` matches and the head adds or changes.
- **Evidence.** A result holds one image digest and one toolchain today. It gains `parts`, one entry for each part. The old `sandbox` fields stay and describe the first part. The pull request page shows one toolchain row for each part. The runner asks a profile for its toolchain once for each process, as it asks for Go's today.
- **A missing image** ends the whole job (sections 8 and 9).
- **Retries are per job**: three attempts. Before the first test container the runner checks only what it settles without running a test: each image is held, each profile's toolchain is known (the probe is a container, but not a test container), and each part's dependency plan is read (the forms are checked, which is where `install-failed` for an unsupported form comes from). A download happens as the part's input streams into its container, so a `fetch-failed` can follow a part that has already run. The attempt then ends, and the next one runs every part again, the finished ones included. The runner keeps nothing between attempts (decision 9).
- **One deadline and one set of limits** serve the whole job: `EXECUTOR_JOB_TIMEOUT` and the limits of the lease. Each part's container holds the whole tree (decision 10), so the tree's share of the tmpfs is today's, spent once for each part. The spike measured `core/` of Borealis well inside the limits, and not a whole repository for each part (section 7).
- **Parts come from the canonical declaration of the job**, the one that its `declaration_digest` names. They do not come from the `declaration` bytes that the lease ships, which are the governing declaration. The two differ for an approved pull request that edits `.suiteward.yml`. So the lease carries the folders itself.

### 2. The declaration's `profiles`

```yaml
execution:
  canonical: required        # one for the repository, as today
  receipt: advisory
  profiles:                  # or the old scalar `profile: go`, never both
    - profile: cpp           # closed set: go, cpp
      folder: core           # optional; absent means the repository root
    - profile: go
      folder: tools/gen
      run: false             # protected only; the default is true
```

- `profiles` has 1 to 8 entries. A `(profile, folder)` pair appears once, and that is the only uniqueness rule: entries may nest, and two profiles may name one folder (gap rule 1). A folder is a clean relative path. `profile: go` stays valid and means one Go profile at the root: this repository keeps it.
- The ids are a closed set. The code accepts `cpp` only from the task that lands its profile (X18), so no engine reads a declaration that it cannot run.
- A protected-only entry (`run: false`) never runs. It takes its profile's test files under its folder away from the enclosing running entry of the same profile (section 1, decision 11), and those files stay under the contract through the `include` patterns, as in a repository with no `execution` block.
- A level (`canonical` or `receipt`) named with no running profile is an invalid declaration (this extends the rule of ADR 0028 section 1: levels need a running profile). A list whose entries are all protected only, with no level, is valid and runs nothing.
- `Execution()` keeps its shape. Its `Profile` is the label of section 1, and it reports "not configured" when nothing runs, so its three callers do not change.
- The reductions gain three entries: `execution profile: <id> <folder> removed`, `execution profile: <id> <folder> -> protected only`, and (decision 11) `execution profile: <id> <folder> protected only inside <enclosing>` † for a protected-only entry added inside a running one of its profile. They are listed in the revision comment as the level reductions are. The exact rules are in the [runtime contract](../contracts/runtime.md#the-declaration-m22).

### 3. The lease and the result

`POST /lease` still takes no body. The answer gains `profiles`, the running entries of the job's canonical declaration, each `{id, image, folder}`. `profile.id` is the constant `list` when the canonical or the governing declaration is in list form, and the label (`go`) when both are the scalar form. `profile.image` is the first part's image. The runner refuses with `profile-unsupported` when an id is unknown to it or an image is not its own pin.

The result gains `parts` (`[{profile, folder, imageDigest, toolchain}]`, in lease order) and `removed` (`[{package, name, file}]`, the tests a receipt's change removes: SW-62). A reason may be `image-missing-<id>`. A test name is a Go name or a GoogleTest name. The tables, the bounds and the rules are in the [runtime contract](../contracts/runtime.md#the-lease-the-result-and-the-versions-m22).

**Service and runner of different releases.** The [runtime contract](../contracts/runtime.md#the-lease-the-result-and-the-versions-m22) has the table and the rules. Two facts belong to this record. First, the table holds only when both releases pin the same Go image, and the pin has moved: `v0.5.0-alpha.1` to `v0.5.0-alpha.4` pin `golang:1.27.3-alpine@sha256:f2c872bc…` and the base of this phase pins `golang:1.27.4-alpine@sha256:0a5f7fe7…` (#74), so an alpha.4 runner and an M2.2 service refuse each other with `profile-unsupported` at once, and the installer's rule (upgrade both together) is a requirement and not advice. Second, `profile.id` is `list` when the canonical **or** the governing declaration is in list form: the governing declaration is the `declaration` that the lease ships and that an M2.1 runner parses strictly, so an approved pull request that changes `profile: go` to `profiles:` would otherwise reach an M2.1 runner as `go` and fail `executor-error` three times. `list` is never the job's `profile` column, and `profiles` comes from the declaration of `declaration_digest`, not from the bytes in `declaration` (section 1).

### 4. Migration 23

`00023_profiles.sql` (task Y1 owns it). The [persistence contract](../contracts/persistence.md#migration-00023-several-profiles-m22-normative) gives it constraint by constraint:

1. `execution_jobs_profile_check` stops being `profile = 'go'` and becomes a label: the ids joined by `-`, at most 32 bytes.
2. `execution_jobs_reason_check` accepts the family `image-missing-<id>` in the `unavailable` branch only.
3. `execution_results_reason_check` accepts the same family.
4. `execution_results.parts` (`jsonb`, bounded by `execution_parts_valid`, and empty unless the status is `complete`).
5. `execution_results.removed` (`jsonb`, bounded by `execution_removed_valid`, and empty unless the status is `complete`).
6. `SupportedVersion` becomes 23.

The insert and update guards, `execution_tests` and `execution_unparsed_valid` do not change.

### 5. The profile seam, the list of profiles and the pure packages

`internal/executor/run` cuts a profile at "run one side", not at "a command and its parse". Go runs a side as several invocations, in chunks and with reruns, in several containers. C++ must run the configure, the build and every test round in **one** container: a rerun in a new container is a full rebuild, and a test that reads a path fixed at configure time needs the same layout. The interface is unexported:

```go
type profile interface {
    image() string
    probe() (argv, env []string)                      // prints the toolchain
    reads(path string) bool                           // go.mod, go.sum | CMakeLists.txt, *.cmake
    testFile(path string) bool                        // recognizes a test file: _test.go | .c .cc .cpp .cxx
    declared(files tree) declared                     // tests, excluded, unparsed
    changed(head, canonical tree) (added, changed, removed []testID)
    markers(path string, data []byte) []markerRow     // nil for cpp
    plan(s sideFiles, l client.Limits, toolchain string) (sidePlan, error)
    run(ctx context.Context, box invoker, p sidePlan, tests []testID) (map[testID][]execution.Observation, error)
}
```

- Paths are relative to the part's folder, although the whole tree is present (decision 10). `run` adds the folder back, so a result's `package` is from the repository root.
- `invoker` is today's `invoke` with argv, environment and extra tar entries chosen by the profile. The rules for an out-of-memory kill, exit status 137 and truncated output stay in that one place.
- `goprofile` and `cppprofile` stay pure packages: they read no file and run nothing. Each adapter (`profile_go.go`, `profile_cpp.go`) converts its package's `TestID`.

**The list of profiles** is one constant of the release, in `internal/executor/profiles`. The service names each image from it in a lease, the runner holds its pins from it, and the installer reads it for the page, `profile add`, `doctor` and the upgrade. The declaration keeps its own closed set, because the domain imports no executor package, and a test asserts that the two are equal. Names and types are †:

```go
type Profile struct {
    ID         string   // ^[a-z][a-z0-9]{0,15}$, from the closed set: "go", "cpp"
    Name       string   // the words of the installer page
    Image      string   // pinned by digest
    ImageBytes int64    // the size the page shows: fixed in the code, never asked of a registry
    BuildFiles []string // base names that suggest the profile: go.mod; CMakeLists.txt
}
func All() []Profile                  // in the list's order
func ByID(id string) (Profile, error) // an unknown id is an error that names the known ones
func Suggest(paths []string) []string // ids, in the list's order, each once; no file is read
```

`Suggest` matches on the last segment of a path. A `CMakeLists.txt` anywhere suggests `cpp`, with or without tests: nothing reads file contents. The page preselects the suggestion and the owner decides.

**`cppprofile`** mirrors `goprofile`. The exact names and types are † and belong to tasks X10 to X16. What each function takes and gives is the contract:

```go
const Image = "ghcr.io/ignisdevne/suiteward-sandbox-cpp@sha256:…" // X10; the digest is section 6
type TestID struct{ Package, Name, File string }                  // Package: relative to the part's folder

func DeclaredTests(files map[string][]byte) []TestID                                                    // X11: never fails, excludes nothing
func ChangedTests(head, canonical map[string][]byte) (added, changed, removed []TestID)                 // X12
func Dependencies(files map[string][]byte) ([]Dependency, error)                                        // X13: an error is "unsupported"
func Command(deps []Dependency, cpus, reruns int, timeout time.Duration) (argv, env []string)           // X15: one container, one side
func Parse(stdout []byte, exitCode int, expected []TestID) (map[TestID][]execution.Observation, error) // X16
```

`Dependency` is `{Name, URL, SHA256}`. There are no markers: the receipt's "lines to look at" for C/C++ come from the change report (task X21).

### 6. The C/C++ profile (`cpp`)

- **Image.** `ghcr.io/ignisdevne/suiteward-sandbox-cpp@sha256:676ff87dac3affb1645736325374b9b3f039d28315cf3b980e49f5fc373c1b8e` at the time of this record: Debian trixie, g++ 14.2.0, CMake 3.31.6, Ninja 1.12.1. It has no `make`, so the generator is always Ninja. The constant in `cppprofile` is the authority, and the weekly image scan can change it. The toolchain text is the compiler's version and CMake's, at most 64 printable bytes.
- **Test files and declared tests.** The test files are the C/C++ sources that the part owns (section 1) and that the repository protects (in the manifest for a canonical run, evaluated for a receipt). No name pattern: the scope's `include` decides. Headers are not scanned. A tolerant lexer drops comments, string and raw-string literals and `#if 0` blocks, and finds `TEST`, `TEST_F`, `TEST_P`, `TYPED_TEST` and `TYPED_TEST_P`. It never fails to parse and excludes nothing. The tests come from the protected sources and not from CTest, so a test that is dropped from `CMakeLists.txt` reads MISSING.
- **Names (spike).** CTest's JUnit puts the same string in `name` and `classname`: the name that `gtest_discover_tests` gives the case. It is `Suite.Name` for `TEST` and `TEST_F`; `Prefix/Param.IsPositive/<printed value>` for an instance of a `TEST_P`, with a `  # GetParam() = 7` suffix when the instance has a custom name; `Typed.HasSize<int>` for a typed test; and `DISABLED_` is stripped. The reported test is the declared one: its `package` is the directory of the declaring source and its `name` is `Suite.Name`. A case maps to its declared test by stripping the instance prefix, the `/<value>` or `<type>` suffix and any `  # ...` suffix. Several cases of one declared test take the worst result, as the Go profile does (fail over skip over pass). A JUnit case with no declared test is ignored, and its failure still counts when the exit status is compared with the report. A declared test with no case reads `absent` (MISSING). `TEST_PREFIX` is not supported and reads MISSING.
- **Build failure (spike).** It does not show in the JUnit. A target that did not build appears as `<target>_NOT_BUILT`, with `status="notrun"`, a `<skipped message="Unable to find executable"/>`, `failures="0"`, and `ctest` exits 8. The signal is the exit status of `cmake --build`, or of the configure. Either gives `build-error` to every declared test of the side, whatever executables happened to link. The build uses `-- -k 0`, so one broken target does not hide the others.
- **Reruns (spike).** Everything runs in one container. A first `ctest --output-junit … --schedule-random --timeout <testTimeoutSeconds>` † (the counterpart of Go's `-timeout`) is followed by rounds of `--rerun-failed`, but only after a round **with a failure**: with none, `--rerun-failed` reruns every test. There are at most `1 + reruns` rounds. Each round prints its status and its file, and the parser gives one observation per round that ran the test. A build error is never rerun.
- **Configure and build (spike).** `cmake -S . -B /work/.suiteward/build -G Ninja -DCMAKE_BUILD_TYPE=Debug -DBUILD_TESTING=ON -DFETCHCONTENT_FULLY_DISCONNECTED=ON` and, for each dependency, `-DFETCHCONTENT_SOURCE_DIR_<NAME>=/work/.suiteward/deps/<name>`. CMake presets are ignored: they are the candidate's. `BUILD_TESTING` is a no-op for Borealis, which prints a warning, and harmless; the flag is a choice of this record †. The build is `cmake --build … -j <cpus> -- -k 0`, because Ninja's default started ten jobs on a quota of four CPUs (peak 1.43 GB, against 0.83 GB with `-j 4`). `TMPDIR` is `/work/.suiteward/tmp`: GCC falls back to the working directory without it, but a build step that uses `mktemp` would fail on a read-only `/tmp`.
- **Dependencies (decisions 5, 9 and 12, with the spike's redirect).** The runner reads them from the CMake files of each side: `FetchContent_Declare(<name> URL <url> URL_HASH SHA256=<hex> …)`. The declared URL must be `https://github.com/…` and carry a SHA-256. Its redirects may reach GitHub's website domains (decision 12): `*.github.com`, `*.githubusercontent.com`, `*.githubassets.com`, `*.github.io` and `*.github.dev`, as a fixed list in the code. The spike saw `release-assets.githubusercontent.com` for a release asset and `codeload.github.com` for an archive. The contract has the matching rules, including that `github.com` itself is on the list † and that a refused redirect, like a hash mismatch, reads `install-failed` †. Anything else (a `GIT_REPOSITORY`, a variable in the URL, another host, no hash) reads as an unavailable run, in plain words. The runner downloads outside the sandbox, as the part's input streams into its container (decision 9), with no credential and no proxy setting, and checks the SHA-256 while it streams: a mismatch writes no completion marker, so nothing runs. The archive is unpacked **inside** the sandbox (with `--strip-components=1` †), so the runner never parses an untrusted archive. A configure that is offline and lacks the source override fails with no wording about a download (CMP0170, then "target `GTest::gtest_main` not found"), so the plan must know every dependency before the run.
- **The receipt's base side.** The head's evaluated test files are laid over the base commit. If the base builds them, they run. If the base's build fails, every test reads `build-error` (NEW API). If the base's CMake does not list a new test file, the test reads `absent` (MISSING). The guide will tell owners to protect the `CMakeLists.txt` that lists the tests: Borealis lists its test sources explicitly in `core/tests/CMakeLists.txt`.
- **Limits of the profile.** A `TEST_PREFIX`, tests under an `#if` that is not compiled, a plain `add_test` of a whole binary, `TYPED_TEST_P` and GMock were not exercised by the spike. A test that the lexer cannot see (a project macro that expands to a `TEST`, a generated test) is not expected and not covered. A download that the plan does not read (`file(DOWNLOAD)`, `ExternalProject_Add`) fails offline and reads `build-error`. The contract lists the rest.

### 7. What the spike measured

On `core/` of Borealis (`main` at `1820fef4`), with the exact sandbox flags: 77 of 77 tests pass offline. The container ran 5.1 to 5.8 s (configure 0.19 s, build 3.6 s, ctest 0.15 s). `/work` took 29 MB, which is 2.8% of the 1024 MiB tmpfs. The memory peak was 1.31 to 1.43 GB of 3072 MiB, with no out-of-memory kill. No Borealis option was needed, and `/tmp` not being writable did not break the compile. This is one target with a 4 s build: it says nothing about a build of minutes or one near 3 GiB, and it did not exercise `TYPED_TEST_P`, `TEST_PREFIX`, `PRE_TEST` discovery or GMock. With decision 10 each part's container gets the whole tree, and the spike measured only `core/` (1.8 MB): the cost of the whole repository of Borealis for each part is not measured. The limits of a profile are therefore the lease's limits, with no per-profile change.

### 8. The installer

- `Settings.Profiles`, the key `profiles` of the settings file, has no `omitempty`. A file with no such key (an install of an earlier release) reads as `[go]`. `profiles: []` is an explicit "none".
- The sandbox images are pulled after the repositories are chosen, only for the chosen profiles. A pull that fails warns, and names the fix: `suiteward profile add <name>`. The test runner never pulls.
- The profiles page lists every profile with the size of its image, and preselects the profiles that the **file paths** of the chosen repositories suggest (`go.mod`, `CMakeLists.txt`; `settings.gradle` joins in M2.3). Nothing reads file contents. With GitHub off, nothing is suggested.
- `suiteward profile add <name>` is a co-located command. On the isolated tier the root host step takes the profiles (gap rule 3).
- `doctor`'s `executor.image` row covers each chosen profile, and an upgrade pulls the new images of the chosen profiles.

The [runtime contract](../contracts/runtime.md#the-installer-and-the-profiles-m22) states the key and the command; the install guide (task D2) explains them to users.

### 9. The missing image, and what it costs

When the container engine does not hold the image of a profile of the lease, the job ends **as a whole** as `unavailable` with the reason `image-missing-<id>`. It is final, so it is not retried, and the runner sets it before it starts any container: it asks the engine and never pulls. The rules (the first missing profile in lease order, an engine that cannot answer, the sentence and the next step) are in the [runtime contract](../contracts/runtime.md#a-missing-image).

Why the whole job, and not "those tests excluded": an excluded test passes the gate. A co-located agent could delete an image to skip tests. The owner accepted this cost with the plan.

**What the owner needs to know.** A missing image ends the whole run of a commit as unavailable, the results of the other profiles included, and it does so even when the missing profile has nothing to run in that revision. Adding the image does not run the commit again by itself (gap rule 4).

## Alternatives rejected

- **One job for each profile.** It would rewrite the most delicate code of M2.1b (the plan, the gates, `pr_receipt_checks`, the results comment) with no decision that asks for it.
- **Mark only the tests of the missing image as not run.** Excluded tests pass the gate (section 9).
- **One image with every language.** An Android image alone is several GB.
- **Refuse folders inside folders.** Decision 8 chose against it: Go at the root with C/C++ in a subfolder would not work.
- **A folder owns every file under it, whatever the profile.** The first design (decision 8's wording): the root's Go part would declare no test under `core/` when `core/` runs `cpp`, and one package would never hold two profiles. Decision 11 replaced it with an owner for each file within each profile, so that no recognized test is out of reach.
- **Give a part only the files under its folder.** The first design. It breaks a build that reaches outside its folder (`../cmake/`, an `add_subdirectory` of a sibling) and may break M2.3, where Borealis's Android module adds `<repo>/core`. Decision 10 chose the whole tree.
- **Buffer the downloads in the runner before the first container**, so that a fetch failure repeats no finished run. Decision 9 chose against it: Go's modules already stream into the container, and the runner keeps nothing between attempts.
- **A runner that pulls a missing image.** It would give the candidate's declaration a way to make the runner download. The owner decided that it never pulls.
- **Downloads from `GIT_REPOSITORY`, or from any host.** They need a network and a client that parses what the candidate names. The owner limited it to `github.com` with a hash (decision 5).
- **Read the dependencies by running an online configure.** The sandbox has no network, and a configure with no override fails slowly (85 s measured) and with no wording about a download.
- **Take the tests from CTest.** A test dropped from `CMakeLists.txt` would then disappear instead of reading MISSING.

## Consequences

- A repository can run Go and C++ in one job, and the cost of one missing image is the whole run of a commit.
- A `fetch-failed` of a C++ dependency after an earlier part has run repeats that part in the next attempt (decision 9). Each part's container streams and unpacks the whole tree (decision 10).
- Where an installation chooses `cpp`, the executor reaches `github.com` and the GitHub website domains of decision 12 over HTTPS, besides the two names of Go. An egress allow-list by name for them is recommended. Two of the five domains (`*.githubusercontent.com`, `*.github.io`) serve what others publish, so the SHA-256 authenticates the bytes and the host does not. The threat model gains a line for downloads that the candidate names (task D1).
- The bytes of a C/C++ dependency are as safe as TLS to GitHub and the candidate's own `URL_HASH`, which the candidate controls. That is equal to a networked configure and not stronger, as for Go's `go.sum` ([ADR 0028, section 11](0028-canonical-execution-and-the-receipt.md#11-the-sandbox-and-the-go-profile)).
- The candidate's code runs in the sandbox during the configure, the build and `gtest_discover_tests` (which runs each test binary at build time), as Go's compile and tests do. It can write the JUnit files, which is the class of forged events that Go has. M2.2 adds no signal for it. The exit-status rule is the only cross-check (the [contract](../contracts/runtime.md#the-cpp-profile)).
- A `CMakeLists.txt` anywhere suggests `cpp` in the installer, with or without tests. A part's tests that its CMake does not list read MISSING, so owners should protect the `CMakeLists.txt` that lists their tests.
- A revision that only deletes test files has no receipt, so the weakened signal cannot name those removals (gap rule 2).
- The pull request page, the evidence binding and the check summaries state parts, so their texts change (tasks Y6 to Y8 and Y19 to Y20).

## What this amends

**[ADR 0028](0028-canonical-execution-and-the-receipt.md).** ADR 0028 needs a dated note that points here. No task of the phase owns that file yet (task D1 owns ADR 0029), so until one does, this list is the note.

- **Section 1.** `profile: go` is not the only value: the list form accepts `go` and `cpp`, each with an optional folder and an optional `run`. Strict keys now include `profiles`, `folder` and `run`. "Levels need a profile" reads "need a running profile". "The toolchain is not configurable" holds for each profile: its image is the release's pin.
- **Section 2.** The expected tests are those of each part, by its profile and the test files it owns, and not only the top-level Go test functions of the manifest. The tree is still composed once, and every part's container is given the whole of it (decision 10), so the tree's share of the tmpfs (half, section 11) is spent once for each part.
- **Section 4.** The Go rules are the rules of the Go profile. The `cpp` profile declares tests by the lexer of section 6 and excludes none.
- **Section 5.** The rule that reads a package as built or not on the base side (`MISSING` when "the base package built", and `NEW API`) is read for each `(profile, package)`, because one directory may hold tests of two profiles (decision 11). `build-error` for a `cpp` part means a failed configure or build, for every test of the side. The reasons that are final for the repository gain `image-missing-<id>`.
- **Section 10.** The executor's egress gains `github.com` and the GitHub website domains of decision 12, for an installation that chose `cpp`.
- **Section 11.** "One image pinned by digest for each profile" now has a consequence: a run may use several. Dependencies are per profile: Go as written, C/C++ by section 6. The sentence that Python and TypeScript dependencies each decide their safety in M2.2 and M2.3 is replaced by the roadmap note of 2026-11-03: C/C++ is M2.2, Kotlin and Android M2.3, and the others wait in the backlog.
- **Section 14.** The profile is the label of section 1, and the image digest and toolchain are those of each part.

**[ADR 0029](0029-results-communication.md).** Only section 4, the weakened signal (SW-62). The set gains the tests that the receipt's `removed` lists and whose approved version fails, is missing or is flaky. A test that did not build is marked apart, and the page does not ask the owner to read an edit for that reason alone. The counts in the comment and the checks stay equal to the page's. Section 1 is not amended: the one new sentence of a comment names a profile that comes from a closed set, never text of the repository or of the runner. The details are in the [runtime contract](../contracts/runtime.md#removed-tests-and-the-weakened-set-m22); task D1 adds the note to ADR 0029.

## Gap rules

Decisions 9 to 12 settled three points of the first version of this record: what a part sees, a protected-only entry and nested entries, and the redirect hosts. The rules below are not decided by the owner. Each is the narrowest one that the code can implement, and the contracts mark with † the ones that go beyond the design.

1. **Two profiles at one folder (needs the owner's confirmation).** The orchestrator reads decision 11 as allowing `go` and `cpp` both at the root: the only uniqueness rule is that a `(profile, folder)` pair appears once, and the label of such a job is `cpp-go`. The stricter reading keeps running folders pairwise different. It refuses that declaration and changes nothing else, because Go at the root with `cpp` in `core/` shares directories either way.
2. **A revision that only deletes test files has no receipt.** ADR 0028 section 2 makes it `not-applicable`, so no run reports `removed` and the weakened signal cannot name those deletions. Changing the applicability rule is beyond this record. The cost is the case that SW-62 describes, when it is the only change.
3. **The fix of a missing image on the isolated tier.** There `suiteward profile add` does not exist: the root host step takes the profiles. The sentence of the reason must name a fix the reader can run. The contract writes the co-located command, and asks task Y6 for the isolated wording once task Y18 settles the host step.
4. **Adding the image does not run the commit again.** `image-missing-<id>` is final for its job, as `toolchain-mismatch` is. After `profile add`, a new push makes the next run. Where a gate holds, the owner can unblock the commit instead, which passes without running its tests. Replacing the job when an image appears would need the service to learn about the runner's image store, which it does not know.
5. **Choices beyond the design, marked † in the contracts.**
   - `ctest --timeout <testTimeoutSeconds>`: CTest's own default is 1500 s, so a hung test would hold the job to its deadline and read as `timeout`, not as `fail`. `-DBUILD_TESTING=ON`.
   - A hash mismatch, a refused redirect and a bound that is passed read `install-failed`. A download that fails or is cut off reads `fetch-failed`.
   - `github.com` itself is on the redirect list, so that a renamed repository's archive URL is followed; a redirect has at most 3 hops.
   - An archive is unpacked with `--strip-components=1`, so it must hold one top-level folder.
   - The sources that `cpp` recognizes are `.c`, `.cc`, `.cpp` and `.cxx`. A GoogleTest name is `Suite.Name`. A dependency has a name of `^[A-Za-z][A-Za-z0-9_]{0,63}$`, a `.tar.gz` or `.tgz` URL of at most 2048 bytes, and a part has at most 16 of them.
   - The dependency plan reads only the CMake files under the part's folder, although the part gets the whole tree: a `FetchContent_Declare` outside the folder is not planned, and every test of the side reads `build-error`.
   - The third reduction string of section 2. Inside a protected-only entry, a running entry of the same profile keeps its own subtree.
   - Smaller rules: the root is spelled `.` in the lease, the parts and the reductions, and `folder: .` is refused; the scalar `profile` accepts `go` only; `sandbox` describes the first part; `removed` leaves out excluded tests; `Suggest` matches on the last segment of a path.

## Not decided

- The Kotlin and Android profile, and the dependency resolution of Gradle (M2.3).
- A wider governed list of download hosts, and dependencies that are not a `FetchContent_Declare` with a URL (`GIT_REPOSITORY`, `ExternalProject_Add`, vcpkg, Conan): M3 and later.
- Python and TypeScript stay in the backlog.
- Limits for each profile: the spike needed none.
- Sealed tests, mutation testing and remote attestation stay deferred, as in ADR 0028.
