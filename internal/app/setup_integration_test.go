//go:build integration

package app

import (
	"encoding/base32"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

// codeFromOutput answers setup as an owner would: Enter at the confirmation,
// then the code of the secret on the second line of out, as an authenticator
// app computes it. setup prints the secret before it reads the code.
type codeFromOutput struct {
	out   *logBuffer
	reads int
}

func (c *codeFromOutput) Read(p []byte) (int, error) {
	c.reads++
	if c.reads == 1 {
		return copy(p, "\n"), nil
	}
	if c.reads > 2 {
		return 0, io.EOF
	}
	// setup reads ahead, so the secret may not be printed yet: wait for it, as an owner would.
	var lines []string
	for deadline := time.Now().Add(30 * time.Second); len(lines) < 3 && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		lines = strings.Split(c.out.String(), "\n") // two lines and the empty rest after the last newline
	}
	if len(lines) < 3 {
		return 0, io.ErrNoProgress
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(lines[1])
	if err != nil {
		return 0, err
	}
	code, err := totp.Code(secret, totp.Step(time.Now()))
	if err != nil {
		return 0, err
	}
	return copy(p, code+"\n"), nil
}

func TestSetupEnrollsAndServeReportsTheOwnerAndTheTier(t *testing.T) {
	databaseURL := newDatabase(t)
	fake := newSetupGitHub(t)
	vars := serveVars(t, databaseURL)
	for name, value := range fake.vars() {
		vars[name] = value
	}

	// Before setup there is no owner.
	before := startRun(t, vars)
	before.waitReady()
	if status := before.status(); status["owner"] != nil || status["tier"] != "co-located" || status["tierNote"] != nil {
		t.Fatalf("GET /status before setup = %v; want the default tier and no owner", status)
	}
	if code := before.stop(); code != 0 {
		t.Fatalf("a clean stop exited %d:\n%s", code, before.logs.String())
	}

	var stdout, stderr logBuffer
	env := testEnv(vars)
	env.Stdin = &codeFromOutput{out: &stdout}
	args := []string{"setup", "--owner", "octo", "--test-check", setupWidgetsRepo + "=" + setupTestCheck}
	if code := Run(t.Context(), args, env, &stdout, &stderr); code != 0 {
		t.Fatalf("setup exited %d:\n%s", code, stderr.String())
	}
	if lines := strings.Split(strings.TrimSpace(stdout.String()), "\n"); len(lines) != 2 || !strings.HasPrefix(lines[0], "otpauth://totp/SuiteWard:octo%20%28co-located%20") {
		t.Fatalf("stdout = %q; want the URI and the secret", stdout.String())
	}
	if got := scalar(t, databaseURL, "SELECT result FROM github_protection_results WHERE repository_id = $1", setupRepoID); got != "findings" {
		t.Fatalf("stored protection result = %q; want findings (no rules in the fake)", got)
	}
	for what, secret := range map[string]string{"the database password": password(t, databaseURL), "a token": setupReadAuth} {
		if strings.Contains(stdout.String()+stderr.String(), secret) {
			t.Errorf("setup printed %s:\n%s\n%s", what, stdout.String(), stderr.String())
		}
	}

	// The configured tier is now isolated while the enrollment says co-located.
	vars["SUITEWARD_INSTALLATION_TIER"] = "isolated"
	after := startRun(t, vars)
	after.waitReady()
	status := after.status()
	owner, _ := status["owner"].(map[string]any)
	if owner["login"] != "octo" || owner["accountId"] != float64(setupOwnerID) || owner["enrolledAt"] == nil {
		t.Fatalf("GET /status owner = %v; want octo, %d and an enrollment time", status["owner"], setupOwnerID)
	}
	if status["tier"] != "co-located" || status["tierNote"] == nil {
		t.Fatalf("GET /status tier = %v, note %v; want co-located with a note to re-run setup", status["tier"], status["tierNote"])
	}
	// The entry names the instance by the id /status shows.
	if want := "%20" + shortInstance(scalar(t, databaseURL, "SELECT instance_id FROM suiteward_instance")) + "%29?"; status["instance"] == nil || !strings.Contains(stdout.String(), want) || !strings.Contains(want, status["instance"].(string)) {
		t.Fatalf("GET /status instance = %v; want it in the entry name (%s) of %q", status["instance"], want, stdout.String())
	}
	if code := after.stop(); code != 0 {
		t.Fatalf("a clean stop exited %d:\n%s", code, after.logs.String())
	}
}
