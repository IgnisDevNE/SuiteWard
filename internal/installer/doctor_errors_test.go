package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("the pipe is closed") }

// Doctor says so when it cannot do its job at all, and never hides a failure behind an exit code of 0.
func TestDoctorReportsWhatPreventsItFromRunning(t *testing.T) {
	t.Run("deps without an Exec", func(t *testing.T) {
		var out bytes.Buffer
		code := Doctor(context.Background(), Deps{GOOS: "linux", Getenv: func(string) string { return "" }}, filepath.Join(t.TempDir(), "settings.yaml"), "", &out, false)
		if code != 1 || !strings.Contains(out.String(), "FAIL settings") || !strings.Contains(out.String(), "report it") {
			t.Errorf("exit %d, output %q", code, out.String())
		}
	})
	t.Run("an output that cannot be written", func(t *testing.T) {
		f, s := colocated{}.build(t)
		path := filepath.Join(t.TempDir(), "settings.yaml")
		if err := SaveSettings(path, s); err != nil {
			t.Fatal(err)
		}
		if code := Doctor(context.Background(), f.deps(), path, "", failingWriter{}, false); code != 1 {
			t.Errorf("exit %d for a healthy stack whose report could not be written, want 1", code)
		}
	})
	t.Run("no default settings place", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("AppData", "")
		t.Setenv("HOME", "")
		f := newFake("linux")
		var out bytes.Buffer
		if code := Doctor(context.Background(), f.deps(), "", "", &out, false); code != 1 || !strings.Contains(out.String(), "--settings PATH") {
			t.Errorf("exit %d, output %q", code, out.String())
		}
		if len(f.calls) != 0 {
			t.Errorf("commands ran without settings: %v", f.lines())
		}
	})
}

func TestDoctorMachineProblems(t *testing.T) {
	cases := []struct {
		name       string
		over       []rule
		connection string
		env        string
		want       []string
		fix        string
	}{
		{"the machines cannot be listed", []rule{{match: "machine list", err: errors.New("podman: not found")}}, "podman-machine-default", "", []string{"list the Podman machines", "not found"}, "installed"},
		{"the list is not JSON", []rule{{match: "machine list", out: "not json"}}, "podman-machine-default", "", []string{"read the Podman machines"}, "machine list --format json"},
		{"no machine of that name", []rule{{match: "machine list", out: `[{"Name":"other","Running":true}]`}}, "podman-machine-default", "", []string{"no Podman machine is called podman-machine-default"}, "podman machine init"},
		{"no machine at all", []rule{{match: "machine list", out: `[]`}}, "", "", []string{"no Podman machine exists"}, "podman machine init"},
		{"the machine is starting", []rule{{match: "machine list", out: `[{"Name":"podman-machine-default","Starting":true}]`}}, "podman-machine-default", "", []string{"still starting"}, "wait"},
		{"a machine with its own name is stopped", []rule{{match: "machine list", out: `[{"Name":"dev","Running":false}]`}}, "dev", "", []string{"dev is not running"}, "podman machine start dev"},
		{"the machine is chosen by the environment", []rule{{match: "machine list", out: `[{"Name":"dev","Running":false}]`}}, "", "dev-root", []string{"dev is not running"}, "podman machine start dev"},
		{"the first machine is taken when none is named or running", []rule{{match: "machine list", out: `[{"Name":"a","Running":false},{"Name":"b","Running":false}]`}}, "", "", []string{"a is not running"}, "podman machine start a"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s := colocated{goos: "windows", over: c.over}.build(t)
			s.Connection = c.connection
			if c.env != "" {
				f.env["CONTAINER_CONNECTION"] = c.env
			}
			code, res, _ := runDoctor(t, f, s)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			wantFail(t, res, "machine", c.want...)
			if !strings.Contains(res["machine"].Fix, c.fix) {
				t.Errorf("fix %q does not mention %q", res["machine"].Fix, c.fix)
			}
			wantFail(t, res, "runtime", "not checked: machine")
			wantFail(t, res, "restart", "not checked")
			wantFail(t, res, "ports", "not checked: machine")
		})
	}
}

