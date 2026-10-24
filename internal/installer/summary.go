package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The summary screen of a finished install or upgrade, shown on a full screen only: what the instance is, where to reach it and what doctor
// found. Close prints its rows again on the normal screen, so what the user needs afterwards (the token file, the status address, the
// backup, what needs a look) is a row or a warning, and nothing is kept a second time.

// summaryRows builds the rows of a summary; a row without a value is left out.
type summaryRows []ui.KV

func (s *summaryRows) add(key, value string) {
	if value != "" {
		*s = append(*s, ui.KV{Key: key, Value: value})
	}
}

// warn adds a warning to the summary.
// shortcut: a warning is a row without a key, so that every builder still returns rows only; showSummary hands those to the screen as
// warnings. Upgrade when a builder needs more than the text of a warning.
func (s *summaryRows) warn(text string) { *s = append(*s, ui.KV{Value: text}) }

// interruptedKey is the key of the row that says a Ctrl-C ended the checks the summary was built with: showSummary shows what there is and
// ends the command as interrupted.
// shortcut: a second signal in the shape of a row, so that the builders, and the callers outside this change, keep returning rows only.
// Upgrade with the first builder that has to return more than rows.
const interruptedKey = "\x00interrupted"

// addDoctor runs the checks of `suiteward doctor` against the instance the settings file describes, as the last step of an install or an
// upgrade, and adds what they found: the Doctor row (the counts, why doctor did not run, or that it was interrupted), a warning with its
// fix for each result that needs a look, and, when there is something to look at, how to check again. A failed check is not a failed
// install: it never fails the run. A Ctrl-C is the exception, as it is for doctor itself: one during the checks ends the command as
// interrupted, and the checks it cancelled are not counted. One that came before them only skips them, as it skips the wait for the service.
// again is the command that checks again.
// shortcut: ui.Summary shows its last rows when the page is taller than the window, and cannot trim warnings as Page does, so many warnings
// push the rows above them off a small window; Close keeps them all. Upgrade when ui.Summary trims (the polish task).
func (s *summaryRows) addDoctor(ctx context.Context, env Env, settingsPath, again string) {
	before := ctx.Err() != nil
	var results []doctorResult
	if !before {
		spinning(screenOn(env.Term.Out), "Checking the instance", func() { results = doctorChecks(ctx, env.Deps, env.httpGet(), settingsPath) })
	}
	if before || ctx.Err() != nil {
		s.add("Doctor", "interrupted")
		s.add("Check again", again)
		if !before {
			*s = append(*s, ui.KV{Key: interruptedKey})
		}
		return
	}
	if first := results[0]; first.Name == "settings" && !first.OK { // the first result is always the settings, and doctor stops at one it cannot read
		s.add("Doctor", "not run: "+first.Detail+" — "+first.Fix)
		s.add("Check again", again)
		return
	}
	passed, warned, failed := doctorCounts(results)
	s.add("Doctor", fmt.Sprintf("%d ok, %d warnings, %d failures", passed, warned, failed))
	for _, r := range results {
		if r.needsALook() {
			r.Fix = importRulesetFix(r, filepath.Dir(settingsPath))
			s.warn(r.line())
		}
	}
	if warned+failed > 0 {
		s.add("Check again", again)
	}
}

// What the ruleset file fixes, as the findings of a protection result name them: it requires the contract check and the test check, and that
// branches are up to date.
var rulesetFindings = []string{string(protection.MissingContractCheck), string(protection.MissingTestCheck), string(protection.NotUpToDate)}

// What the evaluator says that an import does not fix: the setup instruction (no test check is configured yet) and the caveat of a ruleset
// whose bypass list cannot be seen. A test passes the sentences of the real evaluator through importRulesetFix, so a rewording fails there.
const (
	setupInstructionMark = "suiteward enroll"
	bypassCaveatMark     = "bypass list"
)

// importRulesetFix is the fix of a result that needs a look. A repository whose protection lacks what the ruleset requires is told to
// import the ruleset file that the install wrote beside the settings, when the file is there. The import replaces the instruction of the
// check, which would take two more lines of a page that is short of them, only when it is the whole fix: every finding is one the ruleset
// fixes and the evaluator has no other instruction. Otherwise the import comes first and the evaluator's instructions stay.
func importRulesetFix(r doctorResult, dir string) string {
	repo, isProtection := strings.CutPrefix(r.Name, "protection.")
	codes, isFindings := strings.CutPrefix(r.Detail, "findings: ")
	if !isProtection || r.OK || !isFindings {
		return r.Fix
	}
	findings := strings.Split(codes, ", ")
	fixed := func(code string) bool { return slices.Contains(rulesetFindings, code) }
	if !slices.ContainsFunc(findings, fixed) {
		return r.Fix
	}
	file := rulesetPathOf(dir, repo)
	if _, err := os.Stat(file); err != nil {
		return r.Fix // the install that wrote it may have been another one, or the file moved: the check's own fix is all there is
	}
	imports := "import the ruleset file " + file + " (in the repository's Settings, Rules, choose New ruleset, then Import a ruleset)"
	if slices.ContainsFunc(findings, func(code string) bool { return !fixed(code) }) || strings.Contains(r.Fix, setupInstructionMark) || strings.Contains(r.Fix, bypassCaveatMark) {
		return imports + ". " + r.Fix
	}
	return imports
}

