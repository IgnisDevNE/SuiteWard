package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Guards for what the SHA256SUMS check already handles: a file that lists the program twice or not at all, a file with Windows line endings, and the
// name of a Windows release asset.

func TestPrepareHostRefusesSumsThatListTheProgramTwiceOrNotAtAll(t *testing.T) {
	for name, text := range map[string]func(sum string) string{
		"twice": func(sum string) string {
			return sum + "  suiteward_v0.4.0_linux_amd64\n" + sum + "  suiteward_v0.4.1_linux_arm64\n"
		},
		"an empty file": func(string) string { return "" },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHostFake()
			h.exe = writeProgram(t)
			h.files[execSums] = text(sha256File(t, h.exe))
			_, err := runHost(t, h, map[string]string{"metadata-rule": "no", "sha256sums": execSums}, false, "")
			if err == nil || !strings.Contains(err.Error(), "not listed exactly once") {
				t.Fatalf("want a refusal that says the program is not listed exactly once, got %v", err)
			}
			if h.has("install", "-m 0755 -o root -g root") {
				t.Errorf("the binary was installed: %v", h.lines())
			}
		})
	}
}

func TestPrepareHostReadsSumsWithWindowsLineEndings(t *testing.T) {
	h := newHostFake()
	h.exe = writeProgram(t)
	line := sha256File(t, h.exe) + "  suiteward_v0.4.0_linux_amd64"
	h.files[execSums] = strings.Repeat("a", 64) + "  install.sh\r\n" + line + "\r\n"
	if _, err := runHost(t, h, map[string]string{"metadata-rule": "no", "sha256sums": execSums}, false, ""); err != nil {
		t.Fatal(err)
	}
	if got := writeOf(h, execPinFile); got != line+"\n" {
		t.Errorf("the pin is %q, want the release's line without the carriage return", got)
	}
}

func TestInstallOnWindowsPinsTheExecutorToTheExeLineOfTheSums(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	sums := writeSums(t, h, "suiteward_v0.4.0_windows_amd64.exe")
	if err := h.podman("--sha256sums", sums); err != nil {
		t.Fatal(err)
	}
	if got, want := h.readHome("executor/executor.pin"), sha256File(t, h.program())+"  suiteward_v0.4.0_windows_amd64.exe\n"; got != want {
		t.Errorf("the pin is %q, want %q", got, want)
	}
}

func TestInstallOnWindowsRefusesSumsThatListOnlyAnotherSystemsBinary(t *testing.T) {
	h := machineHarnessOn(t, "windows")
	sums := writeSums(t, h, sumsAsset) // the Linux name
	err := h.podman("--sha256sums", sums)
	if err == nil || !strings.Contains(err.Error(), "windows binary") {
		t.Fatalf("want a refusal that names a windows binary, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(h.dir, "executor")); !os.IsNotExist(statErr) {
		t.Errorf("the executor was set up: %v", statErr)
	}
}

func TestInstallReadsSumsWithWindowsLineEndings(t *testing.T) {
	h := newHarness(t, "linux")
	sums := filepath.Join(t.TempDir(), "SHA256SUMS")
	text := strings.Repeat("a", 64) + "  install.sh\r\n" + sha256File(t, h.program()) + "  " + sumsAsset + "\r\n"
	if err := os.WriteFile(sums, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.podman("--sha256sums", sums); err != nil {
		t.Fatal(err)
	}
	if got, want := h.readHome("executor/executor.pin"), sha256File(t, h.program())+"  "+sumsAsset+"\n"; got != want {
		t.Errorf("the pin is %q, want %q", got, want)
	}
}
