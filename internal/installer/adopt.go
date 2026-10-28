package installer

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// Diff is one difference between an installed stack and what the named release would install.
type Diff struct {
	Source string // the unit file or the container
	Field  string // the unit section or the container property
	Want   string // what the release has; empty when the installed stack has something it does not have
	Got    string // what is installed; empty when the installed stack lacks something the release has
}

// DiffError stops an adoption: something is installed that the installer does not know how to carry over, and it never
// overwrites an installation silently.
type DiffError struct{ Diffs []Diff }

func (e *DiffError) Error() string {
	var b strings.Builder
	b.WriteString("the installation differs from this release, and nothing was changed. The installer never overwrites an installation that was edited by hand. Undo each edit listed below, or move it to a setting the installer supports, then run again:")
	for _, d := range e.Diffs {
		want, got := d.printable()
		fmt.Fprintf(&b, "\n  %s %s: want %q, got %q", d.Source, d.Field, want, got)
	}
	return b.String()
}

// environmentName finds the NAME of the NAME=value assignments in a list or a unit line.
var environmentName = regexp.MustCompile(`(?:^|[ ,"'])([A-Z_][A-Z0-9_]*)=`)

// printable is the want and got of a diff as a message may show them: an environment value can be a password or a token,
// so a variable is named and its value never printed.
func (d Diff) printable() (want, got string) {
	return hideEnvironment(d.Field == "environment", d.Want), hideEnvironment(d.Field == "environment", d.Got)
}

func hideEnvironment(list bool, text string) string {
	if rest, ok := strings.CutPrefix(text, "Environment="); ok {
		text, list = rest, true
	}
	if !list || text == "" {
		return text
	}
	var names []string
	for _, m := range environmentName.FindAllStringSubmatch(text, -1) {
		names = append(names, m[1])
	}
	return strings.Join(names, ", ") + " (values hidden)"
}

// Adopted is what an adoption carries over from an installed stack.
type Adopted struct {
	Settings   Settings // tier, runtime, ports, job timeout and, from containers, the GitHub values
	Image      string   // the release image that is installed
	EnvFile    string   // units only: the EnvironmentFile path found, which the isolated installer moves
	DBImage    string   // the PostgreSQL image the installed database runs, when it could be read
	NoGitHub   bool     // units only: the installed service unit has no App key lines, so its stack runs without GitHub
	NoExecutor bool     // units only: the installed service unit has no executor token lines (an earlier release, or no token yet)
}

// parseMemory reads a --memory value (k, m or g suffix, or plain bytes) as bytes.
func parseMemory(s string) (int64, error) {
	unit := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'k':
			unit, s = 1<<10, s[:n-1]
		case 'm':
			unit, s = 1<<20, s[:n-1]
		case 'g':
			unit, s = 1<<30, s[:n-1]
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a memory size (a number with an optional k, m or g)", s)
	}
	return n * unit, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ---- units ----

const (
	serveUnit = "suiteward.container"
	dbUnit    = "suiteward-db.container"
	// postgresRepo starts the Image= line of the database unit.
	postgresRepo = "docker.io/library/postgres:"
	// A valid digest that stands in for an installed one that could not be read; the lines it would render are not compared.
	unknownDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
)

var (
	installedImage = regexp.MustCompile(`^Image=` + releaseImageRepo + `@(sha256:[0-9a-f]{64})$`)
	installedHTTP  = regexp.MustCompile(`^PublishPort=127\.0\.0\.1:([0-9]+):8080$`)
	installedMCP   = regexp.MustCompile(`^PublishPort=127\.0\.0\.1:([0-9]+):8090$`)
)

// AdoptUnits compares the installed unit files with the units of a named release (reference; nil means the embedded ones of
// this binary). The ports, tier, job timeout, image digest, env-file path and whether the service unit has the App key lines
// are known fields and are carried into the settings; any other difference, in any unit, is returned in a *DiffError.
func AdoptUnits(installed, reference map[string][]byte) (Adopted, error) {
	return adoptUnits(installed, reference, false)
}

