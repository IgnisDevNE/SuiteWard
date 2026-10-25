# S3: test discovery study

- **Date:** 2026-10-25
- **Phase:** [M1.15](../plan/phases/M1.15.md)
- **Decision:** [D-SCOPE-DISCOVERY](../plan/decisions.md#d-scope-discovery-suggesting-and-widening-the-scope) (the owner's question Q4)

This is a document study with no code: the vendors' and runners' own documentation, plus a hand-picked sample of seven CI workflows. Every source was read on 2026-10-25 and is listed at the end. It decides what task S1 of [M1.15](../plan/phases/M1.15.md) reads besides file paths.

**Result.** No tool discovers tests from a source tree. The reporting tools ingest what a CI run produced, the runners enumerate by executing, and the rest ask the user to declare. SuiteWard's model (path conventions, then a declared scope the owner approves) is the same one. The runners' static config files are the one source that adds to it. A CI workflow's test commands do not.

## 1. How the tools find tests

| Tool | Input | How it finds it |
| --- | --- | --- |
| Codecov uploader | Coverage reports | Walks the CI workspace on the file system for a default list of report names (`coverage.xml`, `lcov.info`, `cover.out`, `jacoco*.xml`...), skipping `node_modules`, `vendor`, `.git` and similar, or takes `--file` globs. It never calls git for this. Report paths are matched to repository paths with built-in CI prefix rules and the user's `fixes:` list (`before::after`). Flags are set at upload; components are declared in `codecov.yml`, never discovered. |
| Codecov Test Analytics, Trunk Flaky Tests, BuildPulse | JUnit XML only | The pipeline names the files (Codecov defaults to `*junit.xml`; BuildPulse takes a file, directory or glob). A test that never ran never appears. |
| Datadog Test Optimization | A tracer inside the run, or JUnit XML | Maps tests to code with a `file` attribute in the XML or a `test.source.file` tag. |
| dorny/test-reporter, mikepenz/action-junit-report | JUnit and similar | A `path` glob; mikepenz defaults to `**/junit-reports/TEST-*.xml`. |
| Launchable / CloudBees Smart Tests | The list of tests to subset | The runner generates it, or the CLI scans directories the pipeline names and matches patterns. |
| SonarQube | The test scope | The user declares `sonar.tests` (no default outside Maven, Gradle and .NET); inclusions and exclusions only narrow it. |

## 2. How the runners find tests

| Runner | Where the test location is declared | Readable without running code? |
| --- | --- | --- |
| pytest | `testpaths` (globs allowed, used only when no path is given and pytest runs from the rootdir); `python_files` defaults to `test_*.py`, `*_test.py`. Config: `pytest.toml`/`.pytest.toml` (`[pytest]`), `pytest.ini`, `pyproject.toml` (`[tool.pytest]` or `[tool.pytest.ini_options]`), `tox.ini` (`[pytest]`), `setup.cfg` (`[tool:pytest]`). The first file found wins, nothing is merged. | Config yes. `--collect-only` imports the test modules: no. |
| Jest | `roots` (default `<rootDir>`; Jest says it locates "test files and source files"), `testMatch`/`testRegex`. Config: `jest.config.js\|ts\|mjs\|mts\|cjs\|cts\|json`, or the `jest` key of `package.json`, an object or a path to another file. | Only the JSON forms. |
| Vitest | `test.include`, default `**/*.{test,spec}.?(c\|m)[jt]s?(x)`, in `vitest.config.*`. | No (code). |
| Mocha | `spec` in `.mocharc.json\|jsonc\|yaml\|yml` or the `mocha` key of `package.json`. | The JSON and YAML forms; `.mocharc.js\|cjs\|mjs` are code. |
| Go | `*_test.go` by name. `./...` skips `testdata`, `vendor` and names starting with `_` or `.`. `go test -list` builds the package. | Names yes; `-list` no. |
| Cargo | `tests/` is auto-discovered (`autotests`), `[[test]] path` adds targets. Unit tests are `#[test]` functions inside the source files. | Yes, except the inline unit tests, which no path or config reveals. |
| PHPUnit | `<testsuite>` with `<directory>` and `<file>` children, in `phpunit.xml`, `phpunit.dist.xml` or `phpunit.xml.dist` (the 10.5 manual lists the three and states no precedence). | Yes. |
| Maven, Gradle | `src/test/java` and `src/test/resources`. Maven's `<testSourceDirectory>` in `pom.xml` is static XML; Gradle's `sourceSets` is build-script code. | Layout yes; Maven's override yes; Gradle's override no. |

## 3. What a CI workflow's test command tells

The sample is small and chosen by hand, not representative: `pallets/flask` (`tests.yaml`), `psf/requests` (`run-tests.yml`), `laravel/framework` (`tests.yml`), `expressjs/express` (`ci.yml`), `BurntSushi/ripgrep` (`ci.yml`), `vitest-dev/vitest` (`ci.yml`) and this repository's `ci.yml`.

- **No test command names a test directory.** Flask runs `tox`, requests `make ci`, Express `npm run test-ci`, Vitest `pnpm run test:ci:<suite>`, Laravel a bare `vendor/bin/phpunit`, ripgrep `cargo test --workspace`. This repository's CI calls `./scripts/check-go.ps1`, and its one direct `go test` names `./internal/installer`, a package that holds source and tests. Each command hands the choice to a config file, a script or a package manager.
- **Where the tests are named, a config names them.** Flask and requests set `testpaths = ["tests"]` in `pyproject.toml`; Laravel's `phpunit.xml.dist` lists `./tests`. Express has neither: its test directories appear only as mocha arguments in the `scripts.test` string of `package.json`.
- **Config adds little over path detection for these seven.** Every test directory named above is `tests/` or `test/` at the top level, or below it, which the path rules already find. Config helps for unconventional names; this sample does not measure how common they are.
- **Reading the commands means emulating a shell.** A `run:` line holds variables, `${{ matrix.* }}` and `if:` expressions, `working-directory`, reusable workflows and composite actions, so the same line means different things per job.
- **The workflow is already protected.** `.github/workflows/**` is a fixed default ([D-SCOPE-DEFAULTS](../plan/decisions.md#d-scope-defaults-which-files-the-protected-defaults-cover)), so a PR that retargets a test command is already a protected change.

## 4. What transfers to SuiteWard

SuiteWard reads paths and contents through Contents read, never executes code, treats every repository file as untrusted, protects only what the owner approves, and keeps to the D-SYNC budget.

| Approach | Transfers? | Why |
| --- | --- | --- |
| Reports and instrumentation (Codecov, Test Analytics, Trunk, BuildPulse, Datadog, the GitHub report actions) | No | They read output of a CI run or a tracer inside it. That output is not in the repository tree, the App has no permission for artifacts, the PR's own code produces it (evidence, not authority), and it lists the tests that ran, not those that exist. |
| Codecov's name list and ignored folders; components; `fixes` | Partly, by analogy | The name list mirrors the convention globs, the ignored folders the vendored list. Components and path fixes are declared by the user, as `include` and `exclude` are. Nothing here to add. |
| Sonar `sonar.tests`, Launchable's directory scan | Partly, by analogy | Declared scope with filters that only narrow, and a scan with pattern matching: the model SuiteWard already follows (a suggestion only adds). |
| Runner enumeration (`pytest --collect-only`, `go test -list`, `jest --listTests`) | No | It imports or builds project code. |
| Go and Maven/Gradle conventions (`_test.go`, `src/test`) | Yes, already | Path only; `suggest.go` covers them. |
| Static runner config (task S1) | Yes | Declarative and parse-only, the same file the runner reads, and bounded by size and count. |
| Executable config (`jest.config.js\|ts`, `vitest.config.*`, Gradle `sourceSets`, `conftest.py`) | No | It needs execution. |
| Shell strings (`package.json` `scripts.test`, `run:` lines, Makefile targets) | No | It needs a shell parser, which is execution by another name. |
| CI workflow test commands | No | Section 3: no directory named, wrappers hide the runner, and the file is already protected. |

## 5. Recommendation for task S1

- **Keep the approved format list and add no CI source.** Section 3 is the evidence.
- **Within the approved runners, beyond the literal list (the orchestrator decides):**
  - `pytest.toml` and `.pytest.toml` (`[pytest]`), and pyproject's `[tool.pytest]` table next to `[tool.pytest.ini_options]`. The suggester already lists both files as runner files, and the TOML library is approved.
  - Exact section names: `setup.cfg` uses `[tool:pytest]`, `tox.ini` uses `[pytest]`.
  - `phpunit.dist.xml` (listed in the 10.5 manual), and `<file>` children of `<testsuite>` as well as `<directory>`.
  - `jest.config.json`, and a `jest` key in `package.json` that is a path to another file: follow it or ignore it, but never fail on it.
  - Jest `roots` does not mean "tests are here". `{"jest": {"roots": ["<rootDir>/src"]}}` is a common layout, and read literally it would suggest `src/**` and put every production change behind an approval. Either suggest a root only when the existing path rules already treat it as a test directory, or drop `roots` from task S1, because the any-depth convention globs already find Jest test files. `testpaths` does mean "tests are here", so the root-like rejection below is enough for it.
- **Options that need the owner's yes, none chosen here:** `.mocharc.json|jsonc|yaml|yml` `spec`, and `pom.xml` `<testSourceDirectory>`.
- **The bounds must cover:**
  - Resolve every value against the directory of the config file that holds it, not the repository root (nested `Cargo.toml`, monorepo packages). Strip `<rootDir>`.
  - Reject absolute paths, `..`, backslashes, control characters and root-like values (`.`, `<rootDir>`, empty). A directory that also holds production code, such as `src`, cannot be told apart statically (`roots` and `testpaths` may name one), so the reply should say which config file a pattern came from.
  - Accept every value form: an array, a space-separated string, `**` in `testpaths`.
  - Take the union across config files and do not copy pytest's first-file-wins rule. The union only over-suggests, and a PR that adds a higher-priority file is already `runner-uncovered`.
  - Skip vendored directories, submodules and symbolic links when picking files to read; cap the bytes per file, the files per tree and the entries per file; count the reads in the D-SYNC budget; treat a file that does not parse as absent, with a warning.
  - Read XML without entity expansion, and test with an entity bomb.
- **State the limit:** inline Rust unit tests and Python doctests are visible to neither paths nor config, so the owner declares them (`scope add`, task S2).

## Sources

Read on 2026-10-25.

- **Codecov:** <https://docs.codecov.com/docs/file-search>, <https://docs.codecov.com/docs/fixing-paths>, <https://docs.codecov.com/docs/components>, <https://docs.codecov.com/docs/test-analytics>, <https://docs.codecov.com/docs/codecov-cli>, <https://github.com/codecov/codecov-cli/blob/main/codecov_cli/services/upload/file_finder.py>.
- **Test-result tools:** <https://docs.trunk.io/flaky-tests/get-started/index.md>, <https://github.com/buildpulse/buildpulse-action>, <https://docs.datadoghq.com/tests/setup/junit_xml/>, <https://github.com/dorny/test-reporter>, <https://github.com/mikepenz/action-junit-report>.
- **Launchable / CloudBees:** <https://docs.cloudbees.com/docs/cloudbees-smart-tests/latest/concepts/subset>.
- **SonarQube:** <https://docs.sonarsource.com/sonarqube-server/2025.1/project-administration/analysis-scope>.
- **Runners:** <https://docs.pytest.org/en/stable/reference/customize.html>, <https://docs.pytest.org/en/stable/reference/reference.html>, <https://jestjs.io/docs/configuration>, <https://vitest.dev/config/include>, <https://mochajs.org/running/configuring/>, <https://pkg.go.dev/cmd/go>, <https://doc.rust-lang.org/cargo/reference/cargo-targets.html>, <https://docs.phpunit.de/en/12.0/configuration.html>, <https://docs.phpunit.de/en/10.5/textui.html>, <https://maven.apache.org/guides/introduction/introduction-to-the-standard-directory-layout.html>, <https://maven.apache.org/pom.html>, <https://docs.gradle.org/current/userguide/java_plugin.html>.
- **CI workflows:** <https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax>, and this repository's [`ci.yml`](../../.github/workflows/ci.yml).
- **The sample, workflows:** <https://raw.githubusercontent.com/pallets/flask/main/.github/workflows/tests.yaml>, <https://raw.githubusercontent.com/psf/requests/main/.github/workflows/run-tests.yml>, <https://raw.githubusercontent.com/laravel/framework/master/.github/workflows/tests.yml>, <https://raw.githubusercontent.com/expressjs/express/master/.github/workflows/ci.yml>, <https://raw.githubusercontent.com/BurntSushi/ripgrep/master/.github/workflows/ci.yml>, <https://raw.githubusercontent.com/vitest-dev/vitest/main/.github/workflows/ci.yml>.
- **The sample, configs:** <https://raw.githubusercontent.com/pallets/flask/main/pyproject.toml>, <https://raw.githubusercontent.com/psf/requests/main/pyproject.toml>, <https://raw.githubusercontent.com/laravel/framework/master/phpunit.xml.dist>, <https://raw.githubusercontent.com/expressjs/express/master/package.json>.
