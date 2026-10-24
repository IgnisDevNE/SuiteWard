package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// M1.14 N1: the defaults moved from 8081 (HTTP) and 8091 (MCP), which other tools on a development machine use, to 27081 and 27091. A stack that
// exists never moves: its ports are in its settings file, its containers or its units, and what is read there wins over the default. The tests
// after the first two install on the old numbers, so a default that leaked into an existing install would show. They pass before the defaults
// move as well (the installed ports always came from the stack); they are what keeps the move from reaching a stack that exists.

const (
	oldHTTPPort = 8081
	oldMCPPort  = 8091
)

func TestDefaultPortsAreOffTheCrowdedOnes(t *testing.T) {
	if got := DefaultSettings(); got.HTTPPort != 27081 || got.MCPPort != 27091 {
		t.Fatalf("the default ports are %d and %d, want 27081 and 27091", got.HTTPPort, got.MCPPort)
	}
}

func TestInstallPodmanPublishesTheDefaultPortsOfAFreshInstall(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	serve := h.f.find("--name suiteward --network")[0].line()
	for _, want := range []string{"--publish 127.0.0.1:27081:8080", "--publish 127.0.0.1:27091:8090"} {
		if !strings.Contains(serve, want) {
			t.Errorf("serve lacks %q: %s", want, serve)
		}
	}
	if s := h.settings(); s.HTTPPort != 27081 || s.MCPPort != 27091 {
		t.Errorf("saved settings: %+v", s)
	}
}

// oldPortSettings are the settings of a stack installed before the defaults moved.
func oldPortSettings(tier config.Tier) Settings {
	s := DefaultSettings()
	s.Tier, s.HTTPPort, s.MCPPort = tier, oldHTTPPort, oldMCPPort
	return s
}

// A re-run of the install takes the ports of the settings file when nothing else says which stack it is: a stack that was uninstalled without
// --purge, or a first run that stopped before the containers existed.
func TestInstallColocatedKeepsTheOldPortsOfItsSettingsFile(t *testing.T) {
	h := newHarness(t, "linux")
	if err := SaveSettings(h.settingsPath(), oldPortSettings(config.TierCoLocated)); err != nil {
		t.Fatal(err)
	}
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	serve := h.f.find("--name suiteward --network")[0].line()
	for _, want := range []string{"--publish 127.0.0.1:8081:8080", "--publish 127.0.0.1:8091:8090"} {
		if !strings.Contains(serve, want) {
			t.Errorf("serve lacks %q: %s", want, serve)
		}
	}
	if s := h.settings(); s.HTTPPort != oldHTTPPort || s.MCPPort != oldMCPPort {
		t.Errorf("saved settings: %+v", s)
	}
}

func TestInstallIsolatedKeepsTheOldPortsOfItsSettingsFile(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.noImage = true
	if err := SaveSettings(settingsFile(f.home), oldPortSettings(config.TierIsolated)); err != nil {
		t.Fatal(err)
	}
	if _, err := runIsolated(t, f, nil, ""); err != nil {
		t.Fatal(err)
	}
	serve := string(readFileOrFail(t, filepath.Join(unitDir(f.home), "suiteward.container")))
	for _, want := range []string{"\nPublishPort=127.0.0.1:8081:8080\n", "\nPublishPort=127.0.0.1:8091:8090\n"} {
		if !strings.Contains(serve, want) {
			t.Errorf("the unit lacks %q:\n%s", want, serve)
		}
	}
	if s, err := LoadSettings(settingsFile(f.home)); err != nil || s.HTTPPort != oldHTTPPort || s.MCPPort != oldMCPPort {
		t.Errorf("saved settings: %+v, %v", s, err)
	}
}

// A host installed by hand from the install guide has units and no settings file: a re-run of the install, given no port, keeps the ports of
// the units and does not publish the defaults.
func TestInstallIsolatedRerunOverUnitsOnTheOldPortsKeepsThem(t *testing.T) {
	home := t.TempDir()
	installedUnits(t, home, UnitParams{Digest: testDigest, Tier: config.TierIsolated, HTTPPort: oldHTTPPort, MCPPort: oldMCPPort, EnvFile: DefaultEnvFile})
	f := newIsoFake(home)
	out, err := runIsolated(t, f, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "you asked for") {
		t.Errorf("no port was given, so nothing was asked for:\n%s", out)
	}
	serve := string(readFileOrFail(t, filepath.Join(unitDir(home), "suiteward.container")))
	for _, want := range []string{"\nPublishPort=127.0.0.1:8081:8080\n", "\nPublishPort=127.0.0.1:8091:8090\n"} {
		if !strings.Contains(serve, want) {
			t.Errorf("the unit lacks %q:\n%s", want, serve)
		}
	}
	if s, err := LoadSettings(settingsFile(home)); err != nil || s.HTTPPort != oldHTTPPort || s.MCPPort != oldMCPPort {
		t.Errorf("saved settings: %+v, %v", s, err)
	}
}

