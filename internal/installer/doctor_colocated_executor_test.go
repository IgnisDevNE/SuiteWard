package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// What `suiteward doctor` says of the executor on the co-located tier: the files an install leaves beside the token copies, which doctor finds from
// the default place of this user's token file (defaultTokenFile), and the answers of the operating system that keeps the executor running.

const coloProgramText = "the program that runs doctor"

// coloStatus is /status of a co-located service that has an executor token, which an executor last asked for work sinceLastSeen ago.
func coloStatus(sinceLastSeen time.Duration) string {
	seen := time.Now().Add(-sinceLastSeen).UTC().Format(time.RFC3339)
	base := okStatus
	return base[:len(base)-1] + fmt.Sprintf(`,"execution":{"enabled":true,"jobs":{"pending":0,"leased":0,"complete":3,"unavailable":0,"cancelled":0},"lastSeenAt":%q}}`, seen)
}

// coloExecFiles are the files of the executor of the fake's operating system, in the directories of the test.
type coloExecFiles struct{ token, dir, binary, pin string }

// coloTokenFile is the default token file of the fake computer, beside which colocated.build puts down the files of a healthy executor. The settings
// files of these tests are temporary ones, which doctor takes for another instance's, so a test that checks the executor names this token file as
// --token-file.
func coloTokenFile(t *testing.T, f *fakeEngine) string {
	t.Helper()
	file, err := defaultTokenFile(f.deps())
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func coloExecFilesOf(t *testing.T, f *fakeEngine) coloExecFiles {
	t.Helper()
	tokenFile, err := defaultTokenFile(f.deps())
	if err != nil {
		t.Fatal(err)
	}
	dir := executorDirOf(tokenFile)
	return coloExecFiles{token: executorTokenFile(tokenFile), dir: dir, binary: filepath.Join(dir, executorBinaryName(f.goos)), pin: filepath.Join(dir, "executor.pin")}
}

// healthyColocatedExecutor points the per-user directories at one of the test, puts down what an install of the executor leaves there, and has
// the fake operating system say that the executor is registered and runs. folder, when it is not empty, is a subfolder of that directory that the
// executor's files go in.
func healthyColocatedExecutor(t *testing.T, f *fakeEngine, folder string) {
	t.Helper()
	dir := filepath.Join(shortDir(t), folder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"APPDATA", "XDG_CONFIG_HOME", "HOME"} {
		t.Setenv(key, dir)
	}
	f.env["LOCALAPPDATA"], f.env["HOME"] = dir, dir
	program := filepath.Join(dir, "program")
	if err := os.WriteFile(program, []byte(coloProgramText), 0o755); err != nil { //nolint:gosec // a test program
		t.Fatal(err)
	}
	f.exe = program
	files := coloExecFilesOf(t, f)
	sum := sha256.Sum256([]byte(coloProgramText))
	made := map[string]string{
		files.token:  strings.Repeat("c", 64),
		files.binary: coloProgramText,
		files.pin:    hex.EncodeToString(sum[:]) + "  " + testReleaseAsset(f.goos) + "\n", // made from a release's sums
	}
	if f.goos == "windows" { // a registered task is this instance's when this instance made the two files of it; the task is the current definition
		plan, err := planExecutor(f.deps(), coloTokenFile(t, f), Settings{HTTPPort: DefaultHTTPPort, Runtime: RuntimePodman}, "")
		if err != nil {
			t.Fatal(err)
		}
		made[plan.wrapper], made[plan.taskXML] = plan.windowsWrapper(), string(plan.windowsTask(testSID))
	}
	for path, content := range made {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	switch f.goos {
	case "windows":
		f.on("Win32_Process", listingOf([]osProc{{pid: 4242, parent: 4, name: executorBinaryName("windows"), exe: files.binary, cmd: `"` + files.binary + `" execute`}}))
	case "darwin":
		f.on("=-u", "501\n").on("=print gui/501/"+executorLabel, "state = running\n")
	default:
		f.on("=--user show -p LoadState -p ActiveState -p SubState "+executorUnitName, "LoadState=loaded\nActiveState=active\nSubState=running\n")
	}
}

func TestDoctorColocatedExecutorIsHealthyOnEveryOperatingSystem(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			f, s := colocated{goos: goos}.build(t)
			code, res, _ := runDoctor(t, f, s)
			if code != 0 {
				t.Errorf("exit %d, results %+v", code, res)
			}
			wantOK(t, res, "executor.token", "executor.binary", "executor.lifecycle", "executor.running")
			// The reduced assurance of this tier is stated once, by the install's summary and the page that introduces it: not by each row.
			if d := res["executor.binary"].Detail; !strings.Contains(d, "matches the official release") || strings.Contains(d, "reduced assurance") {
				t.Errorf("the binary check must say that the program matches the release, and not repeat the caveat: %q", d)
			}
			if _, has := res["executor.loopback"]; has != (goos != "linux") {
				t.Errorf("the loopback of a Podman machine is checked on Windows and macOS only: %v", has)
			}
		})
	}
}

