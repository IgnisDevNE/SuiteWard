package installer

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

const releaseImagePrefix = "ghcr.io/ignisdevne/suiteward@"

var unitNames = []string{
	"suiteward-data.volume", "suiteward-db-data.volume", "suiteward-db.container",
	"suiteward-migrate.container", "suiteward.container", "suiteward.network",
}

func isolatedParams() UnitParams {
	return UnitParams{Digest: testDigest, Tier: config.TierIsolated, HTTPPort: DefaultHTTPPort, MCPPort: DefaultMCPPort}
}

// configLines are the lines that count: comments and blanks are not configuration.
func configLines(unit []byte) []string {
	var out []string
	for _, line := range strings.Split(string(unit), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out
}

func embeddedLines(t *testing.T, name string) []string {
	t.Helper()
	raw, err := unitFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return configLines(raw)
}

func hasPrefix(lines []string, prefix string) bool {
	return slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, prefix) })
}

// The quadlet assertions of scripts/test-deploy.ps1, over the embedded units.
func TestEmbeddedMigrateUnitFailsClosedAndHoldsTheOwnerURL(t *testing.T) {
	migrate := embeddedLines(t, "suiteward-migrate.container")
	for _, line := range []string{
		"Type=oneshot", "RemainAfterExit=yes", "Exec=migrate", "Requires=suiteward-db.service", "After=suiteward-db.service",
		"Network=suiteward.network", "ReadOnly=true", "DropCapability=ALL", "NoNewPrivileges=true",
		"Environment=SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url",
		"Environment=SUITEWARD_MIGRATION_DATABASE_URL_FILE=/run/secrets/database-owner-url",
	} {
		if !slices.Contains(migrate, line) {
			t.Errorf("suiteward-migrate.container lacks %s", line)
		}
	}
	for _, prefix := range []string{
		"Secret=suiteward-database-owner-url,type=mount,target=/run/secrets/database-owner-url",
		"Secret=suiteward-database-url,type=mount,target=/run/secrets/database-url",
	} {
		if !hasPrefix(migrate, prefix) {
			t.Errorf("migrate does not mount %s", prefix)
		}
	}
	// A failed migrate must leave the service down: a restart loop would hide a real failure.
	for _, key := range []string{"PublishPort=", "Restart=", "RestartSec=", "StartLimitIntervalSec=", "AutoUpdate="} {
		if hasPrefix(migrate, key) {
			t.Errorf("migrate has %s", key)
		}
	}
}

func TestEmbeddedServeUnitNeverSeesTheOwnerURL(t *testing.T) {
	serve := embeddedLines(t, "suiteward.container")
	for _, line := range []string{
		"Requires=suiteward-migrate.service", "After=suiteward-migrate.service", "ReadOnly=true", "DropCapability=ALL", "NoNewPrivileges=true",
	} {
		if !slices.Contains(serve, line) {
			t.Errorf("suiteward.container lacks %s", line)
		}
	}
	if !hasPrefix(serve, "Secret=suiteward-database-url,type=mount") {
		t.Error("serve does not mount the application URL secret")
	}
	for _, line := range serve {
		if strings.Contains(line, "owner") || strings.Contains(line, "MIGRATION") {
			t.Errorf("serve sees the owner URL: %s", line)
		}
	}
	if hasPrefix(serve, "AutoUpdate=") {
		t.Error("serve has AutoUpdate (a documented opt-in, not the default)")
	}
}

func TestEmbeddedReleaseImageLinesAgree(t *testing.T) {
	pinned := regexp.MustCompile(`^Image=ghcr\.io/ignisdevne/suiteward@sha256:(<digest>|[0-9a-f]{64})$`)
	var images []string
	for _, name := range []string{"suiteward.container", "suiteward-migrate.container"} {
		for _, line := range embeddedLines(t, name) {
			if strings.HasPrefix(line, "Image=") {
				images = append(images, line)
			}
		}
	}
	if len(images) != 2 || images[0] != images[1] || !pinned.MatchString(images[0]) {
		t.Fatalf("the release Image= lines are not one digest pin: %v", images)
	}
}

