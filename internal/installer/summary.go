package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The summary screen of a finished install or upgrade, shown on a full screen only: what the instance is, where to reach it and what to
// do next. Close prints its rows again on the normal screen, so what the user needs afterwards (the token file, the status address, the
// backup) is a row, and nothing is kept a second time.

// summaryRows builds the rows of a summary; a row without a value is left out.
type summaryRows []ui.KV

func (s *summaryRows) add(key, value string) {
	if value != "" {
		*s = append(*s, ui.KV{Key: key, Value: value})
	}
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

// statusRow is where to read /status of the co-located stack: its address, or inside the Podman machine, which publishes the port,
// the command that reads it there.
func (c *colocatedInstall) statusRow(ctx context.Context) string {
	if c.inMachine() {
		return c.statusHint(ctx)
	}
	return statusURL(c.s.HTTPPort)
}

// commonSummary is what an install and an upgrade of the co-located tier both show.
func (c *colocatedInstall) commonSummary(ctx context.Context, rows *summaryRows) {
	rows.add("Tier", string(c.s.Tier))
	rows.add("Runtime", engineLine(c.rt.Engine()))
	rows.add("Instance", instanceVersion(ctx, c.readStatus))
	rows.addGitHub(c.s.GitHub)
}

func (c *colocatedInstall) installSummary(ctx context.Context, t installTarget) []ui.KV {
	var rows summaryRows
	c.commonSummary(ctx, &rows)
	rows.add("Owner", c.owner)
	rows.add("Status", c.statusRow(ctx))
	rows.add("Token file", c.tokenFile)
	rows.add("Settings", t.settingsPath)
	rows.add("Next", doctorHint(t.settingsPath, t.defaultPath))
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
	rows.add("Next", doctorHint(t.settingsPath, t.defaultPath))
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
	rows.add("Next", "suiteward doctor")
	return rows
}