// The fix of a runtime that cannot be used depends on why.
func TestDoctorRuntimeFixes(t *testing.T) {
	podmanServer := `{"Platform":{"Name":"Podman Engine"},"Version":"5.0"}`
	cases := []struct {
		name  string
		rig   colocated
		fix   string
		check func(s *Settings)
	}{
		{"not Docker", colocated{engine: RuntimeDocker, over: []rule{{match: "version --format", out: podmanServer}}}, "Docker's own context", nil},
		{"Docker does not answer", colocated{engine: RuntimeDocker, over: []rule{{match: "version --format", err: errors.New("no daemon")}}}, "start Docker", nil},
		{"Podman on Linux does not answer", colocated{over: []rule{{match: "info --format", err: errors.New("no socket")}}}, "podman info", nil},
		{"Podman in a machine does not answer", colocated{goos: "windows", over: []rule{{match: "info --format", err: errors.New("no socket")}}}, "podman machine list", nil},
		{"no single rootless connection", colocated{goos: "windows", over: []rule{{match: "system connection list", out: `[]`}}}, "rootless connection", func(s *Settings) { s.Connection = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s := c.rig.build(t)
			if c.check != nil {
				c.check(&s)
				f.on("machine list", machineList)
			}
			_, res, _ := runDoctor(t, f, s)
			wantFail(t, res, "runtime")
			if !strings.Contains(res["runtime"].Fix, c.fix) {
				t.Errorf("fix %q does not mention %q", res["runtime"].Fix, c.fix)
			}
		})
	}
}

func TestDoctorContainerProblems(t *testing.T) {
	noRestart := func(name string, obj map[string]any) {
		if name == ContainerDB {
			obj["HostConfig"].(map[string]any)["RestartPolicy"] = map[string]any{"Name": "no"}
		}
	}
	cases := []struct {
		name  string
		rig   colocated
		check string
		want  []string
	}{
		{"an inspect output that is not JSON", colocated{over: []rule{{match: "inspect --type container " + ContainerDB, out: "not json"}}}, "containers", []string{"read the state of " + ContainerDB + " from its inspect output"}},
		{"a database without a health check", colocated{state: map[string]map[string]any{ContainerDB: doctorStateOf("running", 0, "")}}, "containers", []string{ContainerDB, "no health check"}},
		{"a migrate container that cannot be looked for", colocated{over: []rule{{match: "name=" + ContainerMigrate, err: errors.New("podman: not found")}}}, "containers", []string{"not found"}},
		{"a migrate container that cannot be inspected", colocated{migrate: doctorStateOf("exited", 0, ""), over: []rule{{match: "inspect --type container " + ContainerMigrate, err: errors.New("it vanished")}}}, "containers", []string{"it vanished"}},
		{"a restart policy that is not always", colocated{edit: noRestart}, "restart", []string{ContainerDB, `restart policy "no", want always`}},
		{"podman-restart.service that cannot be read", colocated{over: []rule{{match: "podman-restart.service", err: errors.New("no user manager")}}}, "restart", []string{"read podman-restart.service", "no user manager"}},
		{"podman-restart.service that does not exist", colocated{over: []rule{{match: "podman-restart.service", out: "LoadState=not-found\n"}}}, "restart", []string{"not installed"}},
		{"a database that cannot be inspected, so its restart cannot be checked", colocated{over: []rule{{match: "inspect --type container " + ContainerDB, err: errors.New("no such container")}}}, "restart", []string{"not checked: containers"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s := c.rig.build(t)
			code, res, _ := runDoctor(t, f, s)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			wantFail(t, res, c.check, c.want...)
		})
	}
}

func TestDoctorLingerProblems(t *testing.T) {
	cases := []struct {
		name string
		over []rule
		want string
	}{
		{"id fails", []rule{{match: "-un", err: errors.New("id: not found")}}, "read the linger setting"},
		{"id prints no name", []rule{{match: "-un", out: "\n"}}, "id printed no user name"},
		{"loginctl fails", []rule{{match: "show-user", err: errors.New("no logind")}}, "no logind"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s := colocated{over: c.over}.build(t)
			_, res, _ := runDoctor(t, f, s)
			wantFail(t, res, "linger", c.want)
		})
	}
}

func TestDoctorDockerPinProblems(t *testing.T) {
	cases := []struct {
		name string
		rig  colocated
		want string
	}{
		{"an inspect output that is not JSON", colocated{engine: RuntimeDocker, over: []rule{{match: "inspect --type container " + ContainerServe, out: "not json"}}}, ContainerServe},
		{"a database on another image", colocated{engine: RuntimeDocker, edit: func(name string, obj map[string]any) {
			if name == ContainerDB {
				obj["Config"].(map[string]any)["Image"] = "docker.io/library/postgres:99@sha256:" + strings.Repeat("d", 64)
			}
		}}, "want the pinned"},
		{"a service that is not pinned by a digest", colocated{engine: RuntimeDocker, edit: func(name string, obj map[string]any) {
			if name == ContainerServe {
				obj["Config"].(map[string]any)["Image"] = "ghcr.io/ignisdevne/suiteward:latest"
			}
		}}, "not pinned by a digest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s := c.rig.build(t)
			_, res, _ := runDoctor(t, f, s)
			wantFail(t, res, "pins", c.want)
		})
	}
}

