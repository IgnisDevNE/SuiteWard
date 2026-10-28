# ADR 0029: Results communication

- **Date:** 2026-10-28
- **Status:** Accepted for phase M2.1b. The owner's decisions of 2026-10-28 are on the [phase page](../plan/phases/M2.1b.md#owner-decisions-2026-10-28) (decisions 1 to 8). Five points that they do not settle are listed at the end for the owner, with the option this record writes down.
- **Product:** SuiteWard
- **Scope:** What SuiteWard says about its test runs and where: the comments of a head, the page of a pull request, the reason of each flagged test, the signal for a test that an edit weakened, a test runner that stops responding and the owner's early unblock, the installer that asks which installation, and the wording of every comment, check summary and page.
- **Amends:** [ADR 0028](0028-canonical-execution-and-the-receipt.md) (the unblock never lifts a pending run, and an unblock does not carry over to a fresh job; ADR 0028 carries its own dated note).
- **Related:** [ADR 0027](0027-agent-human-trust-separation.md), [D-ROADMAP](../plan/decisions.md#d-roadmap-the-order-after-m21) and D-APPROVAL-CONTEXT in the [decision register](../plan/decisions.md), the [M2.1 phase page](../plan/phases/M2.1.md) (decision 22), the [runtime contract](../contracts/runtime.md#results-communication-m21b-normative) and the [persistence contract](../contracts/persistence.md#execution-m21-normative).

Names, numbers and texts that the contracts mark with † are choices of task A0 of M2.1b, made inside this decision. The integrator may change them under the register's rule on implementation choices. The points the owner decided, at the end, are not such choices.

## Context

M2.1 runs the tests and keeps the evidence. It says little about it.

- A pull request with no protected change gets no comment about its run. A head that is run gets its counts only in a check summary, where the App can write no names.
- The page of results has no tables. It shows a state and a package and not why the state is what it is.
- An approved test that a change edits, and that fails in its approved version, looks like a green change. The owner has to know to look.
- A test runner that stops waits two minutes before anything says so, and `/suiteward unblock` then works only once the run reads `unavailable`.
- The installer asks for five flags to name an installation, and nothing can list the installations of a computer.
- Many comments, check summaries and page texts still use words that a person with little technical background does not know.

The App has `pull_requests: write` and no `issues` permission, and nothing edits a comment. That decides how anything below is said on GitHub.

## Decision

### 1. Three comments for a head, each posted once

The checks job writes `github_comment` messages. A comment is never edited. The outbox key and the hidden marker already make a key post once.

- **Results (decisions 1, 2 and 3).** One comment when every run that the head wants is final: complete, unavailable, or unblocked by the owner. The runs wanted are the canonical run, when `canonical` is not `off`, and the receipt, when `receipt` is not `off` and it applies. It is written only where the canonical declaration names a profile and some run is wanted, so a pull request with no protected change gets its comment from its canonical run. A head that moves before its runs end gets none. A closed pull request and a fork get none.
- **Not responding (decision 4).** One comment for each set of waiting jobs, when a head first reads *not responding* (below).
- **Runner back (decision 6).** One comment for a head that the owner unblocked early, when the test runner is seen again and the head is still the head of an open pull request.

Each key names the jobs it reports. A fresh job gets its own comment: the job that the recovery rule makes after the runner returns, and the canonical job that the head gets under the approved version of the tests once the owner approves. The approval would therefore add a second results comment for the same head, and the first run after an upgrade would write one for every open pull request whose runs are already final. The owner decided (2026-10-28, points 4 and 5 below): the set that follows an approval is announced unless both it and the head's earlier final set have nothing to say (no failing test, no tests that did not run), and only when the head has such an earlier set, so that a head always gets at least one comment and a later comment can correct an earlier one; the burst after an upgrade or a long outage is accepted.

A comment holds counts and the general situation, never a test name, a path or any text of the repository or of the runner (decision 3): what ran and what it found, how many tests are weakened and what to do, whether each kind of change (`fix`, `feature`, `refactor`) is met and why, the situation of a runner that does not respond, the next step, and the link to the pull request page. The payloads, the keys, the exact texts and the restart rules are in the [runtime contract](../contracts/runtime.md#the-comments-of-a-head).

Why one comment per head and not an edited one: the App cannot edit. Why a key bound to jobs: a comment that was posted must stay true of the run it reported, and a new run is a new fact.

### 2. One page for each pull request

`GET /pulls/{owner}/{repo}/{pull}` † shows the current head and revision in tables:
- the tests by state, with what each state means;
- the tests of each run (state, test, package, file, the reason in words, the weakened marker);
- the three kinds of change, met or not, and why;
- the situation (running, not responding, not run, unblocked), the evidence binding, and the preview mode;
- the change report of the revision without its hunks, with links to the full report of the revision and to the reports of the earlier revisions.

`/receipts/{owner}/{repo}/{pull}` answers 308 to the new route, because posted comments link it. `/reports/{owner}/{repo}/P<n>-R<m>` stays the page of one revision, with the hunks. The guard rules do not change: a loopback `Host` only, the four headers, escaping, one page built at a time.

**The size rule.** The page of M2.1 had an 8 MiB cap that answered 413. Two runs of 10,000 tests and the 4 MiB of hunks no longer fit it. This record takes the hunks off the page and gives every part a budget, counted in escaped bytes: 3 MiB † for a table of tests, 1 MiB † for the table of files, and 2 MiB † for the fixed part (256 KiB for each of its six lists of paths, 512 KiB for the rest). Rows are written in order of importance, the weakened tests first, then the flagged ones, then the rest. The rows that do not fit are counted in a sentence, and the counts stay complete. The parts add up to 9 MiB. The cap becomes 12 MiB † and is a guard that the budgets keep unreachable, with 503 and not 413 as its answer. A normal large pull request never gets 413, and no pull request can reach the cap.

### 3. The reason in words, and the meaning of each state

Decision 7. One fixed sentence for each flagged test, derived from the state and the stored runs (`pass`, `fail`, `build-error`, `skip`, `absent`). A timeout and an exit-status contradiction are stored as `fail`, so the sentence says "the test failed or did not finish". No test output is stored and none is shown.

*Flagged* is `FAIL`, `FAILS`, `MISSING`, `FLAKY`, `NEW API` and `INERT`: every state that is not a plain pass (`PASS`, `DETECTS`), a skip or an exclusion. An excluded test shows its stored reason too. The page also says what every state means: `PASS`, `FAIL`, `SKIPPED`, `MISSING`, `FLAKY`, `EXCLUDED`, `DETECTS`, `INERT`, `NEW API`, `FAILS`. The sentences are tables in the contract, and they are the reference wording for the comments and for the check summaries.

### 4. The weakened signal

An approved test that this change edits, and that fails in its approved version, is *weakened*. The set is the tests that the receipt marks `changed` whose canonical state, in the canonical run on the same head governed by the same canonical manifest, is `fail`, `missing` or `flaky`, joined on package and name. It needs both runs complete and is otherwise unknown, never an absence.

- **Where it shows.** The page names the tests and marks them. The results comment gives a count and a sentence under every level. The Contract check summary gives a count and a sentence in the summary of an `approval-missing` head under `canonical: required`, and it replaces F11a's generic sentence ("At least one approved test fails, is missing or is flaky… If this change edits that test, read the change before you approve…") when the count is more than 0. The generic sentence stays when the count is 0 or unknown, because it is still true. Check summaries keep no name. The Receipt check and the Contract check under `advisory` do not change.
- **How the check learns it, with no migration.** The ledger rewrites a check when its head, state, reason, reference, `unblocked` or counted job changes, and it has no field for the receipt job. So the summary of an `approval-missing` check states its counts, and the weakened sentence, once every wanted run of the head is final, as the results comment does. Before, it stated them as soon as the canonical run completed. The cost is that the counts wait for the receipt run. The alternative, a compared column (migration `00023`), is point 3 below, which the owner decided for this rule.
- **A limit.** A fresh receipt job that replaces an unavailable one later does not rewrite the check. The comment of the fresh jobs and the page carry the new count.

### 5. Not responding

Decision 4. A head *reads not responding* when no executor was seen for 30 seconds † and a wanted job of the head waits for the runner: it is `leased`, or `pending` for a reason other than a failing GitHub read. The 30 seconds are a constant, not a setting, and are read at the first pass after them: 30 to 60 seconds, since the checks job runs every 30 seconds.

- **What shows.** The Contract check (under `required` only, where the gate would hold it) says `canonical-not-responding` † instead of `canonical-pending`, and the Receipt check says `receipt-not-responding` † instead of `receipt-running`, each when its own job waits for the runner, and each with the unblock offered where a gate holds. Under `advisory` the Contract check does not change. The comment, and the page, say the same, and read the whole head: they need any one of its wanted jobs to wait.
- **How it fits `EXECUTOR_LEASE_WAIT`.** It is the same silence, read earlier. At 30 seconds the head reads not responding and the owner can unblock it. At the lease wait (2 minutes by default) the pending jobs end as `unavailable` (`no-executor`), as before, and the owner can unblock them as before. The unavailable outcome of the wait adds no second not-responding comment: the key is the same. It adds the results comment, which says that the tests did not run and why.
- **The numbers that keep a healthy runner out of it.** A false trip posts a comment that is never taken back, so a healthy runner must never be silent for 30 seconds. Today it can be: the heartbeat is every 20 seconds and the error backoff reaches 60 seconds, so a crash and its restart (20 s, plus the restart delay, plus the clean-up) can pass 30. A result post was not counted as seen either, and the heartbeats stop before the first post, so a slow store of a large result could pass 30 too. This record sets the heartbeat at 10 seconds † and stops the error backoff at 20 seconds †. The service records a lease request when it answers as well as when it arrives (a lease request lasts up to 20 seconds), and it records a result post when it arrives, when its body is read and when it is answered. A crash then leaves a silence of at most 10 seconds, plus the restart delay (5 seconds on Linux and, after task I0, on Windows; at most 10 on macOS), plus the clean-up of the containers that the old process left. A restart whose clean-up takes more than about 15 seconds (10 on macOS) does read not responding, and that is true: the runner was away for 30 seconds. Until task I0 is merged the Windows wrapper waits 61 seconds, and every crash there reads not responding. The runner and the service of one release are upgraded together by the installer. An older runner (60 seconds of backoff) next to a service of this release can trip the state after an error.

### 6. The early unblock, and its hold

Decisions 5 and 6. `/suiteward unblock <code>` is accepted for a head that reads not responding. In one transaction it ends the head's open jobs that wait for the runner (a `leased` job, or a `pending` one that does not wait on GitHub) as `unavailable` and lifts them, with the unblock rows and with the jobs that were `unavailable` already.

- **The reason is `no-executor`,** which exists, so no migration is needed: no new reason, no new column, and `SupportedVersion` stays 22. The store ends the job as the sweep ends one, with no results row and no failure counted. The old lease's late result is 409. What the command ended is derived (the job's `finished_at` equals the `decided_at` of its unblock row) and nothing more is stored.
- **A run that a live runner holds is refused,** with the new reason `runner-responding` †. The old `not-unavailable` stays for a head with nothing to lift. The check is not a read followed by a write: the caller passes the state and the lease id it observed of each job, and the store refuses, under the row lock, a job that has changed since (a runner that came back and took a `pending` job gives it a new lease id). The command is then retried and reads `runner-responding`.
- **It holds.** The recovery rule does not replace a `no-executor` job that has an unblock row. The executor that comes back runs nothing for that head, and the next push runs as usual. A `no-executor` outcome that the owner unblocked after the lease wait holds in the same way (point 2, decided by the owner). A job that ended for `executor-not-configured` is still replaced when a token is configured: fixing the configuration is what the owner wanted.
- **The warning.** When the runner is seen again, the checks job writes the runner-back comment: this head was unblocked, its tests did not run, a new push runs them.

D-APPROVAL-CONTEXT holds: the unblock lifts a gate and never an approval, never a failing test, and the TOTP code is consumed as before. A dated note in ADR 0028 records what changed there.

### 7. What the installer asks

Decision 22 of M2.1: an installer that asks on screen what it can instead of taking flags, and asks which installation when there are several. The resolver (`Value`, `Select`) already refuses with the flag's name when no terminal can be asked, so every flag stays. A question is added only where the answer is a choice the program can offer.

**The flags a person types today, and what each becomes** (`install` and `install --upgrade` share their flags):

| Command | Flag | Becomes | Why |
| --- | --- | --- | --- |
| `install --upgrade`, `uninstall`, `enroll`, `backup`, `backup init`, `restore`, `doctor` | `--settings`, `--token-file`, `--tier`, `--runtime`, `--connection` | **one question, the installation** (below), when no installation flag was given, there is a terminal and there are several installations. The flags stay | they name one instance together; today a person types up to five |
| `install` (new) | `--settings`, `--token-file` | stay | creating a second installation is rare and moves data; the refusal already names the way. Backlog: ask for a folder |
| `install` (new) | `--tier`, `--runtime` | already questions (tier page, runtime select) | |
| `install` (new) | `--connection` | stays | the default engine is found; the installation question supplies it for an installed one |
| `install` | `--answers`, `--upgrade`, `--yes`, `--plain` | stay | they select the mode or the output, not a value |
| `install` | `--image`, `--accept-docker`, `--github`, `--github-account`, `--ruleset-imported`, `--github-app-id`, `--github-installation-id`, `--github-owner`, `--github-repositories`, `--github-app-key-file`, `--test-check`, `--backup-recipient` | already questions on a terminal | the flags stay for scripts |
| `install` | `--http-port`, `--mcp-port` | already questions on the co-located tier's first install; **stay flags** on the isolated tier, which keeps the installed ports and never asks for them, and on any upgrade | a port that moves breaks a running stack |
| `install` | `--manifest-port` | stays | it is read only when given (the program picks a free port); there is nothing to ask |
| `install` | `--sha256sums`, `--job-timeout`, `--backup-dir` | stay | optional, rare, and `--sha256sums` is a file the person brings |
| `install host` | `--sha256sums` | **a question** (`Value`, "Where is the release's SHA256SUMS file?") | required there, and a person who lacks it gets a refusal and not a question. It changes the premise of `host_command_text_test.go`, which says that the host step refuses to run without the flag: with a terminal it now asks, and the refusal (`ErrUsage`, exit 2) is the case with no terminal. The hints keep naming the flag, since a script needs it, so the test's assertion holds and its comment changes |
| `install host` | `--answers`, `--yes`, `--metadata-rule`, `--http-port`, `--plain` | stay (`--yes` and `--metadata-rule` are already questions) | |
| `uninstall` | `--purge`, `--yes`, `--instance` | stay | `--purge` names the dangerous act. `--instance` is the typed confirmation, and it is taken: no new selecting flag |
| `enroll` | `--owner`, `--test-check` | already questions | |
| `enroll` | `--settings`, `--tier` | the installation question | (`enroll` has no `--token-file`) |
| `backup`, `backup init`, `restore` | the five instance flags | the installation question | |
| `backup` | `--to` | stays | defaults beside the token file |
| `backup init` | `--kit`, `--yes`, `--replace` | stay | `--replace` is destructive, `--kit` has a default |
| `restore` | `--kit`, `--instance`, `--yes` | `--kit` is already a question (the key is pasted); the others stay | |
| `restore` | the backup file, `--plaintext-dir`, `--backup-dir` | stay. **Backlog:** choose the backup from the folder | listing and reading backups is its own design |
| `doctor` | `--settings`, `--token-file` | the installation question, except with `--json` | |
| `doctor` | `--json`, `--plain` | stay | `doctor --json` never opens a screen and never refuses |

**The list of installations.**
- **Where.** `installations.yaml`, beside the default settings file (`os.UserConfigDir()/suiteward/`), mode 0600, in the configuration folder of whoever runs the installer. On the isolated tier that is the service account's, which owns the one isolated installation of a host.
- **Format.** `version: 1` and a list `installations`. Each entry has `settings` (the settings file, absolute; it is the key, compared case-insensitively on Windows as `isDefaultSettings` does), `token_file`, `tier`, `runtime`, `connection`, `instance` (the 8 characters of `/status`, empty when unknown), `http_port` and `mcp_port`.
- **Who writes it.** `install` and `install --upgrade` add or refresh the entry where they save the settings file. `uninstall --purge` removes it with the settings file. Nothing else writes it: `restore` and `backup init` work on an installation that exists, and neither changes the fields of its entry (`restore` saves no settings file, and `backup init` changes only the recovery key in the file), so a restored installation that was never listed joins at its next `--upgrade`. A plain `uninstall` keeps it, as it keeps the settings. An installation made before M2.1b joins at its next `--upgrade`. Two installers at once is a mistake they already make unsafe (ports, the one test runner of a user), so there is no lock: each write reads the file, changes one entry and replaces the file atomically, the last write wins, and a lost entry returns at that instance's next `--upgrade`.
- **Candidates.** The entries whose settings file exists, and the default settings file when it exists, always, even with no entry. A stale entry (its settings file is gone) is not a candidate. The page names how many were skipped for that. Entries leave only by a purge.
- **Which commands show the page.** `install --upgrade`, `uninstall`, `enroll`, `backup`, `backup init`, `restore` and `doctor` without `--json`, and only when no installation flag was given, there is a terminal and there is more than one candidate. With one candidate it is used, and a line says which. `install` without `--upgrade` never shows it.
- **What a row shows.** The settings file path (the name that is typed on a plain screen), and in its description the instance, the installation type in words ("on this computer" or "on a separate server"), the engine (the container program and its connection) and the two ports. The default settings file says so.
- **No terminal.** With several candidates and no flag, the command refuses and names the flags: "This computer has several SuiteWard installations (<settings files>). Say which one with `--settings <file>` and `--token-file <file>`." `doctor --json` is the exception: it checks the default installation, as it does today. A script that relied on the default while a second installation is listed must now pass the flags.
- **Write-back, and what it must not do.** The choice is held in its own map on the resolver, `chosen`, with at most the keys `settings`, `token-file`, `tier`, `runtime` and `connection`, and it is made right after the resolver is built and before each `env.Deps.instance = r.instance()`. `instance()` (which `Deps.suggest` reads, so that every hint names the chosen instance as M2.1 decision 21 requires), `preset` and the reading of `--settings` (`settingsPathFrom`) read `chosen` after the flags and the answers file. `given`, `asks` and `answerSource` never read it. So a chosen installation is not "typed": the tier page and its notice are not skipped because of it, and a refusal that names a flag never names a `--settings` that nobody typed. Writing the choice into the resolver's flags would do both, which is why it is not done. The default installation, chosen, sets nothing, so its hints carry no flag.
- **One isolated installation per host.** It is one entry, with `tier: isolated`, and choosing it needs no other field.

### 8. Plain wording

**The rule** is the owner's, 2026-10-27, as the installer already follows it: every text answers what happened, whether it is a problem, and what to do, in short sentences, in English, with the vocabulary of the installer's plain-language rules ("test runner", not "executor"; "the approved version", not "canonical"; "the commit", not "the head"). A command, a flag, a path and a check name stay exact and in code formatting. A caveat that repeats is said once. The two wording tasks, W1 and W2, change texts and nothing else: no behavior, no code that is parsed, no reason code, no status.

**What W1 rewrites (comments and acknowledgments)**, in `internal/application/commandtext` unless said:

| Text | Renderer | File |
| --- | --- | --- |
| Acknowledgments of consent: approved, revoked, no active approval, rejected, after a promotion | `consentAck`, `rejectionText` | `render.go` |
| Acknowledgments of a command that was wrong: bad or reused code, no Suite, malformed, edited first, another pull request | `commandAck`, `wrongCode`, `malformedText` | `render.go` |
| Refusals of `propose`, `skip`, `suggest`, `mode` and `unblock` | `refusal`, `refusals`, `refusalStep`, `refused` | `render.go` |
| Answers of the bootstrap commands | `bootstrapAck` | `render.go` |
| Acknowledgments of `receipt-refused`, `mode-set` and `unblocked` | `executionAck`, `gateText`, `modeNeeds`, `countsLine`, `codeUsedAgain` | `executionack.go` |
| The revision comment: its lead, the receipt line, the reduction text, the rollback text | `RevisionComment`, `receiptLine`, `approvalParts`, `reductionParts`, `rollback` | `render.go` |
| The change report in the revision comment: the numbers, the fences, the reasons without a diff, the link note | `ChangesOf` and its text | `changes.go` |
| The merge comment: promoted, corrected, blocked | `MergeComment`, `blockedReasons`, `mergeLead`, `mergeRecovery` | `merge.go` |
| The bootstrap comments: summary, promoted, notice, cancelled | `BootstrapSummary`, `BootstrapNotice`, `BootstrapCancelled` | `render.go` |
| The scope notice and its suggestion | `ScopeNoticeComment`, `ScopeSuggestionBody` | `scopediscovery.go` |
| The runner-uncovered notice and its choices | `RunnerUncoveredComment`, `RunnerChoiceBody` | `runneruncovered.go` |
| The invalid-source notice | `InvalidSourceComment` | `invalidsource.go` |
| The declaration suggestion | `SuggestDeclaration`, `editorStep`, `pasteStep` | `render.go` |
| Sentences that several comments share | `CoLocatedNotice`, `commitNote` | `render.go` |

**What W2 rewrites (check summaries and page texts):**

| Text | Renderer | File |
| --- | --- | --- |
| The Contract check: every reason, its title, text and next step, the closing sentence | `CheckSummary`, `checkReasons`, `closingText` | `commandtext/check.go` |
| The canonical counts, F11a's sentence and the unblocked sentence | `executionText` (`ExecutionText`), `blockingEditText`, `unblockedText` | `check.go` |
| The Receipt check: reasons, titles, the counts lines, the words of an unavailable reason and of a mode reason | `ReceiptCheckSummary`, `receiptReasons`, `ReceiptFacts`, `unavailableWords`, `modeReasons` | `commandtext/receiptcheck.go` |
| The page of a revision (its headings, sentences and the labels of its tables) | the report template and its builders | `internal/app/reportpage.go` and `commandtext/changes.go` |
| The pull request page | written by task R2 from the reference wording of the contract; W2 only checks it against the rule | `internal/app` (new files) |

The **new texts** of this decision (the three comments, the two new check reasons, the new refusal, the sentences of the page and the tables of reasons) are written once, in the contract, in plain language, and are the reference. W1 and W2 bring the old texts to the same words: the counts lines of the results comment become those of `ExecutionText` and `ReceiptFacts`, the words of a mode reason and of an unavailable reason become the contract's tables.

**Texts that must not change.**
- Texts that code parses or compares: the hidden marker `<!-- suiteward:<instance id>:<key> -->` (`Marker`, `HasMarker`); each command in its code span, which a person copies and `commandtext.Parse` reads: `/suiteward approve P<n>-R<m> [mode] <code>`, `revoke`, `mode`, `unblock`, `propose`, `skip`, `suggest`; the anchor of "In reply to [your command](#issuecomment-<id>)" (`replyTo`); the prefixes `execution canonical: ` and `execution receipt: ` of a reduction entry (`ExecutionEntry`); the links that the renderers check (`reportLinkPattern`, `ValidPullLink`).
- Names and codes that are stored or enumerated: the check names `SuiteWard / Contract` and `SuiteWard / Receipt`; the reason codes of the checks, of the unavailable jobs and of the gates; the dispositions; the states (`PASS` … `FAILS`); the states of a check run (`success`, `in_progress`, `failure`, `neutral`).
- The four phrases that the guard test bans: "does not run tests", "runs no tests", "did not run any tests", "did not run the tests".
- The sentences of the contract that quote a text word for word change only with the contract line, in the same commit: in [Change report (M1.13)](../contracts/runtime.md#change-report-m113-normative) the revision comment's lead sentence, its bullets, "(20 of N)", the omitted-details sentence, the link note, the sentence that the numbers are unknown, and the check summary's closing sentence; the reasons without a diff (`binary file`, `too large to compare`, `too different to compare`, `not compared (work budget ran out)`, `diff left out (report size budget)`) and the two `removed from the protected files` phrases; in [Execution](../contracts/runtime.md#execution-m21-normative) "0 tests ran", "Excluded tests are not covered.", F11a's sentence, "This check reports on the protected contract and on the run of its approved tests.", the approve line "To approve this exact revision, say what kind of change it is (its mode) and comment:", the six titles of the Receipt check, "Observed by this instance's executor. Evidence, not a verdict.", the two unblocked sentences, "Changed test files that do not parse: N.", the receipt line of the revision comment and its three level sentences, the two sentences of an empty page, and the list "The texts that change" under "What a text says about running tests".

### 9. The size of `propose`

`runtime.md` said that about 150 protected files fill one run of the default minute. The code reads eight at a time (`readConcurrency` in the inventory, used by the GitHub tree that `propose` reads), and the GitHub reader keeps the blobs it read, up to 32 MiB, so a cut-off pass resumes. About a thousand files fit one minute at 0.4 s a read, as the install guide says. The contract now says that, and drops "M1.7c or later".

## Alternatives rejected

- **Edit the results comment.** The App has no `issues` permission, and an edited comment would change what a person read.
- **One results comment for a pull request.** A comment says what a head did. The next head is another fact, and an old comment that is never edited would lie.
- **Take the hunks onto the page and raise the cap.** A hostile or large change can fill any cap, and the container has 256 MiB. A budget with an importance order is a rule that always holds.
- **A new reason `owner-unblocked` for a job that the unblock ends.** It would need a migration to the reason constraint, and the checks and the page already read `no-executor` in words.
- **A compared column for the weakened count (migration `00023`).** The check would state the canonical counts at once and rewrite once more when the receipt gives a count. It adds a column and a generation for a sentence. The wait for the receipt costs less.
- **A not-responding threshold above the worst gap of a healthy runner (for example 45 seconds).** It would change an owner decision (about 30 seconds) and still miss the 60-second error backoff. Pacing the runner keeps the owner's number.
- **A registry of installations read from container labels or a scan of the configuration folder.** Labels exist only while a container does, and a second instance's settings file can be anywhere. A file that the installer writes is the one place it can name.
- **A new flag to select the installation** (`--install`). `--instance` is the purge confirmation, and a flag more is what the question replaces.

## Consequences

- Every pull request of a repository with a profile gets one results comment for each head whose runs end, and a busy pull request gets several. With the owner's answers to points 4 and 5, an approval adds a second one only when one of the two has something to say, and an upgrade or a long outage announces every open pull request whose runs are final.
- A runner that goes quiet for 30 seconds posts a comment that stays. The pacing keeps a healthy runner out of it, and a restart whose clean-up is slow can still trip it.
- The service and the runner must be of one release (heartbeat 10 seconds, backoff 20 seconds, and the service records a result post). The installer upgrades both. The code that carries this is in the executor (`internal/executor/run/run.go`) and in the service's executor API (`executorapi_lease.go`, `executorapi_result.go`).
- The early unblock and the hold change the record of ADR 0028, which carries a note. The unblock still needs the owner's code and lifts only gates.
- No migration. A release without M2.1b still reads a job that ended for `no-executor`.
- The Contract check states the canonical counts of an `approval-missing` head once the receipt is final too.
- A script that ran an installer command on the default installation while a second one is listed must now pass the instance flags, and `doctor --json` is the exception.
- The wording tasks touch about twenty renderers and the tests that pin them. The guard test gains the three new comments.

## Decided by the owner

The points below were open when this record was written; the owner answered them on 2026-10-28, and the contracts follow the answers: 1 (a), 2 (a), 3 (a), 4 (b) with the later set written unless both sets have nothing to say, 5 (a), and 6 (b).

1. **A pull request where nothing is wanted to run.** With `canonical: off` and the receipt off or not applicable (a pull request with no protected change), no run exists, so there is nothing to report. Decision 2 says that every pull request of a repository with a profile gets a comment, and says that a repository without a profile gets none "since nothing runs there". This record writes **no comment** when no run is wanted. Options: (a) no comment, which is written here (recommended: a comment that says nothing ran is noise, and a repository that turns the canonical run off knows it); (b) a one-line comment that nothing ran on this commit because the repository's settings turn the runs off.
2. **Whether the hold also covers an unblock given after the lease wait.** Decision 6 speaks of an early unblock. This record holds every unblock of a `no-executor` job, so the owner's unblock lasts until the next push whenever it was given. Options: (a) hold both, one rule (recommended: it is the behavior the owner described, and the other case would depend on whether the owner was quick); (b) hold only an unblock given while the runner was silent, so that an unblock given after the two minutes still lets a fresh job run when the runner returns, as in M2.1. Option (b) needs a stored mark on the unblock row, a migration `00023`.
3. **When the Contract check states the counts of an `approval-missing` head.** M2.1's decision 22 chose migration `00022` so that the counts appear when the canonical run completes. The weakened sentence needs the receipt too, and the ledger cannot see the receipt job. This record states the counts once every wanted run is final, which changes that timing: the counts wait for the receipt run (usually minutes, at most the GitHub wait when a merge base cannot be read). Options: (a) wait for every wanted run, no migration (recommended: one finality rule for the comment and the check, and the owner reads the counts when the receipt is ready anyway); (b) a compared column on `pr_checks` (migration `00023`), so that the counts appear at once and the check is written once more when the receipt gives a weakened count, at the cost of a column and one more check run.
4. **A second results comment for one head, after the approval.** When the owner approves, the head gets a new canonical job under the approved version of the tests, so its job ids are new and, by the rule of keys, so is its comment. Decision 1 says one comment per head. Options: (a) write it always, so the owner sees the approved tests' own result; (b) write it only when its lead is not "Nothing needs your attention", that is, when an approved test fails or the tests did not run, and only when the head already has an earlier final set (recommended: a clean head keeps one comment, and the case that matters, an approved test that fails after the approval, still gets one). The earlier set is the head's final canonical job under the canonical's manifest with the same receipt job; the checks job also writes its comment when it is final and was not written, which happens when the approval comes in the same interval as the runs. A head whose first canonical job was cancelled while pending, because the owner approved first, has no earlier set, and its only set is always written. The contract is written as (b).
5. **The first run after an upgrade.** Without a rule, it writes a results comment on every open pull request whose runs are already final, and so does the first run after a long outage. Options: (a) accept the burst, with the comment saying nothing about when the runs ended; (b) announce only a set whose last job ended less than 24 hours ago (recommended: no stored value is needed, an old run is stale news, and an outage cannot flood the pull requests; the cost is that a run that ended 25 hours ago, in an outage that long, is never announced). **The owner chose (a):** the burst is accepted.
6. **The diffs on the pull request page.** The owner asked for one report instead of two. Options: (a) one page with the diffs, collapsed, in their own budget, with a higher cap; (b) the page with the file table and no diffs, linking the revision's report, which keeps them (section 2). **The owner chose (b).**

Deferred, with no date: asking for a folder when a second installation is created; choosing the backup of `restore` from the backups folder; a command that forgets a listed installation; the runner's silence in `/status`.
