package installer

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The service learns its own address from SUITEWARD_REPORT_BASE_URL (http://127.0.0.1:<HTTP port>): both tiers render it, a stack that
// predates it is adopted as it is and gains it, and a changed HTTP port changes it.

func reportURL(port int) string {
	return "SUITEWARD_REPORT_BASE_URL=http://127.0.0.1:" + strconv.Itoa(port)
}

// withoutReportURL is the inspect output of a service container of v0.3.0-alpha.2, which has no such variable.
func withoutReportURL(name string, obj map[string]any) {
	if name != ContainerServe {
		return
	}
	cfg := obj["Config"].(map[string]any)
	cfg["Env"] = slices.DeleteFunc(slices.Clone(cfg["Env"].([]string)), func(e string) bool { return strings.HasPrefix(e, "SUITEWARD_REPORT_BASE_URL=") })
}

// withReportURL puts a service container's variable to value.
func withReportURL(value string) func(string, map[string]any) {
	return func(name string, obj map[string]any) {
		withoutReportURL(name, obj)
		if name == ContainerServe {
			cfg := obj["Config"].(map[string]any)
			cfg["Env"] = append(cfg["Env"].([]string), "SUITEWARD_REPORT_BASE_URL="+value)
		}
	}
}

func TestColocatedServeGetsTheReportBaseURLOfItsHTTPPort(t *testing.T) {
	for _, port := range []int{18081, 19081} {
		s := colocatedSettings()
		s.HTTPPort = port
		stack, err := ColocatedStack(s, testImage)
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.DeleteFunc(slices.Clone(stack.Serve.Env), func(e string) bool { return !strings.HasPrefix(e, "SUITEWARD_REPORT_BASE_URL=") }); !slices.Equal(got, []string{reportURL(port)}) {
			t.Errorf("serve env for port %d: %v", port, stack.Serve.Env)
		}
		for _, spec := range []RunSpec{stack.DB, stack.Migrate} {
			if strings.Contains(strings.Join(spec.Env, " "), "REPORT_BASE_URL") {
				t.Errorf("%s gets the report base URL", spec.Name)
			}
		}
	}
}

func TestRenderUnitsGivesTheServiceItsReportBaseURL(t *testing.T) {
	for _, port := range []int{DefaultHTTPPort, 18081} {
		p := isolatedParams()
		p.HTTPPort = port
		units, err := RenderUnits(p)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, l := range configLines(units["suiteward.container"]) {
			if strings.Contains(l, "REPORT_BASE_URL") {
				got = append(got, l)
			}
		}
		if want := []string{"Environment=" + reportURL(port)}; !slices.Equal(got, want) {
			t.Errorf("port %d: the service unit has %q, want %q", port, got, want)
		}
		for name, raw := range units {
			if name != "suiteward.container" && bytes.Contains(raw, []byte("REPORT_BASE_URL")) {
				t.Errorf("%s gets the report base URL", name)
			}
		}
	}
	// With a job timeout the two lines come together, and each is there once.
	p := isolatedParams()
	p.JobTimeout = "3m"
	units, err := RenderUnits(p)
	if err != nil {
		t.Fatal(err)
	}
	serve := string(units["suiteward.container"])
	if strings.Count(serve, "REPORT_BASE_URL") != 1 || strings.Count(serve, "JOB_TIMEOUT=3m") != 1 {
		t.Errorf("the service unit with a job timeout:\n%s", serve)
	}
}

func TestAdoptUnitsOfAnAlpha2ServiceUnitWithoutTheReportBaseURL(t *testing.T) {
	line := "Environment=" + reportURL(DefaultHTTPPort)
	installed := renderedUnits(t, nil)
	installed["suiteward.container"] = withoutLines(t, installed["suiteward.container"], line)
	adopted, err := AdoptUnits(installed, nil)
	if err != nil {
		t.Fatalf("a service unit of v0.3.0-alpha.2 is a known shape: %v", err)
	}
	// An install or an upgrade renders the line again, from what was adopted.
	units, err := RenderUnits(UnitParamsFrom(adopted.Settings, strings.TrimPrefix(adopted.Image, releaseImagePrefix)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(configLines(units["suiteward.container"]), line) {
		t.Errorf("the units rendered after an adoption lack %s", line)
	}
}

func TestAdoptUnitsStopsOnAReportBaseURLThatIsNotThePort(t *testing.T) {
	installed := renderedUnits(t, nil)
	installed["suiteward.container"] = bytes.Replace(installed["suiteward.container"], []byte(reportURL(DefaultHTTPPort)), []byte(reportURL(9999)), 1)
	if diff := asDiffError(t, func() error { _, err := AdoptUnits(installed, nil); return err }()); !diffMentions(diff, "REPORT_BASE_URL") {
		t.Errorf("the diff must name the variable: %+v", diff.Diffs)
	}
}

func TestAdoptContainersOfAnAlpha2ServiceWithoutTheReportBaseURL(t *testing.T) {
	want := localPS1Settings()
	if _, err := AdoptContainers(inspectedStack(t, want, testImage, withoutReportURL)); err != nil {
		t.Fatalf("a service container of v0.3.0-alpha.2 is a known shape: %v", err)
	}
	if _, err := AdoptContainers(inspectedStack(t, want, testImage, nil)); err != nil {
		t.Fatalf("a service container with the variable is known: %v", err)
	}
	_, err := AdoptContainers(inspectedStack(t, want, testImage, withReportURL("http://127.0.0.1:9999")))
	if diff := asDiffError(t, err); !diffMentions(diff, "SUITEWARD_REPORT_BASE_URL") {
		t.Errorf("a variable that is not the port's must stop the adoption, naming it: %+v", diff.Diffs)
	}
}

func TestInstallColocatedOverAnAlpha2StackGivesTheServiceTheReportBaseURL(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		port int
	}{"same port": {nil, 18081}, "a changed port": {[]string{"--http-port", "19081"}, 19081}} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, "linux")
			h.existingStack(localPS1Settings(), testImage, withoutReportURL)
			h.have(KindNetwork, NetworkName)
			h.have(KindVolume, VolumeData, VolumeDBData)
			h.have(kindSecret, SecretDBAuth, SecretDatabaseOwnerURL, SecretDatabaseURL, SecretMCPAuth, SecretGitHubAppKey)
			h.first("secret inspect --showsecret --format {{.SecretData}} "+SecretDatabaseURL, DatabaseURL(appRole, "x")+"\n")
			h.roleExists = true
			h.busy[18081], h.busy[18091] = true, true // the running service holds its own ports
			if err := h.install(append([]string{"--runtime", "podman", "--connection", "pm"}, tc.args...)...); err != nil {
				t.Fatalf("an alpha.2 stack must be adopted, not stopped on a diff: %v", err)
			}
			serve := h.f.find("--name suiteward --network")[0].line()
			if !strings.Contains(serve, "--env "+reportURL(tc.port)) {
				t.Errorf("serve lacks %s: %s", reportURL(tc.port), serve)
			}
		})
	}
}