// rulesetPathOf is where the install writes the ruleset of repo (owner/name), as writeRuleset names it.
// shortcut: the name is spelled here and in writeRuleset, which another change owns; a test pins the two together. Upgrade when ruleset.go
// exports its naming.
func rulesetPathOf(dir, repo string) string {
	owner, name, _ := strings.Cut(repo, "/")
	return filepath.Join(dir, "suiteward-ruleset-"+owner+"-"+name+".json")
}

func (s *summaryRows) addGitHub(gh GitHub) {
	if gh.AppID == 0 {
		s.add("GitHub App", "none in the settings")
		return
	}
	s.add("GitHub App", fmt.Sprintf("%d on %s", gh.AppID, gh.Owner))
	s.add("Repositories", strings.Join(gh.Repositories, ", "))
}

func statusURL(httpPort int) string { return fmt.Sprintf("http://127.0.0.1:%d/status", httpPort) }

// instanceVersion is the version /status of the service reports. A service that does not answer, or answers something else, has no
// version to show: the summary leaves the row out, and doctor is what diagnoses the instance.
func instanceVersion(ctx context.Context, status func(context.Context) ([]byte, error)) string {
	body, err := status(ctx)
	if err != nil {
		return ""
	}
	var parsed struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return ""
	}
	return parsed.Version
}

// statusRow is where to read /status of the co-located stack: its address, or, when the port is published inside a Podman machine and does
// not answer from this PC, the command that reads it there (statusHint).
func (c *colocatedInstall) statusRow(ctx context.Context) string {
	if c.inMachine() {
		return c.statusHint(ctx)
	}
	return statusURL(c.s.HTTPPort)
}

// answersHere says that /status of the service answers from this PC, with the version of a SuiteWard service.
func (c *colocatedInstall) answersHere(ctx context.Context) bool {
	address := statusURL(c.s.HTTPPort)
	return instanceVersion(ctx, func(ctx context.Context) ([]byte, error) { return c.env.httpGet()(ctx, address) }) != ""
}

// commonSummary is what an install and an upgrade of the co-located tier both show. The service has been started a moment ago, and
// without GitHub (and in an upgrade) nothing has waited for it since, so the version is waited for as the owner's enrollment waits for
// /status; after an enrollment that gave up there is no version to wait for, and the Owner row says so.
func (c *colocatedInstall) commonSummary(ctx context.Context, rows *summaryRows) {
	rows.add("Tier", string(c.s.Tier))
	rows.add("Runtime", engineLine(c.rt.Engine()))
	if !c.statusDead {
		rows.add("Instance", instanceVersion(ctx, func(ctx context.Context) ([]byte, error) {
			return waitForStatus(ctx, c.env.Sleep, c.readStatus, barOn(screenOn(c.out), "Service"))
		}))
	}
	rows.addGitHub(c.s.GitHub)
}

func (c *colocatedInstall) installSummary(ctx context.Context, t installTarget) []ui.KV {
	var rows, self summaryRows
	env := c.env
	env.Deps = offerSelf(ctx, c.env, &self) // before the hints of the rows: after a copy on the PATH they name the program suiteward
	c.commonSummary(ctx, &rows)
	rows.add("Owner", c.owner)
	rows.add("Status", c.statusRow(ctx))
	rows.add("Token file", c.tokenFile)
	rows.add("Settings", t.settingsPath)
	rows = append(rows, self...)
	rows.addDoctor(ctx, env, t.settingsPath, doctorHint(env.Deps.command(), t.settingsPath, t.defaultPath))
	return rows
}

// upgradeSummary has no row for the backup: upgraded keeps the lines that name it.
func (c *colocatedInstall) upgradeSummary(ctx context.Context, t installTarget) []ui.KV {
	var rows, self summaryRows
	env := c.env
	env.Deps = offerSelf(ctx, c.env, &self)
	c.commonSummary(ctx, &rows)
	rows.add("Release", fmt.Sprintf("%s (was %s)", digestOf(c.image), digestOf(c.previous.Image)))
	rows.add("Status", c.statusRow(ctx))
	rows.add("Token file", c.tokenFile)
	rows.add("Settings", t.settingsPath)
	rows = append(rows, self...)
	rows.addDoctor(ctx, env, t.settingsPath, doctorHint(env.Deps.command(), t.settingsPath, t.defaultPath))
	return rows
}

// isolatedSummary is the summary of the isolated tier: what an install or an upgrade of it share, then its own rows.
func isolatedSummary(ctx context.Context, env Env, host isolatedHost, s Settings, own ...ui.KV) []ui.KV {
	var rows summaryRows
	rows.add("Tier", string(s.Tier))
	rows.add("Runtime", engineLine(host.rt.Engine()))
	address := statusURL(s.HTTPPort)
	rows.add("Instance", instanceVersion(ctx, func(ctx context.Context) ([]byte, error) { return env.httpGet()(ctx, address) }))
	rows.addGitHub(s.GitHub)
	for _, kv := range own {
		rows.add(kv.Key, kv.Value)
	}
	rows.add("Status", address)
	rows.add("Settings", host.settingsPath)
	rows.addDoctor(ctx, env, host.settingsPath, env.Deps.command()+" doctor")
	return rows
}
