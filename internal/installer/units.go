package installer

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/deploy"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// DefaultEnvFile is where the isolated service account keeps the non-secret GitHub values (D-INSTALLER).
const DefaultEnvFile = "%h/.config/suiteward/github.env"

const releaseImageRepo = "ghcr.io/ignisdevne/suiteward"

var (
	imageDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	// A path of the EnvironmentFile= line: one line, and without the leading dash that would make systemd ignore a missing file.
	envFilePath = regexp.MustCompile(`^[^-\x00-\x1f][^\x00-\x1f]*$`)
)

// UnitParams are the per-install values of the rendered quadlet units.
type UnitParams struct {
	Digest     string // the signed index digest of the release image, sha256:<64 hex>
	Tier       config.Tier
	HTTPPort   int
	MCPPort    int
	JobTimeout string // optional, a Go duration; serve only
	EnvFile    string // EnvironmentFile of the service, DefaultEnvFile when empty
	// NoGitHub leaves the App key secret and its environment variable out of the service unit: without an App there is no key to
	// mount, and the service stops at startup when it is given the key but not the App. The zero value keeps both lines.
	NoGitHub bool
	// NoReportURL leaves the report base URL out of the service unit. Only the adoption sets it, for units of v0.3.0-alpha.2 that
	// predate the line, so that they compare equal; an install or an upgrade renders the line (UnitParamsFrom never sets it).
	NoReportURL bool
	// NoExecutor leaves the executor token out of the service unit: the secret does not exist (the administrator has not run the host step of
	// this release, so there is no token to give the service), and a unit that mounts a secret that is not there does not start. The adoption
	// also sets it for units that predate the executor, so that they compare equal.
	NoExecutor bool
}

// reportBaseURLEnv names the variable that tells the service the address of its own HTTP listener, which is published on the loopback.
const reportBaseURLEnv = "SUITEWARD_REPORT_BASE_URL"

func reportBaseURL(httpPort int) string { return "http://127.0.0.1:" + strconv.Itoa(httpPort) }

// UnitParamsFrom takes the ports, tier, job timeout and whether GitHub is configured from the settings.
func UnitParamsFrom(s Settings, digest string) UnitParams {
	return UnitParams{Digest: digest, Tier: s.Tier, HTTPPort: s.HTTPPort, MCPPort: s.MCPPort, JobTimeout: s.JobTimeout, NoGitHub: s.GitHub.AppID == 0}
}

// The lines of the service unit that mount the GitHub App key and the executor token and name their files; NoGitHub and NoExecutor drop them.
const (
	keySecretLine = "Secret=" + SecretGitHubAppKey + ",type=mount,target=/run/secrets/github-app-key,uid=65532,mode=0400"
	keyEnvLine    = "Environment=SUITEWARD_GITHUB_APP_KEY_FILE=/run/secrets/github-app-key"

	executorSecretLine = "Secret=" + SecretExecutorAuth + ",type=mount,target=/run/secrets/executor-token,uid=65532,mode=0400"
	executorEnvLine    = "Environment=" + executorAuthFileEnv + "=/run/secrets/executor-token"
)

func (p UnitParams) validate() error {
	if !imageDigest.MatchString(p.Digest) {
		return fmt.Errorf("%q is not an image digest of the form sha256:<64 lowercase hex characters>", p.Digest)
	}
	// The tier, ports and job timeout follow the settings rules, which are the one definition of what is valid.
	if err := (Settings{Tier: p.Tier, Runtime: RuntimePodman, HTTPPort: p.HTTPPort, MCPPort: p.MCPPort, JobTimeout: p.JobTimeout}).Validate(); err != nil {
		return err
	}
	if p.EnvFile != "" && !envFilePath.MatchString(p.EnvFile) {
		return fmt.Errorf("the GitHub env file path %q must be one line and must not start with a dash", p.EnvFile)
	}
	return nil
}

// embeddedUnits reads every embedded quadlet unit by file name.
func embeddedUnits() (map[string][]byte, error) {
	entries, err := fs.ReadDir(deploy.Quadlet, "quadlet")
	if err != nil {
		return nil, fmt.Errorf("list the embedded units: %w", err)
	}
	units := make(map[string][]byte, len(entries))
	for _, e := range entries {
		if units[e.Name()], err = unitFile(e.Name()); err != nil {
			return nil, err
		}
	}
	return units, nil
}