// adoptUnits is AdoptUnits; with refreshDB (an upgrade) an installed database unit that pins another digest-pinned PostgreSQL
// image is not a difference: the upgrade replaces the pin, and the pin it found is returned in DBImage.
func adoptUnits(installed, reference map[string][]byte, refreshDB bool) (Adopted, error) {
	if reference == nil {
		var err error
		if reference, err = embeddedUnits(); err != nil {
			return Adopted{}, err
		}
	}
	params, diffs, imageKnown := knownUnitFields(configLinesOf(installed[serveUnit]))
	if err := params.validate(); err != nil {
		return Adopted{}, &DiffError{append(diffs, Diff{Source: serveUnit, Field: "known fields", Got: err.Error()})}
	}
	rendered, err := render(reference, params)
	if err != nil {
		return Adopted{}, err
	}
	skip := ""
	if !imageKnown {
		skip = "Image=" + releaseImageRepo
	}
	dbImage := ""
	if m := postgresImageLine.FindSubmatch(installed[dbUnit]); m != nil {
		dbImage = string(m[1])
	}
	for _, name := range sortedKeys(rendered) {
		got, ok := installed[name]
		if !ok {
			diffs = append(diffs, Diff{Source: name, Field: "file", Want: "the unit file"})
			continue
		}
		skipLine := skip
		// Exactly one database image line is relaxed: a second one is a difference, not a pin to refresh.
		if refreshDB && name == dbUnit && dbImage != "" && countLinesWithPrefix(got, "Image="+postgresRepo) == 1 {
			skipLine = "Image=" + postgresRepo
		}
		diffs = append(diffs, diffUnitLines(name, rendered[name], got, skipLine)...)
	}
	if len(diffs) > 0 {
		return Adopted{}, &DiffError{diffs}
	}
	return Adopted{
		Settings: Settings{Tier: params.Tier, Runtime: RuntimePodman, HTTPPort: params.HTTPPort, MCPPort: params.MCPPort, JobTimeout: params.JobTimeout},
		Image:    releaseImageRepo + "@" + params.Digest,
		EnvFile:  params.EnvFile,
		DBImage:  dbImage,
		NoGitHub: params.NoGitHub, NoExecutor: params.NoExecutor,
	}, nil
}