// onlyPort fails the test when any of the addresses asked for is not on the port: nothing may reach another port of this machine, which
// is where the live stack of the person who runs the tests may listen.
func onlyPort(t *testing.T, port string, asked []string) {
	t.Helper()
	if len(asked) == 0 {
		t.Fatal("no address was asked for, so the port check proves nothing")
	}
	for _, address := range asked {
		if !strings.HasPrefix(address, "http://127.0.0.1:"+port+"/") {
			t.Errorf("the run read %s, want port %s", address, port)
		}
	}
}

// `install --upgrade` of a stack that local.ps1 made has no settings file: the containers it publishes are the only source of its ports, and
// the upgrade saves them as the settings.
func TestUpgradeColocatedOfAStackWithoutSettingsKeepsTheOldPorts(t *testing.T) {
	u := newUpgradeHarness(t, func(s *Settings) { s.HTTPPort, s.MCPPort = oldHTTPPort, oldMCPPort })
	var asked []string
	u.status = func(_ context.Context, address string) ([]byte, error) { // the real network may hold the live stack of this PC on 8081
		asked = append(asked, address)
		return []byte(statusBody), nil
	}
	if err := os.Remove(u.settingsPath()); err != nil {
		t.Fatal(err)
	}
	useDefaultConfigDir(t)
	recipient, _ := testRecoveryKey(t)
	args := []string{
		"--upgrade", "--yes", "--tier", "co-located", "--runtime", "podman", "--connection", "pm", "--image", testImage,
		"--token-file", u.tokenPath(), "--backup-dir", u.backups, "--backup-recipient", recipient,
	}
	if err := Install(context.Background(), args, u.env()); err != nil {
		t.Fatal(err)
	}
	serve := u.f.find("--name suiteward --network")[0].line()
	for _, want := range []string{"--publish 127.0.0.1:8081:8080", "--publish 127.0.0.1:8091:8090"} {
		if !strings.Contains(serve, want) {
			t.Errorf("serve lacks %q: %s", want, serve)
		}
	}
	path, err := DefaultSettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if s, err := LoadSettings(path); err != nil || s.HTTPPort != oldHTTPPort || s.MCPPort != oldMCPPort {
		t.Errorf("saved settings: %+v, %v", s, err)
	}
	onlyPort(t, "8081", asked)
}

// The same for a host installed by hand: its units publish the ports, and the upgrade keeps them.
func TestUpgradeIsolatedOfAHostOnTheOldPortsKeepsThem(t *testing.T) {
	u := newIsoUpgrade(t, func(p *UnitParams) { p.HTTPPort, p.MCPPort = oldHTTPPort, oldMCPPort })
	if err := os.Remove(settingsFile(u.home)); err != nil {
		t.Fatal(err)
	}
	useDefaultConfigDir(t)
	var asked []string
	env := u.env()
	env.HTTPGet = func(_ context.Context, address string) ([]byte, error) { // the real network may hold the live stack of this host on 8081
		asked = append(asked, address)
		return []byte(statusBody), nil
	}
	fastHealthWait(t)
	recipient, _ := testRecoveryKey(t)
	args := []string{"--upgrade", "--tier", "isolated", "--image", testDigest, "--yes", "--backup-dir", u.backups, "--backup-recipient", recipient}
	if err := Install(context.Background(), args, env); err != nil {
		t.Fatal(err)
	}
	serve := u.unit("suiteward.container")
	for _, want := range []string{"\nPublishPort=127.0.0.1:8081:8080\n", "\nPublishPort=127.0.0.1:8091:8090\n", "@" + testDigest} {
		if !strings.Contains(serve, want) {
			t.Errorf("the unit lacks %q:\n%s", want, serve)
		}
	}
	if s, err := LoadSettings(settingsFile(u.home)); err != nil || s.HTTPPort != oldHTTPPort || s.MCPPort != oldMCPPort {
		t.Errorf("saved settings: %+v, %v", s, err)
	}
	onlyPort(t, "8081", asked)
}
