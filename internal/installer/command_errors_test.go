package installer

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"net"
	"os"
	"strings"
	"testing"
)

func TestOSEnvIsUsableAndLoopbackFreeSeesBusyPorts(t *testing.T) {
	env := OSEnv(io.Discard)
	if err := env.validate(); err != nil {
		t.Fatalf("the process's environment is not valid: %v", err)
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("this machine does not let a test listen on loopback: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := loopbackFree(port); err == nil {
		t.Errorf("port %d is held by this test, and loopbackFree called it free", port)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := loopbackFree(port); err != nil {
		t.Errorf("port %d was released, and loopbackFree refused it: %v", port, err)
	}
}

func TestEnvValidateNamesWhatIsMissing(t *testing.T) {
	good := newHarness(t, "linux").newEnv()
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	noSleep := good
	noSleep.Sleep = nil
	noPorts := good
	noPorts.PortFree = nil
	noExec := good
	noExec.Deps.Exec = nil
	for name, env := range map[string]Env{"no Sleep": noSleep, "no PortFree": noPorts, "no Exec": noExec} {
		if err := env.validate(); err == nil {
			t.Errorf("%s: an environment without what the installer needs was accepted", name)
		}
	}
	if err := noSleep.validate(); err == nil || !strings.Contains(err.Error(), "PortFree and Sleep") {
		t.Errorf("the refusal does not name PortFree and Sleep: %v", err)
	}
	// Every command validates before it reads a flag.
	for name, run := range map[string]func() error{
		"install":      func() error { return Install(context.Background(), nil, noSleep) },
		"install host": func() error { return installHost(context.Background(), nil, noSleep) },
		"uninstall":    func() error { return Uninstall(context.Background(), nil, noSleep) },
	} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "PortFree and Sleep") {
			t.Errorf("%s with an invalid environment = %v", name, err)
		}
	}
}

// Main turns every outcome into an exit code: 0 for success and --help, 2 for a command line that cannot be read, 1 for the rest.
func TestMainExitCodes(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		code   int
		stderr string
		stdout string
	}{
		{"no command", nil, 2, "unknown command", ""},
		{"an unknown command", []string{"frobnicate"}, 2, `unknown command "frobnicate"`, ""},
		{"install --help", []string{"install", "--help"}, 0, "", "-runtime"},
		{"install host --help", []string{"install", "host", "--help"}, 0, "", "-metadata-rule"},
		{"doctor --help", []string{"doctor", "--help"}, 0, "", "-settings"},
		{"a flag that does not exist", []string{"install", "--nope"}, 2, "flag provided but not defined", ""},
		{"a doctor flag that does not exist", []string{"doctor", "--nope"}, 2, "flag provided but not defined", ""},
		{"a stray argument", []string{"install", "now"}, 2, `unexpected argument "now"`, ""},
		{"install host off Linux", []string{"install", "host"}, 1, "install host prepares an AlmaLinux host", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "darwin")
			var stderr bytes.Buffer
			code := Main(context.Background(), tc.args, h.newEnv(), &stderr)
			if code != tc.code {
				t.Errorf("exit %d, want %d (stderr %q)", code, tc.code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.stderr) {
				t.Errorf("stderr %q lacks %q", stderr.String(), tc.stderr)
			}
			if !strings.Contains(h.out.String(), tc.stdout) {
				t.Errorf("stdout %q lacks %q", h.out.String(), tc.stdout)
			}
			if tc.code == 2 && !strings.Contains(stderr.String(), "usage: suiteward install") {
				t.Errorf("a usage error does not print the usage: %q", stderr.String())
			}
		})
	}
}

