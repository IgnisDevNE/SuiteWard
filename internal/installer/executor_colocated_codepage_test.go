package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// schtasks answers in the console code page when its output is piped: a profile path with a letter outside ASCII (a user called João) comes back
// with that letter as one byte that is not UTF-8. The installer decides whether a registered task is its own without reading that answer, so an
// install of the same instance still updates the executor, and an uninstall still removes it.
func TestInstallKnowsItsOwnScheduledTaskWhenTheProfilePathIsNotASCIIAndTheAnswerIsInACodePage(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	h.dir = filepath.Join(h.dir, "João")
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	newer := filepath.Join(t.TempDir(), "suiteward.exe")
	if err := os.WriteFile(newer, []byte("the next release"), 0o600); err != nil {
		t.Fatal(err)
	}
	again := machineHarnessOn(t, "windows")
	again.dir = h.dir
	again.exe = newer
	again.have(kindSecret, SecretMCPAuth, SecretExecutorAuth)
	wrapper := strings.ReplaceAll(filepath.Join(h.executorDir(), "run.cmd"), "ã", string([]byte{0xc6})) // CP850
	again.first("/Query /TN SuiteWard Executor", "")
	again.first("/Query /TN SuiteWard Executor /XML", "<Actions><Exec><Command>"+wrapper+"</Command></Exec></Actions>")
	again.f.on("Win32_Process", listingOf(nil))
	if err := again.podman(); err != nil {
		t.Fatal(err)
	}
	if got := h.readHome("executor/suiteward-executor.exe"); got != "the next release" {
		t.Errorf("the executor still has %q: the install took its own task for another instance's\n%s", got, again.out.String())
	}

	un := machineHarnessOn(t, "windows")
	un.dir = h.dir
	podmanStackExists(un)
	un.first("/Query /TN SuiteWard Executor", "")
	un.first("/Query /TN SuiteWard Executor /XML", "<Actions><Exec><Command>"+wrapper+"</Command></Exec></Actions>")
	un.f.on("Win32_Process", listingOf(nil))
	if err := un.uninstall("--runtime", "podman", "--connection", "pm", "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(un.f.find("/Delete /TN SuiteWard Executor")) == 0 {
		t.Errorf("the uninstall kept the executor as another instance's: %v\n%s", un.f.calls, un.out.String())
	}
}