// RenderUnits fills the embedded quadlet units: both release Image= lines with one digest, the tier, the published ports,
// the job timeout and the GitHub env file; with NoGitHub the service unit loses the App key lines. The PostgreSQL pin stays
// as written. It returns the unit files by name.
func RenderUnits(p UnitParams) (map[string][]byte, error) {
	templates, err := embeddedUnits()
	if err != nil {
		return nil, err
	}
	return render(templates, p)
}

// lineEdit replaces one whole line of a unit, or removes it.
type lineEdit struct {
	old, new string
	drop     bool
}

// render fills templates; every anchor line it replaces must be there exactly once, so a drifted template fails loudly
// instead of rendering a half-filled unit.
func render(templates map[string][]byte, p UnitParams) (map[string][]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	envFile := p.EnvFile
	if envFile == "" {
		envFile = DefaultEnvFile
	}
	release := lineEdit{old: "Image=" + releaseImageRepo + "@sha256:<digest>", new: "Image=" + releaseImageRepo + "@" + p.Digest}
	tier := "Environment=SUITEWARD_INSTALLATION_TIER=" + string(p.Tier)
	if !p.NoReportURL {
		tier += "\nEnvironment=" + reportBaseURLEnv + "=" + reportBaseURL(p.HTTPPort)
	}
	if p.JobTimeout != "" {
		tier += "\nEnvironment=SUITEWARD_JOB_TIMEOUT=" + p.JobTimeout
	}
	serve := []lineEdit{
		release,
		{old: "PublishPort=127.0.0.1:" + strconv.Itoa(DefaultHTTPPort) + ":8080", new: "PublishPort=127.0.0.1:" + strconv.Itoa(p.HTTPPort) + ":8080"},
		{old: "PublishPort=127.0.0.1:" + strconv.Itoa(DefaultMCPPort) + ":8090", new: "PublishPort=127.0.0.1:" + strconv.Itoa(p.MCPPort) + ":8090"},
		{old: "EnvironmentFile=/etc/suiteward/github.env", new: "EnvironmentFile=" + envFile},
		{old: "Environment=SUITEWARD_INSTALLATION_TIER=isolated", new: tier},
	}
	if p.NoGitHub {
		serve = append(serve, lineEdit{old: keySecretLine, drop: true}, lineEdit{old: keyEnvLine, drop: true})
	}
	if p.NoExecutor {
		serve = append(serve, lineEdit{old: executorSecretLine, drop: true}, lineEdit{old: executorEnvLine, drop: true})
	}
	edits := map[string][]lineEdit{"suiteward-migrate.container": {release}, "suiteward.container": serve}
	out := make(map[string][]byte, len(templates))
	for name, raw := range templates {
		out[name] = raw
	}
	for name, list := range edits {
		raw, ok := templates[name]
		if !ok {
			return nil, fmt.Errorf("the template %s is missing", name)
		}
		text := string(raw)
		for _, e := range list {
			var err error
			if text, err = replaceLine(text, e); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		}
		out[name] = []byte(text)
	}
	return out, nil
}

// replaceLine applies an edit to the one line of text that is exactly e.old.
func replaceLine(text string, e lineEdit) (string, error) {
	lines := strings.Split(text, "\n")
	found := -1
	for i, line := range lines {
		if line != e.old {
			continue
		}
		if found >= 0 {
			return "", fmt.Errorf("the line %q appears twice in the template", e.old)
		}
		found = i
	}
	if found < 0 {
		return "", fmt.Errorf("the template has no line %q (the embedded units and the installer drifted apart)", e.old)
	}
	if e.drop {
		lines = slices.Delete(lines, found, found+1)
	} else {
		lines[found] = e.new
	}
	return strings.Join(lines, "\n"), nil
}

// DaemonReload is the command that makes systemd read changed units; the isolated installer runs it after WriteUnits.
var DaemonReload = []string{"systemctl", "--user", "daemon-reload"}

// WriteResult lists the unit files WriteUnits created or changed.
type WriteResult struct{ Changed []string }

// WriteUnits writes the units into dir (created when missing) and reports which files changed; it needs a daemon reload
// when any did.
func WriteUnits(dir string, units map[string][]byte) (WriteResult, error) {
	names := make([]string, 0, len(units))
	for name := range units {
		if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
			return WriteResult{}, fmt.Errorf("%q is not a unit file name", name)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	var result WriteResult
	for _, name := range names {
		path := filepath.Join(dir, name)
		if existing, err := os.ReadFile(filepath.Clean(path)); err == nil && bytes.Equal(existing, units[name]) {
			continue
		}
		if err := writeFileAtomic(path, units[name], 0o750); err != nil {
			return result, err
		}
		result.Changed = append(result.Changed, name)
	}
	return result, nil
}