func TestRenderUnitsGolden(t *testing.T) {
	units, err := RenderUnits(isolatedParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != len(unitNames) {
		t.Fatalf("got %d units", len(units))
	}
	serve := configLines(units["suiteward.container"])
	want := []string{
		"[Unit]", "Description=SuiteWard", "Wants=network-online.target", "After=network-online.target",
		"Requires=suiteward-migrate.service", "After=suiteward-migrate.service", "StartLimitIntervalSec=0",
		"[Container]", "Image=" + releaseImagePrefix + testDigest, "ContainerName=suiteward", "Network=suiteward.network",
		"PublishPort=127.0.0.1:8081:8080", "PublishPort=127.0.0.1:8091:8090",
		"Secret=suiteward-database-url,type=mount,target=/run/secrets/database-url,uid=65532,mode=0400",
		"Environment=SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url",
		"Secret=suiteward-github-app-key,type=mount,target=/run/secrets/github-app-key,uid=65532,mode=0400",
		"Environment=SUITEWARD_GITHUB_APP_KEY_FILE=/run/secrets/github-app-key",
		"Secret=suiteward-mcp-token,type=mount,target=/run/secrets/mcp-token,uid=65532,mode=0400",
		"Environment=SUITEWARD_MCP_TOKEN_FILE=/run/secrets/mcp-token",
		"EnvironmentFile=%h/.config/suiteward/github.env", "Environment=SUITEWARD_INSTALLATION_TIER=isolated",
		"Volume=suiteward-data.volume:/data", "ReadOnly=true", "DropCapability=ALL", "NoNewPrivileges=true",
		"PodmanArgs=--memory=256m --stop-timeout=45",
		"[Service]", "Restart=always", "RestartSec=5", "[Install]", "WantedBy=default.target",
	}
	if !slices.Equal(serve, want) {
		t.Fatalf("rendered serve unit differs:\n got %q\nwant %q", serve, want)
	}
}

func TestRenderUnitsLeavesEverythingElseAsWritten(t *testing.T) {
	units, err := RenderUnits(isolatedParams())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"suiteward-db.container", "suiteward-data.volume", "suiteward-db-data.volume", "suiteward.network"} {
		raw, err := unitFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(units[name], raw) {
			t.Errorf("%s was changed by the rendering", name)
		}
	}
	pg, _ := PostgresImage()
	if !bytes.Contains(units["suiteward-db.container"], []byte("Image="+pg)) {
		t.Error("the PostgreSQL pin is not the embedded one")
	}
}

func TestRenderUnitsFillsBothReleaseImagesWithOneDigest(t *testing.T) {
	units, err := RenderUnits(isolatedParams())
	if err != nil {
		t.Fatal(err)
	}
	line := "Image=" + releaseImagePrefix + testDigest
	for _, name := range []string{"suiteward.container", "suiteward-migrate.container"} {
		if images := slices.DeleteFunc(configLines(units[name]), func(l string) bool { return !strings.HasPrefix(l, "Image=") }); !slices.Equal(images, []string{line}) {
			t.Errorf("%s: %v", name, images)
		}
		if slices.ContainsFunc(configLines(units[name]), func(l string) bool { return strings.Contains(l, "<digest>") }) {
			t.Errorf("%s still holds the placeholder", name)
		}
	}
}

func TestRenderUnitsRefusesAnythingButAnSHA256Digest(t *testing.T) {
	for _, digest := range []string{
		"", "latest", "sha256:abc", "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("g", 64),
		strings.Repeat("a", 64), "sha256:" + strings.Repeat("a", 64) + "\n", "sha256:" + strings.Repeat("a", 65), "@" + testDigest,
	} {
		p := isolatedParams()
		p.Digest = digest
		if _, err := RenderUnits(p); err == nil {
			t.Errorf("accepted the digest %q", digest)
		}
	}
}

func TestRenderUnitsPortsTierAndTimeout(t *testing.T) {
	p := isolatedParams()
	p.HTTPPort, p.MCPPort, p.Tier, p.JobTimeout = 18081, 18091, config.TierCoLocated, "3m"
	units, err := RenderUnits(p)
	if err != nil {
		t.Fatal(err)
	}
	serve := configLines(units["suiteward.container"])
	for _, line := range []string{
		"PublishPort=127.0.0.1:18081:8080", "PublishPort=127.0.0.1:18091:8090",
		"Environment=SUITEWARD_INSTALLATION_TIER=co-located", "Environment=SUITEWARD_JOB_TIMEOUT=3m",
	} {
		if !slices.Contains(serve, line) {
			t.Errorf("serve lacks %s", line)
		}
	}
	for _, line := range serve {
		if strings.Contains(line, ":8081:") || strings.Contains(line, ":8091:") || strings.HasSuffix(line, "=isolated") {
			t.Errorf("a default survived: %s", line)
		}
	}
	// The job time limit belongs to serve alone.
	if bytes.Contains(units["suiteward-migrate.container"], []byte("JOB_TIMEOUT")) {
		t.Error("migrate gets the job timeout")
	}
	p.JobTimeout = ""
	units, _ = RenderUnits(p)
	if bytes.Contains(units["suiteward.container"], []byte("JOB_TIMEOUT")) {
		t.Error("a job timeout was rendered although none is set")
	}
}