func TestUpgradeColocatedFromAnAlpha2StackGivesTheServiceTheReportBaseURL(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.restack(oldImage, withoutReportURL)
	if err := u.upgrade("--image", testImage, "--yes"); err != nil {
		t.Fatalf("an alpha.2 stack must be adopted by the upgrade: %v", err)
	}
	serve := u.f.find("--name suiteward --network")[0].line()
	if !strings.Contains(serve, "--env "+reportURL(u.port)) {
		t.Errorf("serve lacks %s: %s", reportURL(u.port), serve)
	}
}

func TestInstallIsolatedOverAlpha2UnitsGainsTheReportBaseURLAndRestartsTheService(t *testing.T) {
	home := t.TempDir()
	installedUnits(t, home, isolatedParams())
	path := filepath.Join(unitDir(home), "suiteward.container")
	if err := os.WriteFile(path, withoutLines(t, readFileOrFail(t, path), "Environment="+reportURL(DefaultHTTPPort)), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newIsoFake(home)
	f.serviceRunning, f.everActive = true, true
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatalf("units of v0.3.0-alpha.2 must be adopted, not stopped on a diff: %v", err)
	}
	if got := string(readFileOrFail(t, path)); !strings.Contains(got, "\nEnvironment="+reportURL(DefaultHTTPPort)+"\n") {
		t.Errorf("the service unit did not gain the report base URL:\n%s", got)
	}
	if !f.has("--user restart suiteward.service") {
		t.Errorf("a running service reads its unit when it starts: it must be restarted: %v", f.lines())
	}
}

func TestUpgradeIsolatedFromAlpha2UnitsGivesTheServiceTheReportBaseURL(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	path := filepath.Join(unitDir(u.home), "suiteward.container")
	if err := os.WriteFile(path, withoutLines(t, readFileOrFail(t, path), "Environment="+reportURL(u.port)), 0o600); err != nil {
		t.Fatal(err)
	}
	u.f.noImage = true
	if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
		t.Fatalf("units of v0.3.0-alpha.2 must be adopted by the upgrade: %v", err)
	}
	if got := u.unit("suiteward.container"); !strings.Contains(got, "\nEnvironment="+reportURL(u.port)+"\n") {
		t.Errorf("the upgraded service unit lacks the report base URL:\n%s", got)
	}
}

func TestInstallIsolatedPortChangeUpdatesTheReportBaseURL(t *testing.T) {
	home := t.TempDir()
	installedUnits(t, home, isolatedParams())
	f := newIsoFake(home)
	f.serviceRunning, f.everActive = true, true
	if _, err := runIsolated(t, f, map[string]string{"http-port": "18082"}, ""); err != nil {
		t.Fatal(err)
	}
	serve := string(readFileOrFail(t, filepath.Join(unitDir(home), "suiteward.container")))
	if !strings.Contains(serve, "\nEnvironment="+reportURL(18082)+"\n") || strings.Contains(serve, reportURL(DefaultHTTPPort)+"\n") {
		t.Errorf("the report base URL did not follow the HTTP port:\n%s", serve)
	}
}
