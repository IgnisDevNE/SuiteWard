# ADR 0028: Canonical execution and the receipt

- **Date:** 2026-10-26
- **Status:** Accepted for phase M2.1 (the Go profile). Not implemented yet. Python (M2.2) and TypeScript (M2.3) follow.
- **Product:** SuiteWard
- **Scope:** Running the protected tests in disposable sandboxes: canonical execution on every head, the receipt of the tests a pull request adds or changes, the policy that turns them on, the executor, and the evidence they leave.
- **Amends:** [ADR 0008](0008-progressive-integrity-and-execution.md) (the definition of M2). It also changes two statements of [ADR 0015](0015-required-contract-check.md) (see "Consequences").
- **Related:** [ADR 0002](0002-exact-revision-approval.md), [ADR 0007](0007-canonical-contract-authority.md), [ADR 0013](0013-protected-scope-and-agent-assisted-review.md), [ADR 0027](0027-agent-human-trust-separation.md), D-APPROVAL-CONTEXT, D-COMMANDS and D-CHECKS in the [decision register](../plan/decisions.md), the [M2.1 phase page](../plan/phases/M2.1.md), the [runtime contract](../contracts/runtime.md#execution-m21-normative) and the [persistence contract](../contracts/persistence.md#execution-m21-normative).

Names, sizes and durations that the contracts mark with † are choices of task A0, made inside this decision. The integrator may change them under the register's rule on implementation choices. The open items below are not.

## Context

M1 protects the bytes of the tests. It does not run them: [ADR 0014](0014-merge-triggered-canonical-promotion.md) and ADR 0015 leave test execution to the project's own CI. Two things are missing.

- Nothing in SuiteWard runs the approved tests on the code a pull request changes. The owner cannot tell, from SuiteWard, whether the approved contract still passes on this head.
- The owner approves a changed or added test without seeing whether it detects anything. A test that passes on the code before and after the change protects nothing.

ADR 0008 planned M2 as canonical execution with a `DockerExecutionBackend`. This record settles the first profile (Go), the trust model, the policy, and what the owner sees. The owner's decisions of 2026-10-26 are on the [phase page](../plan/phases/M2.1.md#owner-decisions-2026-10-26).

## Decision

### 1. Execution is opt-in, through the governed policy

Nothing runs until the canonical declaration names a profile. `.suiteward.yml` gains an `execution` block:

```yaml
execution:
  profile: go            # the only value in M2.1
  canonical: advisory    # off | advisory | required
  receipt: advisory      # off | advisory | before-approval | required
```

- **Defaults.** With a `profile`, `canonical` and `receipt` default to `advisory`. With no `execution` block, or with no `profile`, nothing runs: both levels are `off`, whatever they say.
- **Strict keys.** An unknown key under `execution`, or a `profile` other than `go`, makes the declaration invalid, as an unknown top-level key does.
- **Governed.** The policy is part of the declaration, so changing it is a protected change that needs the owner's approval with a TOTP code. Turning execution on is such a change.
- **The canonical declaration governs every pull request, the one that changes it included.** The levels come from the declaration of the Suite's current version. A pull request that weakens them still meets the old levels. Before `V1` nothing runs.
- **Weakening, in order.** `canonical`: `off` < `advisory` < `required`. `receipt`: `off` < `advisory` < `before-approval` < `required`. Removing `profile`, or the whole block, weakens both levels to `off`. A weakening is reported as a reduction in the revision comment, like a removed pattern. A strengthening is not.
- **The toolchain is not configurable.** The Go version is the release's pinned Go image.
- A later task may suggest `execution: {profile: go}` when a `go.mod` exists. A suggestion grants nothing.

### 2. One engine, two plans

Each plan runs on a head and is evidence about that head. Neither enters the consent binding ([D-APPROVAL-CONTEXT](../plan/decisions.md#d-approval-context-approval-binding-for-implementation-only-changes) holds).

**The canonical plan** runs for the head of every open pull request, when `canonical` is not `off`. It runs the head tree with the **governing manifest** laid over it. The governing manifest is the manifest of the current canonical version `V<n>`. Once a consent covers this head, it is the manifest of the consented revision. The overlay works on the files that the governing declaration's `include` matches:

- a head file that `include` matches is replaced by the manifest's bytes, checked against the manifest's digest;
- a head file that `include` matches and the manifest lacks is removed;
- a manifest file that the head lacks is added.

The head can therefore add a test but not silence one. The expected tests are the top-level test functions declared in the manifest's Go test files.

Fork pull requests get no run: they fail closed, as in [D-INTEGRATION](../plan/decisions.md#d-integration-merge-methods-and-the-integrated-source).

**The receipt** runs for a head that has a change proposal revision, when `receipt` is not `off`. (This is provisional. The design note says "a pull request whose revision changes protected test files", and the service cannot tell test files from other protected files without parsing Go; see open item 2.) Its **evaluated tests** are the top-level test functions added or changed, by declaration text, in the files that the **canonical** `include` matches and whose bytes differ from the canonical manifest. "Changed" means against the canonical version the revision expects, not against the merge base. Test files under patterns that only the head adds are listed as "not evaluated: pattern not approved yet".

- **Head side:** the head tree.
- **Base side:** the base commit's tree plus the head's version of the files that hold evaluated tests. Nothing else of the head crosses: source, `go.mod`, `go.sum`, `vendor/`, runner files and `.suiteward.yml` all come from the base commit.
- **The base commit** is the merge base of the head and the pull request's base branch.

Why this base side:

- Only the approved `include` decides what crosses from head to base. A head cannot add a pattern to its own declaration to pull its source into the base run, which would make every test pass on both sides.
- The base run is the code before the change, with its dependencies. A fix that is a dependency bump is still detected: the old dependency fails the test and the new one passes it.

What it costs:

- A test that needs a new dependency, or a symbol that exists only in the head, does not build on the base. It reads NEW API. In Go a build failure belongs to a package, so every evaluated test of that package reads NEW API. This is a documented limit.
- Only test code crosses, not test inputs. A new test that reads an added or changed `testdata/` file fails on the base because the file is missing, and reads DETECTS. A new test that calls a helper in another `_test.go` file reads NEW API. DETECTS means "failed on the base side", not "fails for the reason this change fixes". See open item 3.

### 3. The source is the commit tree, not the archive

GitHub builds archives with `git archive` semantics. A candidate's `.gitattributes` can drop files (`export-ignore`) or rewrite them (`export-subst`), and LFS files arrive as pointers. So the service reads the tree entries of each commit it uses (path, mode, git blob SHA-1; one recursive tree read, refused when GitHub truncates it) and gives them to the executor with the job. The executor recomputes the blob SHA-1 of every extracted file. A missing, extra or mismatched file makes the run `unavailable`. Submodules are skipped and listed. A symlink is created only when it resolves inside the tree; any other symlink makes the run `unavailable`.

### 4. Which tests a profile can run

The Go profile builds its expected list from files that match the sandbox build context (`go/build.Context.MatchFile` with the sandbox's `GOOS` and `GOARCH` and no extra tags). A file with `//go:build integration`, or named `_windows_test.go`, is not expected. Only `func TestXxx(t *testing.T)` at top level counts. `TestMain`, benchmarks, fuzz targets and examples are not tests here.

A declared test that the profile cannot run is `EXCLUDED`. It is decided statically, listed with its reason, never counted as a pass and never blocking. **Canonical assurance does not cover excluded tests.** The page says how many there are. An evaluated set that is all `EXCLUDED` is an empty receipt. Integration tests that need a service such as PostgreSQL are outside M2.1.

### 5. Observations, states and their precedence

Each side of each test yields one observation: `pass`, `fail` (a failed assertion, a panic or a timeout), `build-error` (the package or file did not build, so the test never ran), `skip`, `absent` (expected, no result) or `excluded`. A failure is re-run on the same side (2 reruns†). A side whose runs differ makes the test flaky. The service derives the states below from the observations the executor reports.

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

**Canonical state of a declared test (head only):** `PASS`, `FAIL` (fail or build-error), `SKIPPED`, `MISSING` (absent), `FLAKY` and `EXCLUDED`.

`EXCLUDED` tests are left out of every rule below: they neither satisfy "at least one DETECTS" nor block.

**Run status**, for each plan and head: `pending`, `complete` or `unavailable`. A run is unavailable when the executor fails, the whole job times out, the sandbox cannot be built, the toolchain does not match, or the source check of section 3 fails. **Unavailable is never treated as a pass.**

### 6. Mode summaries of a receipt

The owner names a mode when approving. A mode only judges the receipt; it never changes it.

- `fix`: met when at least one test is `DETECTS` and none is `FAILS`, `MISSING` or `FLAKY`.
- `feature`: met when none is `FAILS`, `MISSING` or `FLAKY`, and at least one is `DETECTS` or `NEW_API`. All `INERT` is "not met: no test detects anything".
- `refactor`: met when every evaluated test is `INERT`.
- No mode: counts only, no judgment.
- An empty receipt (no evaluated test): `fix` and `feature` are not met, `refactor` is met. A revision that changes no test (only `.suiteward.yml` or a runner file, say) therefore needs `refactor` under `receipt: required`.

### 7. Gates

**The receipt gate, on `approve`.** It reads the receipt of the head that is current when the command is processed.

- `off` and `advisory` never refuse.
- `before-approval` refuses unless that receipt is `complete`.
- `required` refuses unless that receipt is `complete`, the approve carries a mode, and the summary for that mode is met.

The code is verified and consumed before the refusal. Otherwise anyone could take the code from a refused comment, which is public, and post it again with another mode. The refusal is a recorded disposition, `receipt-refused`, with an acknowledgment. It records no consent. The `SuiteWard / Contract` check does not change, because a refused command has no effect on it ([D-COMMANDS](../plan/decisions.md#d-commands-pr-commands-identifiers-and-bootstrap)). The next attempt needs the next code. The ledger records the receipt state and the mode at approval time whether the approve was refused or not.

**The canonical gate, on the Contract check.** Under `canonical: required` the check can be `success` only when the current head's canonical run is `complete` with no `FAIL`, `MISSING` or `FLAKY` (`SKIPPED` is allowed and counted). While the run is pending or unavailable the check stays `in_progress` with the reason, never `success` from integrity alone. A `FAIL`, `MISSING` or `FLAKY` is `failure`. `advisory` shows the results and never changes the check.

The gate only withholds. It acts where the check would otherwise be `success`, and a pass never creates a `success` that approval and integrity did not already give (provisional; open item 6).

**After an approval,** a push that breaks the approved tests keeps the approval. Only the canonical gate reacts, by failing the check until a push fixes the head.

### 8. The `SuiteWard / Receipt` check

A second check run, not required. One run per head and generation, posted once (no update), like the Contract check. Its conclusion:

- `in_progress` while the receipt is pending;
- `failure` when any evaluated test is `FAILS` or `MISSING`;
- `neutral` when the receipt is `unavailable`;
- `success` otherwise.

The conclusion never depends on the mode. `neutral` is allowed here only because this check is never required. [ADR 0015](0015-required-contract-check.md) still forbids `neutral` for the required Contract check. The text holds counts only, with no repository text and no link, because a check summary is public. Test names are on the receipt page ([runtime contract](../contracts/runtime.md#the-receipt-page)).

### 9. Commands

- `/suiteward approve P<n>-R<m> [fix|feature|refactor] <code>`. The mode is optional, except under `receipt: required`. `revoke` takes no mode.
- `/suiteward mode <fix|feature|refactor>`. Owner only, no code. It sets the **preview** mode of the pull request, which the receipt page shows. No gate ever reads it.

**The mode comes from the owner, never from candidate text.** The gating mode travels in the approve, which carries a TOTP code. The preview mode has no code, so anyone who holds the owner's GitHub identity can set it, and it therefore decides nothing.

### 10. The executor is a separate process

`suiteward execute` is the same binary as the service, run outside the service's container. It holds only the executor token. It has no database role, no GitHub credential and never opens the service's artifact store, and it refuses to start when it finds the service's credentials in its environment. The service keeps the App key and the database and never receives a container-runtime socket.

The executor speaks to the service over `/executor/v1` on the service's HTTP listener, with a bearer token that is separate from the MCP token. The service gives it a job with a lease, streams the commit archives from GitHub (the executor never holds a GitHub token), serves the manifest files, and validates and sanitizes the result before it stores anything. The wire contract is in the [runtime contract](../contracts/runtime.md#the-executor-api).

### 11. The sandbox and the Go profile

Every run gets disposable containers from a container runtime CLI (`podman` or `docker`), with an image pinned by digest. Required properties, for the test run: no network, a read-only root, a tmpfs work directory, all capabilities dropped, `no-new-privileges`, limits on processes, memory and CPU, a non-root user, never on the `suiteward` network, no route to the host's loopback or to instance metadata. A runtime that cannot honor one of them makes the run `unavailable`; it never runs a weaker sandbox. Spike S0 measures each property on both tiers.

**Install step, per profile.** The only step with network is the dependency install. For Go it is `go mod download` into a module cache keyed by the digest of `go.sum`. Go's download runs no repository code. The cache is read-only for the test run. Python and TypeScript install steps can run package code, so each will decide its own safety in M2.2 and M2.3. Nothing is decided for them here.

**Go environment.** `GOTOOLCHAIN=local` (a `go.mod` that needs a newer Go is `unavailable`, `toolchain-mismatch`), `GOWORK=off`, `GOFLAGS=-mod=readonly`, a fixed `GOPROXY` and `GOSUMDB`, a fresh `HOME`, no CI variables. A vendored repository uses the module download, not `vendor/`. Private modules are not supported. Tests run as `go test -json -count=1 -shuffle=on -run '^(Name1|Name2)$' <package>`, untagged tests only.

### 12. Trust layers: detect and flag

SuiteWard cannot prove that code under test behaves the same outside the sandbox. It adds layers that detect and flag:

- the expected tests come from static analysis (`go/parser`), so a test that does not report is `MISSING`;
- the runner's report is cross-checked with the process exit status, and a disagreement is never read as a pass;
- deterministic markers flag test-aware code in the non-test Go files that differ between the base and the head: `testing` imported outside `_test.go`, `testing.Testing()`, `flag.Lookup("test.`, `os.Exit` inside `init`, reads of `_test.go` paths, and reads of `TESTING` or `CI`;
- tests run in a shuffled order with no CI variables.

A marker is a line to look at, never a verdict. **The honest limit: code that behaves differently under test cannot be ruled out.** Execution is evidence about a head. It is not authority, and a result never enters the consent binding.

A forged result can do little. It cannot grant approval or consent, because the TOTP code and the exact-revision approval stay as they are. It can only remove a withholding (a `required` gate) or make advisory evidence look better. That is why sealing matters per tier.

### 13. Sealing, per tier

- **Isolated:** the executor runs under its own account on the host, which the agent never reaches. Its binary is pinned to the signed release and checked by `doctor`. Sandbox images are pinned by digest.
- **Co-located:** the executor runs on the agent's machine. The same pins catch drift, not a determined agent. This is reduced assurance, and the check and the page say so, as for approvals ([ADR 0027](0027-agent-human-trust-separation.md)).
- Remote attestation is a candidate for M3 or a managed service.

### 14. Evidence binding

Every stored result records: the head SHA; the base SHA (receipt); the governing manifest digest and its source (`V<n>` or `P<n>-R<m>`); the digest of the canonical declaration; the policy levels; the profile; the executor's version and binary SHA-256; the sandbox image digest; and the start and end times. The executor reports its own version, hash and image, so they are as trustworthy as the executor.

## Alternatives rejected

- **The executor inside the service.** It would need a container-runtime socket, and whoever controls that socket controls the host. It would also run candidate code in the process that holds the App key and the database credentials.
- **A database role for the executor.** The executor runs candidate code. A role, even a narrow one, makes a sandbox escape a database compromise. A lease API needs no role.
- **Reading the pull request head's own `.suiteward.yml` for the base-side rule.** The head would decide what moves into the base run. See section 2.
- **A `mode` command without a code that sets the gating mode.** Whoever holds the owner's GitHub identity could pick the mode that the gate judges. Hence the preview-only `mode` command, and a mode that travels in the code-bearing approve.
- **Taking the mode from labels, the title or the description.** That is candidate text.
- **Setting the gating mode in the policy.** The mode describes one change. A policy cannot know it.

## Consequences

- SuiteWard can run tests, and says what it ran. ADR 0015 says "Success never means that SuiteWard executed the tests". Under `canonical: required` that is no longer true, and the Contract check summary then states the executed counts. ADR 0015 needs its own note for this; this record states the change. D-CHECKS ("never claims that tests ran") is amended in [D-M2.1](../plan/decisions.md#d-m21-canonical-execution-the-receipt-and-the-go-profile).
- Turning execution on is a governed change. An installation that never names a profile is unchanged.
- Under `required` levels, a missing or broken executor blocks work. See open item 1.
- A new component runs candidate code. The sandbox, the executor's isolation and the pins are now part of the threat model ([threat model](../security/threat-model.md)).
- The service answers on `/executor/v1` a request that changes state. The runtime contract's "no endpoint changes state" is amended for it.
- Execution cost: one canonical run per head of every open pull request, plus one receipt run per head that has a revision. Both are bounded by a job timeout.

## Open items

These need the owner. Each names the provisional rule the contracts use meanwhile, where there is one.

1. **No escape under `required`.** The canonical declaration governs the pull request that changes it. If the executor is unavailable for good, or the Go profile cannot build the repository, then `canonical: required` keeps every Contract check `in_progress`, the pull request that weakens it to `advisory` included. `receipt: required` refuses every approve, the approve of that weakening included. The ruleset has an empty bypass list. Candidate answers: a pull request that only weakens `execution` is exempt from the gates, a break-glass for the owner, or `required` is not offered until a later phase. None is chosen. Until it is, `required` cannot be called safe.
2. **Revisions that change no test.** A revision may change only runner, workflow or declaration files. Is its receipt empty and `complete`, or is there none? Provisional: a receipt exists for every head that has a revision, and an empty one is `complete`. This decides whether such a revision can be approved under `before-approval` or `required`. Also open: whether the Receipt check is posted when no receipt applies.
3. **Test inputs on the base side.** Whether the head's `testdata/` files for evaluated tests should join the base run, to avoid a false DETECTS or NEW API.
4. **Which receipt the revision comment links.** The comment is per revision, posted once. The receipt is per head, and an implementation-only push keeps the revision. Provisional: the link opens the pull request's page, which shows the current head. The receipts of earlier heads stay in the ledger but have no link.
5. **Gate order for a reference the gate need not judge.** Provisional: the receipt gate applies only to an approve that names the proposal's current revision. Any other reference goes to `ProcessConsent`, which rejects it as before.
6. **Canonical gate precedence.** Provisional: it acts only where the check would otherwise be `success`, as `runner-uncovered` does, and `FAIL`, `MISSING` and `FLAKY` give `failure`.
7. **Lifetime of the preview mode, and retention.** Provisional: the latest `mode` of the pull request since the current enrollment, across heads and revisions. Results and per-test rows are never deleted in M2.1.
8. **Recovery from `unavailable`.** An unavailable run is final for its head and binding. Only a push, or a changed manifest or policy, queues a new one. There is no rerun command and no automatic retry.
9. **An empty expected set.** Provisional: a canonical run with no declared test is `complete` with zero tests and passes the gate.
10. **Markers on the canonical plan.** The canonical job has no base commit, so markers are receipt-only. Whether the canonical plan needs them is open.
11. **A report that contradicts the exit status.** Provisional: every test of that package that is not already `fail` or `build-error` is observed as `fail`.
12. **Levels without a profile are inert.** `execution: {canonical: required}` with no `profile` runs nothing and is not an error. Whether it should be invalid is open.
13. **The reference of the receipt.** The design note says the evaluated tests differ from the "governing manifest", and glosses it as "relative to the canonical". The contracts use the canonical version the revision expects. Against the governing manifest, the receipt of a head would turn empty the moment a consent covers it.
14. **`before-approval` and an unavailable receipt.** The design refuses unless the receipt is `complete`, so unavailable refuses. The phase page says "a pending one". The contracts follow the design.
15. **The GitHub budget.** A run reads two trees, the merge base and two archives, on top of the synchronization. The pacing of [D-SYNC](../plan/decisions.md#d-sync-polling-budget-and-mcp-boundary) counts only the synchronization's own requests. Whether execution reads count toward the same budget, and what a burst of pushes does to it, is not decided.

Deferred, not open:

- **Sealed tests** kept outside the repository, **mutation testing** and **remote attestation** are not in M2.1.
- **Integration tests that need a service** (PostgreSQL in a sandbox) are not run.
- **Python and TypeScript install steps** run package code. Their safety is decided with M2.2 and M2.3.
- **Execution by a managed service** stays with [D-MANAGED-EXECUTION](../plan/decisions.md#open).