func TestInstallRefusesWhatItCannotReadBeforeChangingAnything(t *testing.T) {
	t.Run("an answers file that is not there", func(t *testing.T) {
		h := newHarness(t, "linux")
		err := h.podman("--answers", h.dir+"/nothing.yaml")
		if err == nil || !strings.Contains(err.Error(), "read the answers file") {
			t.Fatalf("install = %v", err)
		}
		h.mustNotMutate()
	})
	t.Run("a console that cannot ask", func(t *testing.T) {
		h := newHarness(t, "linux")
		env := h.newEnv()
		env.Term.IsTerminal = true // and no way to read a password
		err := Install(context.Background(), []string{"--settings", h.settingsPath(), "--runtime", "podman"}, env)
		if err == nil || !strings.Contains(err.Error(), "password reader") {
			t.Fatalf("install = %v", err)
		}
		h.mustNotMutate()
	})
	t.Run("no place for the settings", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("AppData", "")
		t.Setenv("HOME", "")
		h := newHarness(t, "linux")
		err := Install(context.Background(), []string{"--runtime", "podman", "--token-file", h.tokenPath(), "--image", testImage}, h.newEnv())
		if err == nil || !strings.Contains(err.Error(), "user configuration directory") {
			t.Fatalf("install = %v", err)
		}
		h.mustNotMutate()
	})
	t.Run("settings that are not valid", func(t *testing.T) {
		h := newHarness(t, "linux")
		if err := os.WriteFile(h.settingsPath(), []byte("tier: wrong\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := h.podman()
		if err == nil || !strings.Contains(err.Error(), "tier") {
			t.Fatalf("install = %v", err)
		}
		h.mustNotMutate()
		if raw, _ := os.ReadFile(h.settingsPath()); string(raw) != "tier: wrong\n" {
			t.Errorf("the invalid settings were overwritten: %q", raw)
		}
	})
	t.Run("a tier that does not exist", func(t *testing.T) {
		h := newHarness(t, "linux")
		err := h.podman("--tier", "nonsense")
		if err == nil || !strings.Contains(err.Error(), `tier "nonsense" is not`) {
			t.Fatalf("install = %v", err)
		}
		h.mustNotMutate()
	})
	t.Run("an upgrade of a host that has no settings", func(t *testing.T) {
		h := newHarness(t, "linux")
		err := h.install("--upgrade", "--yes")
		if err == nil || !strings.Contains(err.Error(), "no settings file, so the tier cannot be told") {
			t.Fatalf("install = %v", err)
		}
		h.mustNotMutate()
	})
}

func TestInstallHostRefusesWhatItCannotRead(t *testing.T) {
	h := newHarness(t, "linux")
	run := func(args ...string) error { return installHost(context.Background(), args, h.newEnv()) }
	if err := run("--nope"); !errors.Is(err, ErrUsage) {
		t.Errorf("a flag that does not exist = %v", err)
	}
	if err := run("--help"); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("--help = %v", err)
	}
	if err := run("--answers", h.dir+"/nothing.yaml"); err == nil || !strings.Contains(err.Error(), "read the answers file") {
		t.Errorf("an answers file that is not there = %v", err)
	}
	env := h.newEnv()
	env.Term.IsTerminal = true
	if err := installHost(context.Background(), nil, env); err == nil || !strings.Contains(err.Error(), "password reader") {
		t.Errorf("a console that cannot ask = %v", err)
	}
	if len(h.f.calls) != 0 {
		t.Errorf("a command ran for an install host that was refused: %v", h.f.lines())
	}
}

func TestParseRefusesWhatItCannotReadAndSaysWhere(t *testing.T) {
	fs := newFlagSet("x")
	fs.Bool("a", false, "")
	if _, err := parse(fs, []string{"--a", "stray"}, io.Discard); !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), `unexpected argument "stray"`) {
		t.Errorf("parse = %v", err)
	}
}

func TestConfirmDockerNeedsAnExplicitYes(t *testing.T) {
	cases := []struct {
		name  string
		flags map[string]string
		input string
		tty   bool
		want  string
	}{
		{"a no", map[string]string{"accept-docker": "no"}, "", false, "was not confirmed"},
		{"an answer that is not yes or no", map[string]string{"accept-docker": "maybe"}, "", false, "was not confirmed"},
		{"nothing given and nothing to ask", nil, "", false, "was not confirmed"},
		{"a terminal that is closed", nil, "", true, "read the answer for"},
		{"a typed no", nil, "no\n", true, "was not confirmed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term, out := fakeTerminal(tc.tty, tc.input)
			r := newTestResolver(t, tc.flags, nil, term)
			err := confirmDocker(r)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("confirmDocker = %v, want an error containing %q", err, tc.want)
			}
			if !strings.Contains(out.String(), "Docker is allowed on this PC") {
				t.Errorf("the warning was not printed:\n%s", out.String())
			}
		})
	}
	term, _ := fakeTerminal(false, "")
	if err := confirmDocker(newTestResolver(t, map[string]string{"accept-docker": "yes"}, nil, term)); err != nil {
		t.Errorf("an explicit yes was refused: %v", err)
	}
}