// The task of an earlier build (the wrapper run by itself, with a window and RestartOnFailure) is registered by this instance and is not the one this
// build makes: doctor reads it as a difference that `install --upgrade` fixes, and still says that the executor runs.
func TestDoctorColocatedWindowsTaskOfAnEarlierBuildIsADifferenceThatAnUpgradeFixes(t *testing.T) {
	f, s := colocated{goos: "windows"}.build(t)
	plan, err := planExecutor(f.deps(), coloTokenFile(t, f), Settings{HTTPPort: DefaultHTTPPort, Runtime: RuntimePodman}, "")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, plan.taskXML, string(legacyTask(plan)))
	code, res, _ := runDoctor(t, f, s)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	wantFail(t, res, "executor.lifecycle", "not the one this version")
	// It is an older definition or one that was changed by hand: the text does not claim what only the pre-F7 definition did.
	if d := res["executor.lifecycle"].Detail; strings.Contains(d, "opens a window") {
		t.Errorf("the detail %q assumes the old definition", d)
	}
	if !strings.Contains(res["executor.lifecycle"].Fix, "install --upgrade") {
		t.Errorf("the fix %q must name the upgrade", res["executor.lifecycle"].Fix)
	}
	wantOK(t, res, "executor.running")
}

// A test runner that an earlier build made in a folder with a percent sign is checked like any other: the refusal is for setting up a new one, and
// its message (use another folder) is wrong for a test runner that is there.
func TestDoctorChecksAnExistingWindowsExecutorInAFolderWithAPercentSign(t *testing.T) {
	f, s := colocated{goos: "windows", folder: "50%off"}.build(t)
	code, res, _ := runDoctor(t, f, s)
	if code != 0 {
		t.Errorf("exit %d, results %+v", code, res)
	}
	wantOK(t, res, "executor.token", "executor.binary", "executor.lifecycle", "executor.running")
}

// With none registered there, the install would refuse the folder too: doctor says why, once, and checks no other row of the test runner, because
// the fix of each would be an install that cannot work.
func TestDoctorSaysThatNoWindowsExecutorCanBeSetUpInAFolderWithAPercentSign(t *testing.T) {
	notRegistered := rule{match: "/Query /TN", err: errors.New("error: the system cannot find the file specified")}
	f, s := colocated{goos: "windows", folder: "50%off", over: []rule{notRegistered}}.build(t)
	code, res, names := runDoctor(t, f, s)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	wantFail(t, res, "executor.lifecycle", "percent sign")
	for _, name := range names {
		if strings.HasPrefix(name, "executor.") && name != "executor.lifecycle" {
			t.Errorf("%s is checked beside a folder that no install accepts: %+v", name, res[name])
		}
	}
}

// The test runner that is registered may be another instance's: it holds the one place of the computer, but this instance's folder is one that no
// install accepts, so removing the other instance would free a place that this instance's install refuses. Doctor says why no install can set one up,
// once, and does not send the person to remove another instance for nothing.
func TestDoctorDoesNotAskToRemoveAnotherInstanceWhenThisFolderHasAPercentSign(t *testing.T) {
	f, s := colocated{goos: "windows", folder: "50%off"}.build(t)
	files := coloExecFilesOf(t, f)
	removeFile(t, filepath.Join(files.dir, executorWrapperName)) // a registered task whose two files this instance did not make is another instance's
	removeFile(t, filepath.Join(files.dir, "task.xml"))
	code, res, names := runDoctor(t, f, s)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	wantFail(t, res, "executor.lifecycle", "percent sign")
	if fix := res["executor.lifecycle"].Fix; strings.Contains(fix, "another instance") || strings.Contains(fix, "uninstall") {
		t.Errorf("the fix %q sends the person to remove another instance, and the install that follows is refused", fix)
	}
	for _, name := range names {
		if strings.HasPrefix(name, "executor.") && name != "executor.lifecycle" {
			t.Errorf("%s is checked beside a folder that no install accepts: %+v", name, res[name])
		}
	}
}

