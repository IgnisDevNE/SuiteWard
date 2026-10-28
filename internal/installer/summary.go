package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
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
// upgrade, and adds what they found: the Doctor row (the counts, why doctor did not run, that it was skipped, or that it was interrupted),
// a warning with its fix for each result that needs a look, and, when there is something to look at, how to check again. A failed check is
// not a failed install: it never fails the run. A Ctrl-C is the exception, as it is for doctor itself: one during the checks ends the
// command as interrupted, and the checks it cancelled are not counted. One that came before them only skips them ("skipped (Ctrl-C)"), as
// it skips the wait for the service. again is the command that checks again.
func (s *summaryRows) addDoctor(ctx context.Context, env Env, settingsPath, tokenFile, again string) {
	if ctx.Err() != nil {
		s.add("Doctor", "skipped (Ctrl-C)")
		s.add("Check again", again)
		return
	}
	var results []doctorResult
	spinning(screenOn(env.Term.Out), "Checking that everything works", func() { results = doctorChecks(ctx, env.Deps, env.httpGet(), settingsPath, tokenFile) })
	if ctx.Err() != nil {
		s.add("Doctor", "interrupted")
		s.add("Check again", again)
		*s = append(*s, ui.KV{Key: interruptedKey})
		return
	}
	if first := results[0]; first.Name == "settings" && !first.OK { // the first result is always the settings, and doctor stops at one it cannot read
		s.add("Doctor", "not run: "+first.Detail+" — "+first.Fix)
		s.add("Check again", again)
		return
	}
	passed, warned, failed := doctorCounts(results)
	s.add("Doctor", fmt.Sprintf("%d ok, %d warnings, %d failures", passed, warned, failed))
	dir := filepath.Dir(settingsPath)
	for _, r := range results {
		s.dropImported(r, dir)
		if r.needsALook() {
			r.Fix = importRulesetFix(r, dir)
			s.warn(r.line())
		}
	}
	if warned+failed > 0 {
		s.add("Check again", again)
	}
}

// installationTypeWords says an installation type as the screens do, in words and not as the name of the tier: "on this computer" for the co-located
// tier, "on a separate server" for the isolated one. A value this installer does not know is shown as it is, and "" stays empty.
func installationTypeWords(tier string) string {
	switch tier {
	case string(config.TierCoLocated):
		return "on this computer"
	case string(config.TierIsolated):
		return "on a separate server"
	}
	return tier
}

// dropImported deletes the ruleset file of a repository whose protection passes: the import worked, and the install no longer needs the
// file (owner decision 20). A repository that is not protected yet keeps it, and importRulesetFix names it.
func (s *summaryRows) dropImported(r doctorResult, dir string) {
	repo, isProtection := strings.CutPrefix(r.Name, "protection.")
	if !isProtection || !r.OK || r.Warning {
		return
	}
	file := rulesetPath(dir, repo)
	if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.warn(fmt.Sprintf("Could not delete the ruleset file %s (%v). You do not need it any more, so you can delete it yourself.", file, err))
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
	file := rulesetPath(dir, repo)
	if _, err := os.Stat(file); err != nil {
		return r.Fix // the install that wrote it may have been another one, or the file moved: the check's own fix is all there is
	}
	imports := "import the ruleset file " + file + " on GitHub (open the repository's Settings, then Rules, choose New ruleset, then Import a ruleset)"
	if slices.ContainsFunc(findings, func(code string) bool { return !fixed(code) }) || strings.Contains(r.Fix, setupInstructionMark) || strings.Contains(r.Fix, bypassCaveatMark) {
		return imports + ". " + r.Fix
	}
	return imports
}

func (s *summaryRows) addGitHub(gh GitHub) {
	if gh.AppID == 0 {
		s.add("GitHub App", "none (GitHub is not set up)")
		return
	}
	s.add("GitHub App", fmt.Sprintf("%d, installed on %s", gh.AppID, gh.Owner))
	s.add("Repositories", strings.Join(gh.Repositories, ", "))
}

func statusURL(httpPort int) string { return fmt.Sprintf("http://127.0.0.1:%d/status", httpPort) }

