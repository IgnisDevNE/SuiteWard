# S3: test discovery study

- **Date:** 2026-10-25
- **Phase:** [M1.15](../plan/phases/M1.15.md)
- **Decision:** [D-SCOPE-DISCOVERY](../plan/decisions.md#d-scope-discovery-suggesting-and-widening-the-scope) (the owner's question Q4)

This is a document study with no code: the vendors' and runners' own documentation, plus a hand-picked sample of seven CI workflows. Every source was read on 2026-10-25 and is listed at the end. It was to decide what task S1 of [M1.15](../plan/phases/M1.15.md) reads besides file paths.

**Outcome (owner, 2026-10-25).** On this evidence, reading config files was taken out of M1.15: detection stays path-only, and `suiteward scope add` serves the layouts the path rules miss. Section 5 is kept as the design to start from if config reading is reconsidered.

**Result.** No tool discovers tests from a source tree without being told where, or without running code. The reporting tools ingest what a CI run produced, the runners enumerate by executing, and the rest ask the user to declare. SuiteWard's model (path conventions, then a declared scope the owner approves) is the same one. The runners' static config files are the one other source that fits, and on the sample below it adds little: a configured directory says where the runner looks, not which files are tests. A CI workflow's test commands do not fit at all.

## 1. How the tools find tests

| Tool | Input | How it finds it |
| --- | --- | --- |
| Codecov uploader | Coverage reports | Walks the CI workspace on the file system for a default list of report names (`coverage.xml`, `lcov.info`, `cover.out`, `jacoco*.xml`...), skipping `node_modules`, `vendor`, `.git` and similar, or takes `--file` globs. It never calls git for this. A separate step lists the checkout's files with `git ls-files`, so that report paths can be matched to repository paths, together with built-in CI prefix rules and the user's `fixes:` list (`before::after`). Flags are set at upload; components are declared in `codecov.yml`, never discovered. |
| Codecov Test Analytics, Trunk Flaky Tests, BuildPulse | JUnit XML only | The pipeline names the files (Codecov's docs give `*junit.xml` as the default, while `file_finder.py` in its CLI lists `*junit*.xml`, `*test*.xml` and `*TEST-*.xml`; BuildPulse takes a file, directory or glob). A test that never ran never appears. |
| Datadog Test Optimization | A tracer inside the run, or JUnit XML | Maps tests to code with a `file` attribute in the XML or a `test.source.file` tag. |
| dorny/test-reporter, mikepenz/action-junit-report | JUnit and similar | A `path` glob; mikepenz defaults to `**/junit-reports/TEST-*.xml`. |
| Launchable / CloudBees Smart Tests | The list of tests to subset | The runner generates it, or the CLI scans directories the pipeline names and matches patterns. |
| SonarQube | The test scope | The user declares `sonar.tests` (no default outside Maven, Gradle and .NET); inclusions and exclusions only narrow it. |

## 2. How the runners find tests

| Runner | Where the test location is declared | Readable without running code? |
| --- | --- | --- |
| pytest | `testpaths` (globs allowed, used only when no path is given and pytest runs from the rootdir); `python_files`, default `test_*.py` and `*_test.py`, decides which files there count. Config: `pytest.toml`/`.pytest.toml` (`[pytest]`), `pytest.ini`/`.pytest.ini`, `pyproject.toml` (`[tool.pytest]` or `[tool.pytest.ini_options]`), `tox.ini` (`[pytest]`), `setup.cfg` (`[tool:pytest]`). The first file found wins, nothing is merged. | Config yes. `--collect-only` imports the test modules: no. |
| Jest | `roots` (default `<rootDir>`; Jest says it locates "test files and source files"), `testMatch`/`testRegex`. Config: `jest.config.js\|ts\|mjs\|mts\|cjs\|cts\|json`, or the `jest` key of `package.json`, an object or a path to another file. | Only the JSON forms. |
| Vitest | `test.include`, default `**/*.{test,spec}.?(c\|m)[jt]s?(x)`, in `vitest.config.*`. | No (code). |
| Mocha | `spec` in `.mocharc.json\|jsonc\|yaml\|yml` or the `mocha` key of `package.json`. | The JSON and YAML forms; `.mocharc.js\|cjs\|mjs` are code. |
| Go | `*_test.go` by name. `./...` skips `testdata`, `vendor` and names starting with `_` or `.`. `go test -list` builds the package. | Names yes; `-list` no. |
| Cargo | `tests/` is auto-discovered (`autotests`), `[[test]] path` adds targets. Unit tests are `#[test]` functions inside the source files. | Yes, except the inline unit tests, which no path or config reveals. |
| PHPUnit | `<testsuite>` with `<directory>` and `<file>` children, in `phpunit.xml`, `phpunit.dist.xml` or `phpunit.xml.dist` (the 10.5 manual lists the three and states no precedence). | Yes. |
| Maven, Gradle | `src/test/java` and `src/test/resources`. Maven's `<testSourceDirectory>` in `pom.xml` is static XML; Gradle's `sourceSets` is build-script code. | Layout yes; Maven's override yes; Gradle's override no. |

## 3. What a CI workflow's test command tells

The sample is small and chosen by hand, not representative: `pallets/flask` (`tests.yaml`), `psf/requests` (`run-tests.yml`), `laravel/framework` (`tests.yml`), `expressjs/express` (`ci.yml`), `BurntSushi/ripgrep` (`ci.yml`), `vitest-dev/vitest` (`ci.yml`) and this repository's `ci.yml`.

- **No test command names a test directory.** Flask runs `tox`, requests `make ci`, Express `npm run test-ci`, Vitest `pnpm run test:ci:<suite>` (its browser jobs pass one test file and an `--exclude` of one file), Laravel `vendor/bin/phpunit` with flags and no path, ripgrep `cargo test --workspace`. This repository's CI calls `./scripts/check-go.ps1`, and its one direct `go test` names `./internal/installer`, a package that holds source and tests. Each command hands the choice to a config file, a script or a package manager.
- **Where the tests are named, a config names them.** Flask and requests set `testpaths = ["tests"]` in `pyproject.toml`; Laravel's `phpunit.xml.dist` lists `./tests`. Express has neither: its test directories appear only as mocha arguments in the `scripts.test` string of `package.json`.
- **A configured directory says where the runner looks, not which files are tests.** pytest's `python_files` (default `test_*.py`, `*_test.py`) decides that. In-package layouts name production packages: pandas has `testpaths = "pandas"` and astropy `["astropy", "docs"]` (both read 2026-10-25). Doctest options point the same way: requests sets `--doctest-modules`, astropy `--doctest-rst`.
- **What gated reading adds, for the seven: nothing.** The gate is in the recommendation below. The configs I read name the top-level `tests` (flask, requests, Laravel), and Express has no config, so the path rules already suggest `/tests/` and `/test/` for all four. I did not read the other three: Vitest's config is code, ripgrep's tests are mostly inline Rust, and this repository's are `_test.go` files found by name. For pandas and astropy the gate would only list the values for the owner. What reading can add is a directory whose last segment is a test-directory name but that the path rules miss, such as `pkg/tests/` holding files without a test-style name. It cannot add other names, such as `testing` (the pytest docs' own example): those stay with `scope add`.
- **Reading the commands means emulating a shell.** A `run:` line holds variables, `${{ matrix.* }}` and `if:` expressions, `working-directory`, reusable workflows and composite actions, so the same line means different things per job.
- **The workflow is already protected.** `.github/workflows/**` is a fixed default ([D-SCOPE-DEFAULTS](../plan/decisions.md#d-scope-defaults-which-files-the-protected-defaults-cover)), so a PR that retargets a test command is already a protected change.

## 4. What transfers to SuiteWard

SuiteWard reads paths and contents through Contents read, never executes code, treats every repository file as untrusted, protects only what the owner approves, and keeps to the D-SYNC budget.

| Approach | Transfers? | Why |
| --- | --- | --- |
| Reports and instrumentation (Codecov, Test Analytics, Trunk, BuildPulse, Datadog, the GitHub report actions) | No | They read output of a CI run or a tracer inside it. That output is not in the repository tree, the App has no permission for artifacts, the PR's own code produces it (evidence, not authority), and it lists the tests that ran, not those that exist. |
| Codecov's name list and ignored folders; components; `fixes`; the `git ls-files` list | Partly, by analogy | The name list mirrors the convention globs, the ignored folders the vendored list. Components and path fixes are declared by the user, as `include` and `exclude` are. The file list exists to match report paths to files; SuiteWard already reads the tree through Contents read, and has no CI checkout to list. Nothing here to add. |
| Sonar `sonar.tests`, Launchable's directory scan | Partly, by analogy | Declared scope with filters that only narrow, and a scan with pattern matching: the model SuiteWard already follows (a suggestion only adds). |
| Runner enumeration (`pytest --collect-only`, `go test -list`) | No | It imports or builds project code. |
| Go and Maven/Gradle conventions (`_test.go`, `src/test`) | Yes, already | Path only; `suggest.go` covers them. |
| Static runner config (task S1) | Yes | Declarative and parse-only, the same file the runner reads, and bounded by size and count. |
| Executable config (`jest.config.js\|ts`, `vitest.config.*`, Gradle `sourceSets`, `conftest.py`) | No | It needs execution. |
| Shell strings (`package.json` `scripts.test`, `run:` lines, Makefile targets) | No | It needs a shell parser, which is execution by another name. |
| CI workflow test commands | No | Section 3: no directory named, wrappers hide the runner, and the file is already protected. |

## 5. Recommendation for task S1

- **Keep the approved format list minus Jest, and add no CI source.** Section 3 is the evidence. Jest is out of this phase (owner, 2026-10-25): `package.json` is not read, because the any-depth globs already find Jest test files and `roots` often names `src`.
- **Gate every configured directory.** A value says where the runner looks, not which files count (section 3). Suggest a bare `dir/**` only when the existing path rules already treat the directory as a test directory, or its last segment is a test-directory name (`tests`, `test`, `spec`, `testdata`...). List any other value in the reply, with the config file it came from, for the owner to add with `suiteward scope add`. On the seven projects of the sample this suggests nothing the path rules miss, so S1 mainly gives the owner a list of configured directories to decide on.
- **Within the approved runners, beyond the literal list (the orchestrator decides):**
  - `pytest.toml` and `.pytest.toml` (`[pytest]`), and pyproject's `[tool.pytest]` table next to `[tool.pytest.ini_options]`. The suggester already lists both files as runner files; reading them needs a TOML library.
  - Exact section names: `setup.cfg` uses `[tool:pytest]`, `tox.ini` uses `[pytest]`.
  - `phpunit.dist.xml` (listed in the 10.5 manual), and `<file>` children of `<testsuite>` as well as `<directory>`.
- **Declined for now (owner, 2026-10-25):** `.mocharc.json|jsonc|yaml|yml` `spec`, and `pom.xml` `<testSourceDirectory>`.
- **The bounds must cover:**
  - Normalize first (leading `./`, trailing `/`, doubled `/`), then resolve every value against the directory of the config file that holds it, not the repository root (nested `Cargo.toml`, monorepo packages).
  - Reject absolute paths, `..`, backslashes, control characters, and root-like values: `.`, empty, and any value whose first segment is a pure wildcard (`*`, `**`). A directory that also holds production code, such as `src`, cannot be told apart statically, which is why the gate above exists.
  - Accept every value form: a TOML array, a string (pandas writes `testpaths = "pandas"`), and in the INI form a whitespace-separated list (spaces and newlines). Treat a string like the INI form. `**` is allowed in `testpaths`.
  - Take the union across config files and do not copy pytest's first-file-wins rule. The union only over-suggests, and a PR that adds a higher-priority file is already `runner-uncovered`.
  - Skip vendored directories, submodules and symbolic links when picking files to read; cap the bytes per file, the files per tree and the entries per file; count the reads in the D-SYNC budget; treat a file that does not parse as absent, with a warning.
  - Read XML without entity expansion, and test with an entity bomb.
- **State the limit:** inline Rust unit tests are visible to neither paths nor config. Python doctests live in production modules or docs (requests, astropy), so they are usually not visible, or not without suggesting production code. The owner declares them (`scope add`, task S2).

## Sources

Read on 2026-10-25.

- **Codecov:** <https://docs.codecov.com/docs/file-search>, <https://docs.codecov.com/docs/fixing-paths>, <https://docs.codecov.com/docs/components>, <https://docs.codecov.com/docs/test-analytics>, <https://docs.codecov.com/docs/codecov-cli>, <https://raw.githubusercontent.com/codecov/codecov-cli/main/codecov_cli/services/upload/file_finder.py>, <https://raw.githubusercontent.com/codecov/codecov-cli/main/codecov_cli/services/upload/network_finder.py>, <https://raw.githubusercontent.com/codecov/codecov-cli/main/codecov_cli/helpers/versioning_systems.py>.
- **Test-result tools:** <https://docs.trunk.io/flaky-tests/get-started/index.md>, <https://github.com/buildpulse/buildpulse-action>, <https://docs.datadoghq.com/tests/setup/junit_xml/>, <https://github.com/dorny/test-reporter>, <https://github.com/mikepenz/action-junit-report>.
- **Launchable / CloudBees:** <https://docs.cloudbees.com/docs/cloudbees-smart-tests/latest/concepts/subset>.
- **SonarQube:** <https://docs.sonarsource.com/sonarqube-server/2025.1/project-administration/analysis-scope>.
- **Runners:** <https://docs.pytest.org/en/stable/reference/customize.html>, <https://docs.pytest.org/en/stable/reference/reference.html>, <https://jestjs.io/docs/configuration>, <https://vitest.dev/config/include>, <https://mochajs.org/running/configuring/>, <https://pkg.go.dev/cmd/go>, <https://doc.rust-lang.org/cargo/reference/cargo-targets.html>, <https://docs.phpunit.de/en/12.0/configuration.html>, <https://docs.phpunit.de/en/10.5/textui.html>, <https://maven.apache.org/guides/introduction/introduction-to-the-standard-directory-layout.html>, <https://maven.apache.org/pom.html>, <https://docs.gradle.org/current/userguide/java_plugin.html>.
- **CI workflows:** <https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax>, and this repository's [`ci.yml`](../../.github/workflows/ci.yml).
- **The sample, workflows:** <https://raw.githubusercontent.com/pallets/flask/main/.github/workflows/tests.yaml>, <https://raw.githubusercontent.com/psf/requests/main/.github/workflows/run-tests.yml>, <https://raw.githubusercontent.com/laravel/framework/master/.github/workflows/tests.yml>, <https://raw.githubusercontent.com/expressjs/express/master/.github/workflows/ci.yml>, <https://raw.githubusercontent.com/BurntSushi/ripgrep/master/.github/workflows/ci.yml>, <https://raw.githubusercontent.com/vitest-dev/vitest/main/.github/workflows/ci.yml>.
- **The sample, configs:** <https://raw.githubusercontent.com/pallets/flask/main/pyproject.toml>, <https://raw.githubusercontent.com/psf/requests/main/pyproject.toml>, <https://raw.githubusercontent.com/laravel/framework/master/phpunit.xml.dist>, <https://raw.githubusercontent.com/expressjs/express/master/package.json>, and for the in-package layouts <https://raw.githubusercontent.com/pandas-dev/pandas/main/pyproject.toml> and <https://raw.githubusercontent.com/astropy/astropy/main/pyproject.toml>.
