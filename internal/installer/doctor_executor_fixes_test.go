package installer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// What doctor tells the co-located user to do about the executor is that tier's own (an install of this program, never the administrator's host
// step), it checks the executor even when /status reports a failure of another kind, and it finds the executor of an instance that was installed
// with --token-file.

func TestDoctorColocatedExecutorFixesNeverSendToTheHostStep(t *testing.T) {
	cases := map[string]struct{ status, fix string }{
		"seen long ago":            {coloStatus(10 * time.Minute), "install"},
		"the service has no token": {okStatus[:len(okStatus)-1] + `,"execution":{"enabled":false,"jobs":{"pending":0},"lastSeenAt":"2026-10-27T00:00:00Z"}}`, "install --upgrade"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f, s := colocated{status: c.status}.build(t)
			_, res, _ := runDoctor(t, f, s)
			wantFail(t, res, "executor.running")
			fix := res["executor.running"].Fix
			if strings.Contains(fix, "install host") || strings.Contains(fix, "sudo") || strings.Contains(fix, "administrator") {
				t.Errorf("the fix sends this tier to the host step of the isolated tier: %q", fix)
			}
			if !strings.Contains(fix, c.fix) {
				t.Errorf("the fix %q does not mention %q", fix, c.fix)
			}
		})
	}
}

func TestDoctorChecksTheExecutorWhenStatusReportsDiscardedJobs(t *testing.T) {
	status := strings.Replace(coloStatus(5*time.Second), `"discarded":0`, `"discarded":2`, 1)
	f, s := colocated{status: status}.build(t)
	_, res, _ := runDoctor(t, f, s)
	wantFail(t, res, "status", "discarded")
	wantOK(t, res, "executor.running") // the execution object is there whatever else /status says
}

func TestDoctorFindsTheExecutorBesideTheTokenFileOfTheInstance(t *testing.T) {
	f, s := colocated{checkOut: doctorNoGitHub}.build(t)
	files := coloExecFilesOf(t, f)
	custom := filepath.Join(shortDir(t), "mcp-token")
	for from, to := range map[string]string{files.dir: executorDirOf(custom), files.token: executorTokenFile(custom)} {
		if err := os.Rename(from, to); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	env := Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &out}}
	if code := Main(liveContext(t), []string{"doctor", "--json", "--settings", path, "--token-file", custom}, env, &stderr); code == 2 {
		t.Fatalf("doctor refused --token-file: %s", stderr.String())
	}
	var results []doctorResult
	if err := json.Unmarshal(out.Bytes(), &results); err != nil {
		t.Fatalf("the output is not the JSON of the results: %v\n%s", err, out.String())
	}
	got := map[string]doctorResult{}
	for _, r := range results {
		got[r.Name] = r
	}
	wantOK(t, got, "executor.token", "executor.binary")
}