// countLinesWithPrefix counts the configuration lines of a unit that start with prefix.
func countLinesWithPrefix(raw []byte, prefix string) int {
	n := 0
	for _, line := range configLinesOf(raw) {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

// configLinesOf are the lines that count: comments and blank lines are not configuration.
func configLinesOf(raw []byte) []string {
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

// knownUnitFields reads the carried fields of the installed service unit. A field that cannot be read is a Diff and its
// default stands in; imageKnown says whether the image digest was read.
func knownUnitFields(serve []string) (p UnitParams, diffs []Diff, imageKnown bool) {
	p = UnitParams{Digest: unknownDigest, Tier: config.TierIsolated, HTTPPort: DefaultHTTPPort, MCPPort: DefaultMCPPort}
	bad := func(field, want string, got []string) {
		diffs = append(diffs, Diff{Source: serveUnit, Field: field, Want: want, Got: strings.Join(got, "; ")})
	}
	// values returns the text after prefix on every line that starts with it.
	values := func(prefix string) (out []string) {
		for _, l := range serve {
			if v, ok := strings.CutPrefix(l, prefix); ok {
				out = append(out, v)
			}
		}
		return out
	}

	if lines := values("Image="); len(lines) == 1 && installedImage.MatchString("Image="+lines[0]) {
		p.Digest, imageKnown = lines[0][strings.Index(lines[0], "@")+1:], true
	} else {
		bad("Image", "Image="+releaseImageRepo+"@sha256:<64 hex characters>", lines)
	}

	var httpPorts, mcpPorts []string
	unmatched := 0
	for _, v := range values("PublishPort=") {
		line := "PublishPort=" + v
		switch {
		case installedHTTP.MatchString(line):
			httpPorts = append(httpPorts, installedHTTP.FindStringSubmatch(line)[1])
		case installedMCP.MatchString(line):
			mcpPorts = append(mcpPorts, installedMCP.FindStringSubmatch(line)[1])
		default:
			unmatched++
			bad("PublishPort", "127.0.0.1:<port>:8080 or 127.0.0.1:<port>:8090", []string{line})
		}
	}
	switch {
	case len(httpPorts) == 1 && len(mcpPorts) == 1:
		p.HTTPPort, _ = strconv.Atoi(httpPorts[0])
		p.MCPPort, _ = strconv.Atoi(mcpPorts[0])
	case unmatched == 0: // lines that matched neither pattern are already reported
		bad("PublishPort", "one loopback port for 8080 and one for 8090", values("PublishPort="))
	}

	if v := values("Environment=SUITEWARD_INSTALLATION_TIER="); len(v) == 1 {
		p.Tier = config.Tier(v[0])
	} else {
		bad("tier", "one Environment=SUITEWARD_INSTALLATION_TIER= line", v)
	}
	switch v := values("Environment=SUITEWARD_JOB_TIMEOUT="); len(v) {
	case 0:
	case 1:
		p.JobTimeout = v[0]
	default:
		bad("job timeout", "at most one Environment=SUITEWARD_JOB_TIMEOUT= line", v)
	}
	if v := values("EnvironmentFile="); len(v) == 1 {
		p.EnvFile = v[0]
	} else {
		bad("env file", "one EnvironmentFile= line", v)
	}
	// A unit with neither App key line is a stack without GitHub, a known shape; one line alone is read as "with" so that the
	// comparison names the other as missing.
	p.NoGitHub = len(values("Secret="+SecretGitHubAppKey+",")) == 0 && len(values("Environment=SUITEWARD_GITHUB_APP_KEY_FILE=")) == 0
	// A unit of v0.3.0-alpha.2 has no report base URL line, a known shape; a line that is there must be the port's, or it is a diff.
	p.NoReportURL = len(values("Environment="+reportBaseURLEnv+"=")) == 0
	// A unit of an earlier release has neither executor line, a known shape; one line alone is read as "with" so that the comparison names the
	// other as missing.
	p.NoExecutor = len(values("Secret="+SecretExecutorAuth+",")) == 0 && len(values("Environment="+executorAuthFileEnv+"=")) == 0
	return p, diffs, imageKnown
}

type unitLine struct{ section, line string }

func unitLines(raw []byte, skip string) map[unitLine]int {
	counts := map[unitLine]int{}
	section := ""
	for _, line := range configLinesOf(raw) {
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line
			continue
		}
		if skip != "" && strings.HasPrefix(line, skip) {
			continue
		}
		counts[unitLine{section, line}]++
	}
	return counts
}

// diffUnitLines compares the configuration lines of two units, section by section; comments, blank lines and the order of
// lines do not count. Lines that start with skip are left out on both sides.
func diffUnitLines(name string, want, got []byte, skip string) []Diff {
	wantLines, gotLines := unitLines(want, skip), unitLines(got, skip)
	var diffs []Diff
	for _, k := range sortedUnitLines(wantLines) {
		if gotLines[k] < wantLines[k] {
			diffs = append(diffs, Diff{Source: name, Field: k.section, Want: k.line})
		}
	}
	for _, k := range sortedUnitLines(gotLines) {
		if gotLines[k] > wantLines[k] {
			diffs = append(diffs, Diff{Source: name, Field: k.section, Got: k.line})
		}
	}
	return diffs
}

func sortedUnitLines(m map[unitLine]int) []unitLine {
	keys := make([]unitLine, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b unitLine) int { return strings.Compare(a.section+"\n"+a.line, b.section+"\n"+b.line) })
	return keys
}