// instanceStatus is the version and the instance id (the 8 hex characters) that /status of the service reports. A service that does not
// answer, or answers something else, has neither to show: the summary leaves the rows out, and doctor is what diagnoses the instance.
func instanceStatus(ctx context.Context, status func(context.Context) ([]byte, error)) (version, id string) {
	body, err := status(ctx)
	if err != nil {
		return "", ""
	}
	var doc statusDoc
	if json.Unmarshal(body, &doc) != nil {
		return "", ""
	}
	return doc.Version, doc.Instance
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
	version, _ := instanceStatus(ctx, func(ctx context.Context) ([]byte, error) { return c.env.httpGet()(ctx, address) })
	return version != ""
}

// commonSummary is what an install and an upgrade of the co-located tier both show. The service has been started a moment ago, and
// without GitHub (and in an upgrade) nothing has waited for it since, so the version is waited for as the owner's enrollment waits for
// /status; after an enrollment that gave up there is no version to wait for, and the Owner row says so. The Instance row is the instance id
// and the Release row the version of the service, as /status reports them.
func (c *colocatedInstall) commonSummary(ctx context.Context, rows *summaryRows) {
	rows.add("Installation type", installationTypeWords(string(c.s.Tier)))
	rows.add("Container program", engineLine(c.rt.Engine()))
	if !c.statusDead {
		version, id := instanceStatus(ctx, func(ctx context.Context) ([]byte, error) {
			return waitForStatus(ctx, c.env.Sleep, c.readStatus, barOn(screenOn(c.out), "Service"))
		})
		rows.add("Instance ID", id)
		rows.add("Version", version)
	}
	rows.addGitHub(c.s.GitHub)
	rows.warn(reducedAssuranceNotice)
}

func (c *colocatedInstall) installSummary(ctx context.Context, t installTarget) []ui.KV {
	var rows summaryRows
	c.commonSummary(ctx, &rows)
	rows.add("Owner", c.owner)
	rows.add("Status", c.statusRow(ctx))
	rows.add("Access token file", c.tokenFile)
	rows.add("Test runner", c.executorRow)
	rows.add("Settings file", t.settingsPath)
	rows = append(rows, c.copyRows...)
	rows.addDoctor(ctx, c.env, t.settingsPath, c.tokenFile, c.env.Deps.suggest("doctor"))
	return rows
}

// upgradeSummary has no row for the backup: upgraded keeps the lines that name it. The image that replaced the old one has a row of its
// own, Image, as the Release row is the version of the service.
func (c *colocatedInstall) upgradeSummary(ctx context.Context, t installTarget) []ui.KV {
	var rows summaryRows
	c.commonSummary(ctx, &rows)
	rows.add("Image", fmt.Sprintf("%s (was %s)", digestOf(c.image), digestOf(c.previous.Image)))
	rows.add("Status", c.statusRow(ctx))
	rows.add("Access token file", c.tokenFile)
	rows.add("Test runner", c.executorRow)
	rows.add("Settings file", t.settingsPath)
	rows = append(rows, c.copyRows...)
	rows.addDoctor(ctx, c.env, t.settingsPath, c.tokenFile, c.env.Deps.suggest("doctor"))
	return rows
}

// isolatedSummary is the summary of the isolated tier: what an install or an upgrade of it share, then its own rows (an upgrade's is Image).
func isolatedSummary(ctx context.Context, env Env, host isolatedHost, s Settings, own ...ui.KV) []ui.KV {
	var rows summaryRows
	rows.add("Installation type", installationTypeWords(string(s.Tier)))
	rows.add("Container program", engineLine(host.rt.Engine()))
	address := statusURL(s.HTTPPort)
	version, id := instanceStatus(ctx, func(ctx context.Context) ([]byte, error) { return env.httpGet()(ctx, address) })
	rows.add("Instance ID", id)
	rows.add("Version", version)
	rows.addGitHub(s.GitHub)
	for _, kv := range own {
		rows.add(kv.Key, kv.Value)
	}
	rows.add("Status", address)
	rows.add("Settings file", host.settingsPath)
	rows.addDoctor(ctx, env, host.settingsPath, "", env.Deps.suggest("doctor"))
	return rows
}
