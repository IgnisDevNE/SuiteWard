# ADR 0028: Canonical execution and the receipt

- **Date:** 2026-10-26
- **Status:** Accepted for phase M2.1 (the Go profile), merged ([#60](https://github.com/IgnisDevNE/SuiteWard/pull/60)) and released as the pre-release `v0.5.0-alpha.1`. Live, the executor on the co-located tier ran on Windows with Docker and a development build, and on a throwaway AlmaLinux 9 distro the sandbox tests ran under rootless Podman and the host step ran. After the merge the isolated flow ran too, on the published alpha (owner decision 15 of the [phase page](../plan/phases/M2.1.md)). Python (M2.2) and TypeScript (M2.3) follow.
- **Product:** SuiteWard
- **Scope:** Running the protected tests in disposable sandboxes: canonical execution on every head, the receipt of the tests a pull request adds or changes, the policy that turns them on, the owner's unblock, the executor, and the evidence they leave.
- **Amends:** [ADR 0008](0008-progressive-integrity-and-execution.md) (the definition of M2). It also changes three statements of [ADR 0015](0015-required-contract-check.md), which carries its own note.
- **M2.1b note (2026-10-28):** the unblock of section 7 and the recovery of section 5 change for a runner that does not respond. See the [note](#note-2026-10-28-the-unblock-lifts-a-pending-run-since-m21b) at the end ([ADR 0029](0029-results-communication.md)).
- **Related:** [ADR 0002](0002-exact-revision-approval.md), [ADR 0007](0007-canonical-contract-authority.md), [ADR 0013](0013-protected-scope-and-agent-assisted-review.md), [ADR 0027](0027-agent-human-trust-separation.md), D-APPROVAL-CONTEXT, D-COMMANDS and D-CHECKS in the [decision register](../plan/decisions.md), the [M2.1 phase page](../plan/phases/M2.1.md), the [runtime contract](../contracts/runtime.md#execution-m21-normative), the [persistence contract](../contracts/persistence.md#execution-m21-normative) and the [threat model](../security/threat-model.md).

Names, sizes and durations that the contracts mark with † are choices of task A0, made inside this decision. The integrator may change them under the register's rule on implementation choices. The deferred work at the end is not such a choice.

## Context

M1 protects the bytes of the tests. It does not run them: [ADR 0014](0014-merge-triggered-canonical-promotion.md) and ADR 0015 leave test execution to the project's own CI. Two things are missing.

- Nothing in SuiteWard runs the approved tests on the code a pull request changes. The owner cannot tell, from SuiteWard, whether the approved contract still passes on this head.
- The owner approves a changed or added test without seeing whether it detects anything. A test that passes on the code before and after the change protects nothing.

ADR 0008 planned M2 as canonical execution with a `DockerExecutionBackend`. This record settles the first profile (Go), the trust model, the policy, the escape from a blocked run, and what the owner sees. The owner's decisions of 2026-10-26 are on the [phase page](../plan/phases/M2.1.md#owner-decisions-2026-10-26).

## Decision

### 1. Execution is opt-in, through the governed policy

Nothing runs until the canonical declaration names a profile. `.suiteward.yml` gains an `execution` block:

```yaml
execution:
  profile: go            # the only value in M2.1
  canonical: advisory    # off | advisory | required
  receipt: advisory      # off | advisory | before-approval | required
```

- **Defaults.** With a `profile`, `canonical` and `receipt` default to `advisory`. With no `execution` block, or an empty one (`execution: {}`), nothing runs: both levels are `off`. A key with no value (`execution:` alone, or `execution: ~`) is not an empty block: it is null, and the declaration is invalid (amended 2026-10-27: the sentence said "an empty one" and the code refuses the null; the contract states what is accepted).
- **Strict keys.** An unknown key under `execution`, or a `profile` other than `go`, makes the declaration invalid, as an unknown top-level key does.
- **Levels need a profile.** An `execution` block that names `canonical` or `receipt` without a `profile` is an invalid declaration. A policy that looks on while nothing runs would be believed. To turn execution off, remove the block, or keep the profile and set both levels to `off`.
- **Governed.** The policy is part of the declaration, so changing it is a protected change that needs the owner's approval with a TOTP code. Turning execution on is such a change.
- **The canonical declaration governs every pull request, the one that changes it included.** The levels come from the declaration of the Suite's current version. A pull request that weakens them still meets the old levels. Before `V1` nothing runs.
- **Weakening, in order.** `canonical`: `off` < `advisory` < `required`. `receipt`: `off` < `advisory` < `before-approval` < `required`. Removing the block weakens both levels to `off`. A weakening is reported as a reduction in the revision comment, like a removed pattern. A strengthening is not.
- **The toolchain is not configurable.** The Go version is the release's pinned Go image.
- A later task may suggest `execution: {profile: go}` when a `go.mod` exists. A suggestion grants nothing.

### 2. One engine, two plans

Each plan runs on a head and is evidence about that head. Neither enters the consent binding ([D-APPROVAL-CONTEXT](../plan/decisions.md#d-approval-context-approval-binding-for-implementation-only-changes) holds). Fork pull requests get no run: they fail closed, as in [D-INTEGRATION](../plan/decisions.md#d-integration-merge-methods-and-the-integrated-source).

**The canonical plan** runs for the head of every open pull request, when `canonical` is not `off`. It runs the head tree with the **governing manifest** laid over it. The governing manifest is the manifest of the current canonical version `V<n>`. Once a consent covers this head, it is the manifest of the consented revision. The whole manifest is laid over:

- every manifest file is written at its path, replacing the head's file or adding one: tests, `testdata`, runner files such as `conftest.py`, `.suiteward.yml` and workflows;
- every head file that the governing scope covers but the manifest lacks is removed. The scope covers the `include` patterns, the protected defaults and the `runner` patterns, less the `exclude` patterns (`Scope.Covers`);
- head files outside the governing scope stay.

The head can therefore add a test but not silence one, and it cannot change the runner of the approved tests. The expected tests are the top-level test functions declared in the manifest's Go test files.

**The receipt** is the report of the tests that a revision adds or changes. It exists for a head that has a change proposal revision, when `receipt` is not `off`, and when it is **applicable**.

- **Applicable** means that at least one file matched by the **canonical** `include` (and not excluded) is added or changed in the revision against the canonical version it expects. The service decides it at file level, from the two manifests. A revision that changes no such file (only runner files or the declaration changed, or tests were only deleted) has a receipt that is `not-applicable`: no job runs, the receipt gate does not apply (the approve needs no mode), and no Receipt check is published. Deletions are already shown as reductions.
- **Empty.** An applicable receipt whose files hold no test function that was added or changed (only `testdata`, helpers, or test files whose test functions did not change), or whose evaluated tests are all `EXCLUDED`, is complete and empty. The receipt gate treats it like a `not-applicable` one, and the Receipt check is `success` with the counts of the seven states at zero (excluded tests may be listed; amended 2026-10-27: it said six, and the check lists seven states besides the excluded tests). The page lists the changed files.
- **Evaluated tests** are the top-level test functions added or changed, by declaration text, in those files. "Changed" is against the canonical version the revision expects, not against the merge base and not against the consented manifest, so the receipt does not turn empty after an approval. Per-function selection belongs to the profile.
- **Head side:** the head tree.
- **Base side:** the base commit's tree plus the head's version of **every** file that the canonical `include` matches and the head adds or changes (compared with the canonical version the revision expects): test files, `testdata/` and helpers in other `_test.go` files. Nothing else of the head crosses: source, `go.mod`, `go.sum`, `vendor/`, runner files and `.suiteward.yml` all come from the base commit.
- **The base commit** is the merge base of the head and the pull request's base branch. When GitHub says there is none (no common ancestor, or a commit that a force push removed), the receipt is `unavailable` with the reason `no-merge-base`, final for that head.
- **Not evaluated.** Files that the pull request adds or changes and the head's own `include` matches but the canonical `include` does not are listed as "not evaluated: pattern not approved yet". The service computes the list and stores it with the job. It is display only.

Why this base side:

- Only the approved `include` decides what crosses from head to base. A head cannot add a pattern to its own declaration to pull its source into the base run, which would make every test pass on both sides.
- Every approved-pattern file crosses, so a new test does not fail on the base because its fixture or its helper is missing.
- The base run is the code before the change, with its dependencies. A fix that is a dependency bump is still detected: the old dependency fails the test and the new one passes it.

What it costs:

- A test that needs a new dependency, or a symbol that exists only in the head's source (a file the approved `include` does not match), does not build on the base. It reads NEW API. In Go a build failure belongs to a package, so every evaluated test of that package reads NEW API. This is a documented limit.
- DETECTS means "failed on the base side". It does not prove that the test fails for the reason this change fixes.

### 3. The source is the commit tree, not the archive

GitHub builds archives with `git archive` semantics. A candidate's `.gitattributes` can drop files (`export-ignore`) or rewrite them (`export-subst`), and LFS files arrive as pointers. So the service reads the tree entries of each commit it uses (path, mode, git blob SHA-1; one recursive tree read, refused when GitHub truncates it) and gives them to the executor with the job. The executor recomputes the blob SHA-1 of every extracted file. A missing, extra or mismatched file makes the run `unavailable`. Submodules are skipped and listed. A symlink is created only when it resolves inside the tree; any other symlink makes the run `unavailable`. A file mode of `100664`, git's legacy mode, is a regular file.

**Where the tree is written, and names that alias.** The executor builds the stdin tar of the sandbox from the verified tree: it reads the archive stream from the service, computes each entry's git blob SHA-1 while it copies the entry to the container's stdin (the tar header gives the size, so the `blob <size>\0` prefix is known first), and on a missing, extra or mismatched entry (the `CheckSource` rules) it stops the container before `go test` starts. Nothing runs on unverified bytes, and no whole entry is held in memory. Nothing of the tree is written to a host filesystem, where Windows or macOS would alias names. The tree is extracted inside the sandbox's Linux filesystem, a tmpfs, which is case-sensitive and has no 8.3 short names, with OS-level containment (`os.Root`). The domain also refuses lexical aliases, as defence in depth and not as the only guard: names that fold together by case or by a trailing dot or space, and names holding `~` followed by a digit (the shape of an NTFS 8.3 short name), in a tree, an extraction or a link target. A repository with such a name reads `unavailable` (`source-mismatch`), final. Unicode normalization (NFC and NFD forms of one name) is not modelled: it cannot alias inside the Linux sandbox.

### 4. Which tests a profile can run

The Go profile builds its expected list from files that match the sandbox build context (`go/build.Context.MatchFile` with the sandbox's `GOOS` and `GOARCH` and no extra tags). A file with `//go:build integration`, or named `_windows_test.go`, is not expected. Only `func TestXxx(t *testing.T)` at top level counts. `TestMain`, benchmarks, fuzz targets and examples are not tests here.

A declared test that the profile cannot run is `EXCLUDED`. It is decided statically, listed with its reason, never counted as a pass and never blocking. **Canonical assurance does not cover excluded tests.** The page says how many there are. An evaluated set that is all `EXCLUDED` is an empty receipt. Integration tests that need a service such as PostgreSQL are outside M2.1.

**Nested modules (amended 2026-10-27, owner decisions 16 and 17 of the [phase page](../plan/phases/M2.1.md), from the whole-phase review).** A directory with its own `go.mod` is another module, which the root invocation cannot name, so its tests are `EXCLUDED` (`nested-module`). The first design let any `go.mod` of the composed tree do that, which broke the rule that a head can add a test and not silence one: a pull request could add a `go.mod` over a protected test directory and turn the approved tests there into excluded ones, which neither block nor count. Only a `go.mod` that the repository protects makes a nested module now:
- **In the canonical plan,** only a `go.mod` that the governing manifest lists (decision 16). A `go.mod` that the head adds excludes nothing, in one of two ways. The canonical tree is the manifest laid over the head, and a head file that the manifest lacks is removed when the governing scope covers its path (section 2): a head `go.mod` at a covered path is gone, and the tests of the manifest run as usual. One at a path that the scope does not cover stays, `go` cannot resolve the package of the manifest's tests under it, and the setup failure reads `build-error`, which is `FAIL`. A repository with a real nested module protects its `go.mod`, with an `include` or a `runner` pattern.
- **In the receipt,** only a `go.mod` of the head that is present in the merge base, or that the canonical version the revision expects lists, or whose path the approved patterns of that canonical cover, so that the revision's manifest lists it (decision 17; a pattern that only the head adds counts for nothing). Under any other, a test runs, and a setup failure on the head reads `build-error`, which is `FAILS`. A nested module that the base has and the head removed is no module of the head: its tests run on the head, and the base side, where it still is a module, reads `build-error`, so a head pass is `NEW API`.

A package that `go` cannot resolve is named in its report by the pattern of the invocation (`./dir`), not by an import path, and `go` runs the rest of the invocation; the executor reads that name as a package that did not build.

### 5. Observations, states and their precedence

Each side of each test yields one observation: `pass`, `fail` (a failed assertion, a panic or a timeout), `build-error` (the package or file did not build, so the test never ran), `skip`, `absent` (expected, no result) or `excluded`. A changed test file of the receipt that `go` cannot parse lists no test; the executor reports its path, and each such file counts as a blocking failure of the receipt (like `FAILS`), so a broken test file never makes an empty receipt. A package that does not build reports every expected test of it as `build-error`, and so does an invocation that exits non-zero with no event at all (the `go` command failed before any package ran, for example an inconsistent `vendor/`), because nothing ran. A side whose `go.mod` needs a newer Go than the pinned image is found before the run and reads `unavailable` (`toolchain-mismatch`); the build-error reading is only the fallback. A failure, and a test left without a result (after a panic or an exit in another test), is re-run on the same side (2 reruns†). A side whose runs differ makes the test flaky. The service derives the states below from the observations the executor reports.

**Receipt state of an evaluated test, first match wins:**

| # | State | When |
| --- | --- | --- |
| 0 | `EXCLUDED` | declared but not runnable in this profile (static, before any run) |
| 1 | `MISSING` | head side absent, or base side absent while the base package built |
| 2 | `FLAKY` | either side varied across runs |
| 3 | `SKIPPED` | head side skip |
| 4 | `FAILS` | head side fail or build-error |
| 5 | `NEW_API` (shown "NEW API") | head pass, base build-error |
| 6 | `DETECTS` | head pass, base fail |
| 7 | `INERT` | head pass, base pass or skip |

**Canonical state of a declared test (head only), first match wins:** `EXCLUDED`, `MISSING` (absent), `FLAKY`, `SKIPPED`, `FAIL` (fail or build-error) and `PASS`.

`EXCLUDED` tests are left out of every rule below: they neither satisfy "at least one DETECTS" nor block.

**Run status**, for each plan and head: `pending`, `complete` or `unavailable`, and for a receipt also `not-applicable`. A run is unavailable when the executor fails, the job times out, the sandbox cannot be built, the toolchain does not match, the source check of section 3 fails, the receipt has no merge base, or the instance has no executor configured. **Unavailable is never treated as a pass.** The canonical gate and the `required` receipt gate read it as "not complete" unless the owner unblocks the job (section 7). The `before-approval` receipt gate does not refuse it.

**Retry.** An attempt that an executor leased and then lost or failed (a lease that expired, a run past its time, a runtime error, a failed fetch of a dependency) is retried automatically, up to 3 failed attempts with a backoff. After the third, the run is `unavailable` with the reason of the last failure, final for that job, and the owner can unblock it. A run that the repository caused (a failed source check, a toolchain mismatch, a size cap, no merge base, a dependency that fails validation, a `replace` outside the tree, a resource kill) is final for its head and binding, because another attempt would give the same answer.

**No executor.** The service tracks when it last saw an executor, by a lease request or a heartbeat. When none was seen for the lease wait (2 minutes by default †), its pending jobs become `unavailable` with the reason `no-executor`, at once and with no retry. The test is the time since an executor was seen, never the age of a job: a healthy executor that works through a queue keeps every queued job pending. After a restart of the service the clock starts at startup. So a dead executor reads `unavailable` within minutes, and the owner can unblock the head. When no executor is configured, the jobs are still created, already `unavailable` with `executor-not-configured`. Neither outcome is final for good: once an executor is configured, or a real request from one (a lease request or a heartbeat) arrives after a `no-executor` outcome, the checks job creates a fresh job for each open pull request's current head and plan whose current job read that way. An unblock given for the old job does not carry over. The fresh job's outcome governs, and if it comes back unavailable again, the owner can unblock it.

**GitHub failures are not executor failures.** A failed GitHub read (the tree at lease time, the merge base, an archive, a refused request budget, a secondary limit) never consumes an attempt and never reads `no-executor`. The job stays pending, shows the reason `github-unavailable`, and the read is tried again with a delay, for at most 30 minutes † from the first failure. Past that the job is `unavailable` (`github-unavailable`), final for that job, and the owner can unblock it. The bound applies only to a pending job: a leased job is never ended by it. An executor that gets a 502 on an archive gives the lease back without consuming an attempt.

### 6. Mode summaries of a receipt

The owner names a mode when approving. A mode only judges the receipt; it never changes it.

- `fix`: met when at least one test is `DETECTS` and none is `FAILS`, `MISSING` or `FLAKY`.
- `feature`: met when none is `FAILS`, `MISSING` or `FLAKY`, and at least one is `DETECTS` or `NEW_API`. All `INERT` is "not met: no test detects anything".
- `refactor`: met when every evaluated test is `INERT`.
- No mode: counts only, no judgment.
- An empty receipt (no evaluated test function, or every test `EXCLUDED`): `fix` and `feature` are not met, `refactor` is met. The page shows these summaries. The receipt gate does not judge an empty receipt (section 7).

### 7. Gates, and the owner's unblock

**The receipt gate, on `approve`.** It reads the receipt of the head that is current when the command is processed. It applies only to an approve that names the proposal's current revision, and only to an applicable receipt.

- `off` and `advisory` never refuse.
- `before-approval` refuses only while the receipt is `pending`. It never refuses an unavailable receipt: the owner chose to look at the receipt, whatever it says.
- `required` refuses unless the receipt is `complete`, the approve carries a mode, and the summary for that mode is met. An unavailable receipt refuses.
- A complete receipt that is empty (section 2) is not refused whatever the level, and the approve needs no mode.

The code is verified and consumed before the refusal. Otherwise anyone could take the code from a refused comment, which is public, and post it again with another mode. The refusal is a recorded disposition, `receipt-refused`, with an acknowledgment. It records no consent. The `SuiteWard / Contract` check does not change, because a refused command has no effect on it ([D-COMMANDS](../plan/decisions.md#d-commands-pr-commands-identifiers-and-bootstrap)). The next attempt needs the next code. The ledger records the receipt state and the mode at approval time whether the approve was refused or not.

**The canonical gate, on the Contract check.** Under `canonical: required` the check can be `success` only when the current head's canonical run is `complete` with no `FAIL`, `MISSING` or `FLAKY` (`SKIPPED` is allowed and counted). While the run is pending or unavailable the check stays `in_progress` with the reason, never `success` from integrity alone. A `FAIL`, `MISSING` or `FLAKY` is `failure`. A run with no declared test is `complete`, and the check says "0 tests ran". `advisory` shows the results and never changes the check. The gate acts only where the check would otherwise be `success`, as `runner-uncovered` does, so it only withholds: a pass never creates a `success` that approval and integrity did not already give.

**After an approval,** a push that breaks the approved tests keeps the approval. Only the canonical gate reacts, by failing the check until a push fixes the head.

**`/suiteward unblock <code>`: the escape.** The canonical declaration governs the pull request that changes it, so a dead executor would keep every check `in_progress` and refuse every approve under `required`, the approve of the pull request that weakens the policy included. The ruleset has an empty bypass list. The owner's unblock is the escape.

- **Who and how.** Only the enrolled owner. It carries a TOTP code, verified and consumed like an `approve`'s, so an agent with the owner's GitHub identity cannot post it.
- **What it applies to.** The pull request's current head, the head that the synchronization observed when the command is processed. It binds to **jobs**: the jobs of that head that are `unavailable` at that moment, the canonical job and the applicable receipt job. A job created because no executor is configured is one of them, because such jobs are created already `unavailable`. If no job is unavailable, the command is refused (`unblock-refused`, reason `not-unavailable`), and its code stays consumed.
- **What it lifts.** Only those unavailable jobs, and only in the gates: the canonical gate stops withholding the Contract check, and the receipt gate stops refusing (with no receipt there is no summary to meet, so the approve needs no mode). It never lifts a failing, missing or flaky test, a pending run, or any other reason a check is not `success`. An approve of an unblocked head needs no mode, whatever the receipt level.
- **What it leaves.** A new push is a new head and needs its own unblock if its run is unavailable too. So does a job created later for the same head (a new governing manifest after an approval, a changed policy, or a fresh job after `no-executor` or `executor-not-configured`): it is another job. The unblock changes no result: the job stays `unavailable`.
- **What the checks say.** The Contract check and the Receipt check of that head say that the owner unblocked it and that the tests did not run.
- **Dispositions.** `unblocked` and `unblock-refused`, in the command ledger, with the record of the plans it lifted.

An agent cannot use it: it needs the TOTP code. An adversary that makes runs unavailable gains nothing from the unblock, which only the owner can post. The evidence of a failure is never lifted.

### 8. The `SuiteWard / Receipt` check

A second check run, not required. One run per head and generation, posted once (no update), like the Contract check. It exists only for an applicable receipt. Its conclusion:

- `in_progress` while the receipt is pending;
- `failure` when any evaluated test is `FAILS` or `MISSING`;
- `neutral` when the receipt is `unavailable`;
- `success` otherwise.

The conclusion never depends on the mode. `neutral` is allowed here only because this check is never required. [ADR 0015](0015-required-contract-check.md) still forbids `neutral` for the required Contract check. The text holds counts only, with no repository text and no link, because a check summary is public. Test names are on the receipt page ([runtime contract](../contracts/runtime.md#the-receipt-page)).

### 9. Commands

- `/suiteward approve P<n>-R<m> [fix|feature|refactor] <code>`. The mode is optional, except under `receipt: required` for an applicable receipt. `revoke` takes no mode.
- `/suiteward mode <fix|feature|refactor>`. Owner only, no code. It sets the **preview** mode of the pull request, which the receipt page shows. No gate ever reads it.
- `/suiteward unblock <code>`. Owner only, with a code. See section 7.

**The mode comes from the owner, never from candidate text.** The gating mode travels in the approve, which carries a TOTP code. The preview mode has no code, so anyone who holds the owner's GitHub identity can set it, and it therefore decides nothing.

### 10. The executor is a separate process

`suiteward execute` is the same binary as the service, run outside the service's container. It holds only the executor token. It has no database role, no GitHub credential and never opens the service's artifact store, and it refuses to start when it finds the service's credentials in its environment. The service keeps the App key and the database and never receives a container-runtime socket.

The executor speaks to the service over `/executor/v1` on the service's HTTP listener, with a bearer token that is separate from the MCP token. The service gives it a job with a lease, streams the commit archives from GitHub (the executor never holds a GitHub token), serves the manifest files, and validates and sanitizes the result before it stores anything. The wire contract is in the [runtime contract](../contracts/runtime.md#the-executor-api). The reads that execution makes of GitHub (trees, the merge base, archives) go through the same client and count in the D-SYNC budget and pacing.

**Where the executor runs.** A native process on both tiers, which uses the runtime CLI. It is not a container of the service.
- **Isolated.** It runs under its own account, `suiteward-exec`, as a systemd user unit with linger, and the unit sets a memory limit (task T9 sets the value). A drop-in `Delegate=cpu cpuset io memory pids` is written before the stack starts, so that the account gets the cgroup controllers that the limits need. nft rules on `meta skuid` reject the instance-metadata addresses `169.254.169.254` and `fd00:ec2::254` by destination, with no port match. `hidepid` is used only if it is made persistent and tested.
- **Co-located.** It is a host process, and task T9 decides its lifecycle for each operating system.
- **Egress.** Unlike the sandbox, the executor has a network: it reaches the service and `proxy.golang.org`, which the prefetch of section 11 needs, and the `storage.googleapis.com` that the proxy redirects module zips to. An egress allow-list for those two names is recommended.
- **A known limit.** A Podman machine's published loopback port does not always answer from Windows. `doctor` detects it and the executor reports it. The run then reads `no-executor`, and the owner can unblock it.

### 11. The sandbox and the Go profile

Every run gets disposable containers from a container runtime CLI (`podman` or `docker`), with one image pinned by digest for each profile. **No container has any network at all** (owner decision). Every container has: no network, a read-only root, a tmpfs work directory of 1 GiB that counts in the memory, all capabilities dropped, `no-new-privileges`, user 65534, the limits `--memory 3g --memory-swap 3g --cpus 4 --pids-limit 512` †, and no copy of its output kept by the runtime on the host (`--log-driver=none` on both engines; amended 2026-10-27, because Docker on Linux keeps an unbounded `json-file` by default: the attached CLI still receives the output, which is all the executor reads). A runtime that cannot honor one of them makes the run `unavailable`; it never runs a weaker sandbox. The evidence records the name and the version of the runtime. The sources, the composed tree and the module files live only in the sandbox's own filesystem (section 3). Spike S0 (`docs/spikes/s4-sandbox.md`) measures each property on both tiers.

**Dependencies, per profile: a prefetch outside the sandbox.** For Go, the executor fetches the modules that the `go.sum` of each side names (the head's, and for a receipt the base side's), and the sandbox needs no network.
- The executor fetches from `proxy.golang.org`, and follows the proxy's redirect for a module zip only to `storage.googleapis.com`, over HTTPS; it follows no other redirect, and the prefetch sends no request to any other host. An egress allow-list for those two names is recommended. An IP set is brittle, because the addresses rotate. (Corrected 2026-10-27: this section said "`proxy.golang.org` only", which would stop the zips that the proxy redirects. The correction states what the executor does and changes no decision.)
- Before it fetches, it validates every module path and version (`golang.org/x/mod/module.CheckPath`, `EscapePath` and `EscapeVersion`, and semver) and bounds the count and the bytes. A `replace` to a directory inside the tree needs no fetch. A `replace` to one outside the tree fails. A side whose `go.mod` says `go 1.14` or later and that has `vendor/modules.txt` uses `vendor/`, and nothing is fetched for it (owner, 2026-10-26).
- The files enter the sandbox inside the same stdin tar as the source, counted against the tmpfs and the memory, and never touch a host filesystem. The modules stream from the proxy into the tar. They are buffered only when the proxy gives no length, and then within the bounds, so the executor holds in memory at most one module and no whole tree entry: an entry's blob SHA-1 is computed while it is copied (section 3). The sandbox runs `go` with `GOPROXY=file:///...`.
- **Integrity.** The bytes are authenticated by TLS to the proxy and by `go.sum`, which the candidate controls. That is equivalent to a networked install, and not stronger. The sandbox has no checksum database.
- A validation failure, a `replace` outside the tree and a bound that is passed are `unavailable` with the reason `install-failed`, deterministic and final for the job. A fetch error (the proxy, the network, a private module) is a counted infrastructure failure, `fetch-failed`: it is retried up to 3 failed attempts with a backoff, and then it is final for that job and the owner can unblock it. No attempt is made to tell a private module from a network failure. In M2.1 a private module works only when it is vendored. Fetching private modules hosted on GitHub through the App's own read access, under a governed list of module prefixes, is a later phase (`D-EXECUTION-LATER`).
- Python and TypeScript dependencies run package code when they are installed, so each will decide its own safety in M2.2 and M2.3. Nothing is decided for them here.

**Layout.** The tree is extracted under `/work/src` and `go` runs there; go's own state (`HOME`, `TMPDIR`, the module and build caches, the module proxy) lives under `/work/.suiteward`, outside the tree, so no file of a candidate can reach it (a planted `go/env` or build-cache entry would otherwise steer `go`).

**Go environment.** `GOENV=off`, `GOTOOLCHAIN=local` (a `go.mod` that needs a newer Go is `unavailable`, `toolchain-mismatch`), `GOWORK=off`, `GOFLAGS` empty (so `go` picks `-mod=vendor` for a consistent `vendor/` and `-mod=readonly` otherwise, its own default), `GOPROXY=file:///...` (the prefetched files), `GOSUMDB=off`, a fresh `HOME`, no CI variables. `GOPRIVATE`, `GONOSUMDB`, `GOINSECURE` and `GONOPROXY` are unset. The sandbox's environment is exactly the variables listed here (with `PATH`, `TMPDIR` and the module and build cache paths) and nothing inherited from the executor. A vendored side uses `vendor/`, which `go` checks against `go.mod`; an inconsistent `vendor/` is a build error of that side, as it would be in CI. The Go image is pinned by digest in the code next to the profile, like the PostgreSQL pin. Tests run as `go test -json -count=1 -shuffle=on -run '^(Name1|Name2)$' <package>`, untagged tests only.

**Size check before `go test`.** While the tar streams, and before `go test` starts, the executor sums the uncompressed size of the composed tree, the bytes of the module zips and their unpacked size (read from each zip's directory once the zip has passed). When the sum passes half of the tmpfs (512 MiB of the 1 GiB), it refuses with `too-large`, which is deterministic. The other half is left to the Go build cache. A build cache larger than that still fills the tmpfs, and the run then reads as a build error: a residual risk this check does not remove. Text such as "no space left" in the output is never trusted.

**Timeouts.** `go test -timeout` equals `limits.testTimeoutSeconds` (600 s). A test binary that runs past it panics, and `go test` itself kills it a minute later, so the test reads `fail`. One invocation holds a cold build and every package of its chunk, so no wall clock bounds an invocation: `EXECUTOR_JOB_TIMEOUT` bounds the whole leased attempt and ends it as `timeout`, which is counted and retried, and the executor's only time kill is at the attempt's deadline (less 30 s), after which it posts nothing. It must be at least 12m30s, and the service refuses a smaller value (amended 2026-10-27: this said "at least the bound of one test binary", which was 11m, so that the executor's drop, 30 s before the deadline, came before `go test`'s own kill and a hung test read `timeout` and not `fail`). The floor adds up the test bound (600 s), `go test`'s own kill a minute later, the executor's 30 s margin and 60 s for everything around the test binary: the archive download, the prefetch, the tar, the container start and a cold compile before it, and the parse and the post of the result after it. It covers the first run of a hung test and not its reruns: a failing test is run again twice, so a test that hangs on every run still ends the attempt as `timeout` at the default of 30 minutes (counted, retried, `unavailable` after the third failed attempt, which fails closed), never as `fail`. `resource-limit` comes from the memory limit, an exit status 137 that the executor did not cause, and the output bound.

**Resource kills.** Only an out-of-band signal counts: the runtime's `inspect` field `.State.OOMKilled`, the container's exit status 137 that the executor did not cause (with `go test` as the container's process through `exec`, so that the status is Go's), and an output past the executor's bound. A container that the executor killed (at the attempt's deadline, or in the shutdown grace) is never one: the attempt is dropped. Text in the build output never counts. The reason is `resource-limit`: deterministic, final for the job, and the owner can unblock it. A candidate can cause it, for example with a test that hogs memory, so the owner should read the cause before unblocking.

### 12. Trust layers: detect and flag

SuiteWard cannot prove that code under test behaves the same outside the sandbox. It adds layers that detect and flag:

- the expected tests come from static analysis (`go/parser`), so a test that does not report is `MISSING`;
- the runner's report is cross-checked with the process exit status. When they contradict each other (a non-zero exit with no failing test event and no package build failure in the report, expected or not, or a zero exit with one), every expected test of that `go test` invocation (one side) reads `fail`;
- deterministic markers flag test-aware code in the non-test Go files that differ between the base and the head: `testing` imported outside `_test.go`, `testing.Testing()`, `flag.Lookup("test.`, `os.Exit` inside `init`, reads of `_test.go` paths, and reads of `TESTING` or `CI`. Markers belong to the receipt, which has a base commit; the canonical plan has none;
- tests run in a shuffled order with no CI variables.

A marker is a line to look at, never a verdict. **The honest limit: code that behaves differently under test cannot be ruled out.** Execution is evidence about a head. It is not authority, and a result never enters the consent binding.

A forged result can do little. It cannot grant approval or consent, because the TOTP code and the exact-revision approval stay as they are. It can lift a withholding (a `required` gate), flatter advisory evidence, hold a check (a failing result for a head that passes) or make a `required` receipt refuse an approve. It cannot grant approval, but it can delay work. That is why sealing matters per tier.

### 13. Sealing, per tier

- **Isolated:** the executor runs under its own account on the host, which the agent never reaches. Its binary is pinned to the signed release and checked by `doctor`. Sandbox images are pinned by digest.
- **Co-located:** the executor runs on the agent's machine. The same pins catch drift, not a determined agent. This is reduced assurance, and the check and the page say so, as for approvals ([ADR 0027](0027-agent-human-trust-separation.md)). The installer states it once, on the page that introduces the tier, in the summary of an install or an upgrade and at the end of a plain install or of a plain upgrade that upgrades; the rows of `doctor` do not repeat it.
- Remote attestation is a candidate for M3 or a managed service.

### 14. Evidence binding

Every stored result records: the head SHA; the base SHA (receipt); the governing manifest digest and its source (`V<n>` or `P<n>-R<m>`); the digest of the canonical declaration; the policy levels; the profile; the executor's version and binary SHA-256; the name and version of the container runtime; the sandbox image digest; and the start and end times. The executor reports its own version, hash and image, so they are as trustworthy as the executor.

## Alternatives rejected

- **The executor inside the service.** It would need a container-runtime socket, and whoever controls that socket controls the host. It would also run candidate code in the process that holds the App key and the database credentials.
- **A database role for the executor.** The executor runs candidate code. A role, even a narrow one, makes a sandbox escape a database compromise. A lease API needs no role.
- **Reading the pull request head's own `.suiteward.yml` for the base-side rule.** The head would decide what moves into the base run. See section 2.
- **A `mode` command without a code that sets the gating mode.** Whoever holds the owner's GitHub identity could pick the mode that the gate judges. Hence the preview-only `mode` command, and a mode that travels in the code-bearing approve.
- **Taking the mode from labels, the title or the description.** That is candidate text.
- **Setting the gating mode in the policy.** The mode describes one change. A policy cannot know it.
- **An unblock without a code, or one that outlasts the head.** The first could be posted by an agent. The second would let a stale decision cover code the owner never saw.

## Consequences

- SuiteWard can run tests, and says what it ran. ADR 0015 says "Success never means that SuiteWard executed the tests". Under `canonical: required` that is no longer true, and the Contract check summary then states the executed counts. ADR 0015 carries a note for this, and D-CHECKS ("never claims that tests ran") is amended in [D-M2.1](../plan/decisions.md#d-m21-canonical-execution-the-receipt-and-the-go-profile).
- Turning execution on is a governed change. An installation that never names a profile is unchanged.
- Under `required` levels, a missing or broken executor blocks work until it is fixed or the owner unblocks each head. The unblock needs a TOTP code for every head, and it is reachable once the run reads `unavailable`, which a missing executor does after the lease wait (section 5).
- A new component runs candidate code. The sandbox, the executor's isolation and the pins are part of the [threat model](../security/threat-model.md).
- The sandbox has no network at all, so the executor prefetches the dependencies. It needs outbound HTTPS to `proxy.golang.org` and `storage.googleapis.com` (an egress allow-list for those two names is recommended), and a module that the proxy cannot give, a private one included, cannot be tested in M2.1. The bytes of a dependency are as safe as TLS and the candidate's `go.sum`, which is what a networked install would give.
- A candidate can cause a `resource-limit`, for example with a test that hogs memory. The owner should read the cause before unblocking it.
- The service answers on `/executor/v1` a request that changes state. The runtime contract's "no endpoint changes state" is amended for it.
- Execution cost: one canonical run per head of every open pull request, plus one receipt run per head whose revision is applicable. Both are bounded by the lease wait and the job timeout, and their GitHub reads share the synchronization's budget.

## Open items and deferred work

No decision is open. The owner accepted the rules that the contracts state, among them: an unblock binds to the jobs that are `unavailable` when it is processed, so a job of the same head that is created or becomes unavailable later needs another unblock; the "not evaluated: pattern not approved yet" list exists only for an applicable receipt, because it is stored with the job; and no stored result is deleted in M2.1.

Deferred:

- **Sealed tests** kept outside the repository, **mutation testing** and **remote attestation** are not in M2.1.
- **Integration tests that need a service** (PostgreSQL in a sandbox) are not run.
- **Python and TypeScript install steps** run package code. Their safety is decided with M2.2 and M2.3.
- **Pruning** of old per-test rows is not done in M2.1.
- **Execution by a managed service** stays with [D-MANAGED-EXECUTION](../plan/decisions.md#open).

## Note 2026-10-28: the unblock lifts a pending run since M2.1b

Section 7 says that an unblock never lifts "a pending run", and section 5 says that an unblock given for the old job does not carry over when a fresh job replaces a `no-executor` job. Owner decisions 5 and 6 of phase M2.1b change both, for a test runner that does not respond ([ADR 0029](0029-results-communication.md), decisions 5 and 6; the [runtime contract](../contracts/runtime.md#the-early-unblock-and-its-hold)). Everything else in this record stands.

- **A pending run can be unblocked, when the runner does not respond.** A head reads *not responding* when no executor was seen for about 30 seconds and a wanted job of the head waits for the runner. From then on `/suiteward unblock <code>` ends the head's open jobs that wait for the runner (a `leased` job, or a `pending` one that does not wait on GitHub) as `unavailable` (`no-executor`) and lifts them in the same transaction, with the jobs that are `unavailable` already. A run that a responding executor holds is refused (`runner-responding`). The unblock still needs the owner's TOTP code, still binds to the jobs it names and still lifts only the gates: it never lifts a failing, missing or flaky test.
- **An unblock holds.** The recovery rule does not replace a `no-executor` job that the owner unblocked, so the executor that comes back runs nothing for that head, and the next push runs as usual. A `no-executor` outcome that the owner unblocked after the lease wait holds in the same way. A job that ended for `executor-not-configured` is still replaced once a token is configured. SuiteWard warns on the pull request, once, that the head was unblocked and that its tests did not run, when the runner is seen again.
- **The wait is shorter.** The consequence "the unblock is reachable once the run reads `unavailable`, which a missing executor does after the lease wait" now holds from about 30 seconds on. `EXECUTOR_LEASE_WAIT` is unchanged: it still ends the pending jobs as `unavailable` (`no-executor`).
