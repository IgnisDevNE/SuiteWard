package installer

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// The status of an instance in a Podman machine is read where this PC can: its address when /status answers from the PC (the port that the
// machine publishes does, on a Windows PC with WSL, as it was measured), and otherwise the command that reads it inside the machine.

const (
	hostStatus    = "http://127.0.0.1:27081/status"
	machineStatus = `podman machine ssh --username user podman-machine-default "curl -s ` + hostStatus + `"`
)

func hostRefuses(context.Context, string) ([]byte, error) {
	return nil, errors.New("connection refused")
}

// statusLineOf is what follows the prefix on the first line of text that starts with it.
func statusLineOf(text, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, prefix); ok {
			return rest
		}
	}
	return ""
}

func TestTheStatusRowInAPodmanMachineIsTheAddressWhenItAnswersFromThisPC(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refuses bool
		want    string
	}{{"the host answers", false, hostStatus}, {"the host does not answer", true, machineStatus}} {
		t.Run(tc.name, func(t *testing.T) {
			h := machineHarness(t)
			if tc.refuses {
				h.status = hostRefuses
			}
			c := newTermConsole(&bytes.Buffer{})
			env := h.newEnv()
			env.OpenScreen = c.open
			args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "podman", "--connection", "podman-machine-default", "--image", testImage, "--github-app-id", "none"}
			if err := Install(liveContext(t), args, env); err != nil {
				t.Fatal(err)
			}
			if got := statusLineOf(c.tail(), "Status: "); got != tc.want {
				t.Errorf("the Status row is %q, want %q:\n%s", got, tc.want, c.tail())
			}
		})
	}
}

// The line of the plain run says the same. It comes after the enrollment, because the service has just started when the install reaches the
// end and answers a little later, and the question is whether it answers.
func TestTheStatusLineOfAPlainInstallInAPodmanMachineIsTheAddressWhenItAnswersFromThisPC(t *testing.T) {
	for _, tc := range []struct {
		name    string
		refuses bool
		want    string
	}{{"the host answers", false, hostStatus}, {"the host does not answer", true, machineStatus}} {
		t.Run(tc.name, func(t *testing.T) {
			h := machineHarness(t)
			h.github = func(context.Context, githubEnv) (GitHubResult, error) { return githubResult(), nil }
			if tc.refuses {
				h.status = hostRefuses
			}
			if err := h.podman(append([]string{"--connection", "podman-machine-default"}, githubFlags...)...); err != nil {
				t.Fatal(err)
			}
			out := h.out.String()
			if got := statusLineOf(out, "Read the status of the service: "); got != tc.want {
				t.Errorf("the status line is %q, want %q:\n%s", got, tc.want, out)
			}
			read, enroll, check := strings.Index(out, "Read the status of the service"), strings.Index(out, "Setup was not run"), strings.Index(out, "Then check the instance")
			if enroll >= read || read >= check {
				t.Errorf("the status line is not between the enrollment and the doctor line (%d, %d, %d):\n%s", enroll, read, check, out)
			}
		})
	}
}