// An install would refuse the folder of such a test runner too, so the fix of a row that an install fixes elsewhere ("install --upgrade") cannot be
// the install: the way out is to remove the test runner, which the uninstall does, and set up another in a folder without a percent sign.
func TestDoctorSendsATestRunnerInAFolderWithAPercentSignThroughAnUninstall(t *testing.T) {
	f, s := colocated{goos: "windows", folder: "50%off"}.build(t)
	files := coloExecFilesOf(t, f)
	writeFile(t, files.binary, "the previous release")
	sum := sha256.Sum256([]byte("the previous release"))
	writeFile(t, files.pin, hex.EncodeToString(sum[:])+"  suiteward-executor.exe\n")
	removeFile(t, files.token) // an install makes the token copy again whatever becomes of the test runner, so that row keeps the install as its fix
	_, res, _ := runDoctor(t, f, s)
	wantFail(t, res, "executor.binary", "different version")
	fix := res["executor.binary"].Fix
	if !strings.Contains(fix, "suiteward uninstall") || !strings.Contains(fix, "percent sign") || strings.Contains(fix, "--upgrade") {
		t.Errorf("the fix is %q, want the uninstall and a folder without a percent sign, and no upgrade that the install would refuse", fix)
	}
	wantFail(t, res, "executor.token", files.token)
	if fix := res["executor.token"].Fix; !strings.Contains(fix, "install --upgrade") || strings.Contains(fix, "uninstall") {
		t.Errorf("the fix of the token row is %q, want the install", fix)
	}
}

func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorColocatedExecutorFailures(t *testing.T) {
	const userUnit = "=--user show -p LoadState -p ActiveState -p SubState " + executorUnitName
	cases := []struct {
		name  string
		goos  string
		edit  func(t *testing.T, files coloExecFiles)
		over  []rule
		check string
		want  func(files coloExecFiles) []string
		fix   string
	}{
		{"no token copy", "linux", func(t *testing.T, files coloExecFiles) { removeFile(t, files.token) }, nil, "executor.token", func(files coloExecFiles) []string { return []string{files.token} }, "install --upgrade"},
		{"no binary", "linux", func(t *testing.T, files coloExecFiles) { removeFile(t, files.binary) }, nil, "executor.binary", func(files coloExecFiles) []string { return []string{files.binary} }, "install"},
		{"a binary that changed", "linux", func(t *testing.T, files coloExecFiles) { writeFile(t, files.binary, "tampered") }, nil, "executor.binary", func(coloExecFiles) []string { return []string{"changed"} }, "install"},
		{"another build than this program", "linux", func(t *testing.T, files coloExecFiles) {
			writeFile(t, files.binary, "the previous release")
			sum := sha256.Sum256([]byte("the previous release"))
			writeFile(t, files.pin, hex.EncodeToString(sum[:])+"  suiteward-executor\n")
		}, nil, "executor.binary", func(coloExecFiles) []string { return []string{"different version"} }, "install --upgrade"},
		{"not registered on Linux", "linux", nil, []rule{{match: userUnit, out: "LoadState=not-found\nActiveState=inactive\nSubState=dead\n"}}, "executor.lifecycle", func(coloExecFiles) []string { return []string{"systemd user unit"} }, "install"},
		{"not registered on Windows", "windows", nil, []rule{{match: "/Query /TN", err: errors.New("error: the system cannot find the file specified")}}, "executor.lifecycle", func(coloExecFiles) []string { return []string{"scheduled task"} }, "install"},
		{"not running on Linux", "linux", nil, []rule{{match: userUnit, out: "LoadState=loaded\nActiveState=inactive\nSubState=dead\n"}}, "executor.running", func(coloExecFiles) []string { return []string{"not running"} }, "systemctl --user start suiteward-executor.service"},
		{"not running on Windows", "windows", nil, []rule{{match: "Win32_Process", out: listingOf(nil)}}, "executor.running", func(coloExecFiles) []string { return []string{"not running"} }, "schtasks /Run"},
		{"not running on Windows while only another instance's executor runs", "windows", nil, []rule{{match: "Win32_Process", out: listingOf([]osProc{{pid: 9, name: "suiteward-executor.exe", exe: `C:\other\executor\suiteward-executor.exe`, cmd: `"C:\other\executor\suiteward-executor.exe" execute`}})}}, "executor.running", func(coloExecFiles) []string { return []string{"not running"} }, "schtasks /Run"},
		{"not running on macOS", "darwin", nil, []rule{{match: "=print gui/501/" + executorLabel, out: "state = waiting\n"}}, "executor.running", func(coloExecFiles) []string { return []string{"not running"} }, "launchctl kickstart"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s := colocated{goos: c.goos, over: c.over}.build(t)
			files := coloExecFilesOf(t, f)
			if c.edit != nil {
				c.edit(t, files)
			}
			code, res, _ := runDoctor(t, f, s)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			wantFail(t, res, c.check, c.want(files)...)
			if !strings.Contains(res[c.check].Fix, c.fix) {
				t.Errorf("fix %q does not mention %q", res[c.check].Fix, c.fix)
			}
		})
	}
}