func TestDoctorStatusNamesWhatIsMissing(t *testing.T) {
	cases := map[string]struct{ status, want string }{
		"no version":              {`{"schemaVersion":20,"tier":"co-located","jobs":{"discarded":0},"outbox":{"failed":0}}`, "no version"},
		"no jobs.discarded":       {`{"version":"v","schemaVersion":20,"tier":"co-located","jobs":{},"outbox":{"failed":0}}`, "no jobs.discarded"},
		"no outbox.failed":        {`{"version":"v","schemaVersion":20,"tier":"co-located","jobs":{"discarded":0},"outbox":{}}`, "no outbox.failed"},
		"several at once":         {`{}`, "no version, no schemaVersion, no tier, no jobs.discarded, no outbox.failed"},
		"a body that is not JSON": {"<html>", "GET /status on port"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f, s := colocated{status: c.status}.build(t)
			_, res, _ := runDoctor(t, f, s)
			wantFail(t, res, "status", c.want)
		})
	}
}

func TestHTTPGetRefusesAnAddressItCannotRequest(t *testing.T) {
	if _, err := httpGet(context.Background(), "http://[::1"); err == nil {
		t.Error("an address that is not a URL was requested")
	}
	if _, err := httpGet(context.Background(), "http://127.0.0.1:1/never"); err == nil {
		t.Error("a port nobody listens on answered")
	}
}

func TestFormatDiffsBoundsWhatItShows(t *testing.T) {
	var diffs []Diff
	for i := range 6 {
		diffs = append(diffs, Diff{Source: "unit", Field: fmt.Sprintf("field%d", i), Want: "w", Got: "g"})
	}
	got := formatDiffs(diffs)
	if !strings.Contains(got, "field0") || !strings.Contains(got, "field3") || strings.Contains(got, "field4") || !strings.HasSuffix(got, "and 2 more") {
		t.Errorf("formatDiffs = %q", got)
	}
}

// ---- isolated ----

func TestDoctorIsolatedProblemsThatNeedTheirOwnRig(t *testing.T) {
	t.Run("a unit that cannot be read", func(t *testing.T) {
		f, s := isolatedRig{over: []rule{{match: "suiteward-db.service", err: errors.New("no user manager")}}}.build(t)
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "units", "read suiteward-db.service", "no user manager")
		wantFail(t, res, "restart", "not checked: units")
	})
	t.Run("a unit that is not loaded", func(t *testing.T) {
		f, s := isolatedRig{over: []rule{{match: "suiteward.service", out: "LoadState=not-found\n"}}}.build(t)
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "units", "suiteward.service is not-found, not loaded")
	})
	t.Run("a unit that is a file the units directory cannot read", func(t *testing.T) {
		rig := isolatedRig{}
		f, s := rig.build(t)
		unit := filepath.Join(f.env["HOME"], ".config", "containers", "systemd", "suiteward.container")
		if err := os.Remove(unit); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(unit, 0o700); err != nil {
			t.Fatal(err)
		}
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "pins", "read the unit suiteward.container")
	})
	t.Run("the configuration directory comes from XDG_CONFIG_HOME when it is set", func(t *testing.T) {
		f, s := isolatedRig{}.build(t)
		f.env["XDG_CONFIG_HOME"] = filepath.Join(f.env["HOME"], "elsewhere")
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "pins")
	})
	t.Run("the runtime fails, so the running containers are not compared", func(t *testing.T) {
		f, s := isolatedRig{over: []rule{{match: "info --format", err: errors.New("no socket")}}}.build(t)
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "runtime")
		wantOK(t, res, "pins")
		if !strings.Contains(res["pins"].Detail, "were not compared because the runtime failed") {
			t.Errorf("the pins detail does not say the containers were not compared: %q", res["pins"].Detail)
		}
		wantFail(t, res, "memory", "not checked: runtime")
		wantFail(t, res, "metadata", "not checked: runtime")
	})
	t.Run("a running service that cannot be inspected", func(t *testing.T) {
		f, s := isolatedRig{over: []rule{{match: "inspect --type container " + ContainerServe, err: errors.New("no such container")}}}.build(t)
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "pins", ContainerServe, "no such container")
	})
	t.Run("the database that cannot be inspected for its memory limit", func(t *testing.T) {
		f, s := isolatedRig{over: []rule{{match: "inspect --type container " + ContainerDB, err: errors.New("no such container")}}}.build(t)
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "memory", "read the memory limit", "no such container")
	})
	t.Run("the network backend is read through the connection of the settings", func(t *testing.T) {
		f, s := isolatedRig{}.build(t)
		s.Connection = "pm"
		runDoctor(t, f, s)
		calls := f.find("NetworkBackend")
		if len(calls) != 1 || calls[0].args[0] != "--connection" || calls[0].args[1] != "pm" {
			t.Errorf("the network backend was not read through the connection: %v", calls)
		}
	})
}
