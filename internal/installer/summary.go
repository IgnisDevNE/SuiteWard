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

// addDoctor runs the checks of `suiteward doctor` against the instance the settings file describes, as the last step of an install or an
// upgrade, and adds what they found: the Doctor row (the counts, or why doctor did not run), a warning with its fix for each result that
// needs a look, and, when there is one, how to check again. It never fails the run: doctor reports, and a failed check is not a failed
// install. again is the command that checks again.
func (s *summaryRows) addDoctor(ctx context.Context, env Env, settingsPath, again string) {
	var results []doctorResult
	spinning(screenOn(env.Term.Out), "Checking the instance", func() { results = doctorChecks(ctx, env.Deps, env.httpGet(), settingsPath) })
	if i := slices.IndexFunc(results, func(r doctorResult) bool { return r.Name == "settings" && !r.OK }); i >= 0 {
		s.add("Doctor", "not run: "+results[i].Detail+" — "+results[i].Fix) // it stops at the settings, which it needs to know the instance
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

// importRulesetFix is the fix of a result that needs a look. A repository whose protection lacks the checks that the ruleset requires is also
// told to import the ruleset file that the install wrote beside the settings, when the file is there.
func importRulesetFix(r doctorResult, dir string) string {
	repo, isProtection := strings.CutPrefix(r.Name, "protection.")
	lacksChecks := strings.Contains(r.Detail, string(protection.MissingContractCheck)) || strings.Contains(r.Detail, string(protection.MissingTestCheck))
	if !isProtection || r.OK || !lacksChecks {
		return r.Fix
	}
	file := rulesetPathOf(dir, repo)
	if _, err := os.Stat(file); err != nil {
		return r.Fix // the install that wrote it may have been another one, or the file moved: the check's own fix is all there is
	}
	return r.Fix + " The install wrote the ruleset that requires both: import the ruleset file " + file + " (in the repository's Settings, Rules, choose New ruleset, then Import a ruleset)."
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
	var rows summaryRows
	c.commonSummary(ctx, &rows)
	rows.add("Owner", c.owner)
	rows.add("Status", c.statusRow(ctx))
	rows.add("Token file", c.tokenFile)
	rows.add("Settings", t.settingsPath)
	rows.addDoctor(ctx, c.env, t.settingsPath, doctorHint(t.settingsPath, t.defaultPath))
	return rows
}

// upgradeSummary has no row for the backup: upgraded keeps the lines that name it.
func (c *colocatedInstall) upgradeSummary(ctx context.Context, t installTarget) []ui.KV {
	var rows summaryRows
	c.commonSummary(ctx, &rows)
	rows.add("Release", fmt.Sprintf("%s (was %s)", digestOf(c.image), digestOf(c.previous.Image)))
	rows.add("Status", c.statusRow(ctx))
	rows.add("Token file", c.tokenFile)
	rows.add("Settings", t.settingsPath)
	rows.addDoctor(ctx, c.env, t.settingsPath, doctorHint(t.settingsPath, t.defaultPath))
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
	rows.addDoctor(ctx, env, host.settingsPath, "suiteward doctor")
	return rows
}