func TestDoctorColocatedServiceWithoutTheExecutorTokenIsOneOfAnEarlierRelease(t *testing.T) {
	f, s := colocated{edit: func(name string, obj map[string]any) {
		if name == ContainerServe {
			withoutExecutorToken(obj)
		}
	}}.build(t)
	_, res, _ := runDoctor(t, f, s)
	wantFail(t, res, "executor.token", "service")
	if !strings.Contains(res["executor.token"].Fix, "install --upgrade") {
		t.Errorf("fix %q", res["executor.token"].Fix)
	}
}

func TestDoctorColocatedExecutorRunningNeedsTheServiceToHaveSeenIt(t *testing.T) {
	cases := map[string]struct {
		status string
		want   string
	}{
		"seen long ago":            {coloStatus(10 * time.Minute), "ago"},
		"no execution object":      {okStatus, "added the test runner"},
		"the service has no token": {okStatus[:len(okStatus)-1] + `,"execution":{"enabled":false,"jobs":{"pending":0},"lastSeenAt":"2026-10-27T00:00:00Z"}}`, "EXECUTOR_TOKEN"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f, s := colocated{status: c.status}.build(t)
			code, res, _ := runDoctor(t, f, s)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			wantFail(t, res, "executor.running", c.want)
		})
	}
}

func TestDoctorColocatedLoopbackOfAPodmanMachineThatDoesNotAnswerFromThisComputer(t *testing.T) {
	for _, goos := range []string{"windows", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			f, s := colocated{goos: goos}.build(t)
			path := filepath.Join(t.TempDir(), "settings.yaml")
			if err := SaveSettings(path, s); err != nil {
				t.Fatal(err)
			}
			unreachable := func(context.Context, string) ([]byte, error) { return nil, errors.New("connection refused") }
			results := doctorChecks(context.Background(), f.deps(), unreachable, path, coloTokenFile(t, f))
			var got doctorResult
			for _, r := range results {
				if r.Name == "executor.loopback" {
					got = r
				}
			}
			if got.OK || !strings.Contains(got.Detail, "127.0.0.1:"+strconv.Itoa(s.HTTPPort)) || !strings.Contains(got.Detail, "no-executor") {
				t.Errorf("executor.loopback = %+v, want a failure that names the port and says that the runs read no-executor", got)
			}
			if !strings.Contains(got.Fix, "podman machine") {
				t.Errorf("the fix must name the machine: %q", got.Fix)
			}
		})
	}
}

// In a folder that an install refuses, an install does not set up the test runner, so no fix promises that it does: the rows that an install still
// makes right (the token copy, the Go image, the service's token) say what to run and no more, and a test runner that runs but is not seen by the
// service has no install to fix it, so its fix is the way out of the folder.
func TestDoctorPromisesNoTestRunnerFromAnInstallInAFolderWithAPercentSign(t *testing.T) {
	const percent = "50%off"
	noToken := okStatus[:len(okStatus)-1] + `,"execution":{"enabled":false,"jobs":{"pending":0},"lastSeenAt":"2026-10-27T00:00:00Z"}}`
	for name, c := range map[string]struct {
		with colocated
		edit func(t *testing.T, files coloExecFiles)
		row  string
		want string // what the fix says
	}{
		"no token copy":      {colocated{goos: "windows", folder: percent}, func(t *testing.T, files coloExecFiles) { removeFile(t, files.token) }, "executor.token", "install"},
		"no Go image":        {colocated{goos: "windows", folder: percent, over: []rule{{match: "image inspect " + goprofile.Image, err: errors.New("no such image")}}}, nil, "executor.image", "install"},
		"the service has no": {colocated{goos: "windows", folder: percent, status: noToken}, nil, "executor.running", "install --upgrade"},
		"not seen":           {colocated{goos: "windows", folder: percent, status: coloStatus(10 * time.Minute)}, nil, "executor.running", "suiteward uninstall"},
	} {
		t.Run(name, func(t *testing.T) {
			f, s := c.with.build(t)
			if c.edit != nil {
				c.edit(t, coloExecFilesOf(t, f))
			}
			_, res, _ := runDoctor(t, f, s)
			fix := res[c.row].Fix
			if res[c.row].OK || !strings.Contains(fix, c.want) || strings.Contains(fix, "sets up the test runner") || strings.Contains(fix, "same token and port") {
				t.Errorf("%s: %+v, want a failure whose fix says %q and does not promise that an install sets up the test runner", c.row, res[c.row], c.want)
			}
		})
	}
}