func TestRenderUnitsRefusesBadValues(t *testing.T) {
	for name, mutate := range map[string]func(*UnitParams){
		"tier":            func(p *UnitParams) { p.Tier = "cloud" },
		"equal ports":     func(p *UnitParams) { p.MCPPort = p.HTTPPort },
		"port zero":       func(p *UnitParams) { p.HTTPPort = 0 },
		"timeout":         func(p *UnitParams) { p.JobTimeout = "soon\nExecStartPre=/bin/evil" },
		"env file line":   func(p *UnitParams) { p.EnvFile = "%h/x\nExecStartPre=/bin/evil" },
		"optional prefix": func(p *UnitParams) { p.EnvFile = "-%h/.config/suiteward/github.env" },
	} {
		p := isolatedParams()
		mutate(&p)
		if _, err := RenderUnits(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRenderUnitsEnvFile(t *testing.T) {
	units, err := RenderUnits(isolatedParams())
	if err != nil {
		t.Fatal(err)
	}
	lines := configLines(units["suiteward.container"])
	if !slices.Contains(lines, "EnvironmentFile="+DefaultEnvFile) || slices.Contains(lines, "EnvironmentFile=/etc/suiteward/github.env") {
		t.Fatalf("the env file did not move to the service account: %q", lines)
	}
	p := isolatedParams()
	p.EnvFile = "/etc/suiteward/github.env" // an adopted host that still has it there
	units, err = RenderUnits(p)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(configLines(units["suiteward.container"]), "EnvironmentFile=/etc/suiteward/github.env") {
		t.Fatal("a given env file path was not rendered")
	}
}

func TestRenderRefusesATemplateThatLostAnAnchor(t *testing.T) {
	templates := map[string][]byte{}
	for _, name := range unitNames {
		raw, err := unitFile(name)
		if err != nil {
			t.Fatal(err)
		}
		templates[name] = raw
	}
	for name, anchor := range map[string]string{
		"suiteward.container":         "PublishPort=127.0.0.1:8091:8090\n",
		"suiteward-migrate.container": "Image=" + releaseImagePrefix + "sha256:<digest>\n",
	} {
		broken := map[string][]byte{}
		for k, v := range templates {
			broken[k] = v
		}
		broken[name] = bytes.Replace(templates[name], []byte(anchor), nil, 1)
		if _, err := render(broken, isolatedParams()); err == nil {
			t.Errorf("a %s without %q rendered", name, anchor)
		}
	}
}

func TestWriteUnits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "containers", "systemd")
	units, err := RenderUnits(isolatedParams())
	if err != nil {
		t.Fatal(err)
	}
	first, err := WriteUnits(dir, units)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Changed) != len(unitNames) {
		t.Fatalf("first write changed %v", first.Changed)
	}
	for name, content := range units {
		got, err := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
		if err != nil || !bytes.Equal(got, content) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	again, err := WriteUnits(dir, units)
	if err != nil || len(again.Changed) != 0 {
		t.Fatalf("an unchanged write reported %v, %v", again.Changed, err)
	}
	p := isolatedParams()
	p.HTTPPort = 18081
	changed, _ := RenderUnits(p)
	third, err := WriteUnits(dir, changed)
	if err != nil || !slices.Equal(third.Changed, []string{"suiteward.container"}) {
		t.Fatalf("a port change must rewrite only the service unit: %v, %v", third.Changed, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "suiteward.container"))
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("unit mode: %v %v", info, err)
		}
	}
	if !slices.Equal(DaemonReload, []string{"systemctl", "--user", "daemon-reload"}) {
		t.Fatalf("daemon reload command: %v", DaemonReload)
	}
}

func TestWriteUnitsRefusesAPathOutsideTheDirectory(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../escape.container", "sub/x.container", `..\escape.container`} {
		if _, err := WriteUnits(dir, map[string][]byte{name: []byte("x")}); err == nil {
			t.Errorf("wrote %q", name)
		}
	}
}
