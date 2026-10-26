//go:build windows

package ui

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// notAConsole is a file: no console mode can be asked of it.
func notAConsole(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	})
	return f
}

// A file is not a console: asking for virtual-terminal processing must fail, so that OpenOS falls back to plain output.
func TestEnableVTRejectsANonConsole(t *testing.T) {
	if _, err := enableVT(notAConsole(t)); err == nil {
		t.Fatal("enableVT accepted a file")
	}
}

// The same for per-key input: Select then asks by number.
func TestEnableKeysRejectsANonConsole(t *testing.T) {
	if _, err := enableKeys(notAConsole(t)); err == nil {
		t.Fatal("enableKeys accepted a file")
	}
}

// inputRecord is the INPUT_RECORD of a key event: what the console queues for a key that nobody is reading.
type inputRecord struct {
	eventType uint16
	_         uint16
	keyDown   int32
	repeat    uint16
	virtual   uint16
	scan      uint16
	char      uint16
	control   uint32
}

// openConsoleInput opens the input of the console of the test, as the program reads its keys. A process with no console (a runner with
// none attached) gets one for the length of the test; where none can be had, the test is skipped.
func openConsoleInput(t *testing.T) *os.File {
	t.Helper()
	open := func() (windows.Handle, error) {
		name, err := windows.UTF16PtrFromString("CONIN$")
		if err != nil {
			t.Fatal(err)
		}
		return windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	}
	h, err := open()
	if err != nil {
		kernel32 := windows.NewLazySystemDLL("kernel32.dll")
		if r, _, e := kernel32.NewProc("AllocConsole").Call(); r == 0 {
			t.Skipf("this process has no console and cannot make one: %v", e)
		}
		t.Cleanup(func() { _, _, _ = kernel32.NewProc("FreeConsole").Call() })
		if h, err = open(); err != nil {
			t.Skipf("the console that was made has no input to open: %v", err)
		}
	}
	f := os.NewFile(uintptr(h), "CONIN$")
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func pendingEvents(t *testing.T, f *os.File) uint32 {
	t.Helper()
	var n uint32
	if err := windows.GetNumberOfConsoleInputEvents(windows.Handle(f.Fd()), &n); err != nil {
		t.Fatalf("count the events of the console input: %v", err)
	}
	return n
}

// Keys typed while nothing reads are queued by the console, and key mode would hand them to the question that opens next: an Enter that
// nobody pressed for it. Key mode starts by dropping them.
func TestEnableKeysDropsWhatWasTypedBeforeKeyMode(t *testing.T) {
	conin := openConsoleInput(t)
	enter := []inputRecord{
		{eventType: 1, keyDown: 1, repeat: 1, virtual: 0x0d, scan: 0x1c, char: '\r'}, // KEY_EVENT: Enter down
		{eventType: 1, keyDown: 0, repeat: 1, virtual: 0x0d, scan: 0x1c, char: '\r'}, // and up
	}
	var written uint32
	write := windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleInputW")
	if r, _, err := write.Call(conin.Fd(), uintptr(unsafe.Pointer(&enter[0])), uintptr(len(enter)), uintptr(unsafe.Pointer(&written))); r == 0 {
		t.Fatalf("type into the console: %v", err)
	}
	if pendingEvents(t, conin) == 0 {
		t.Fatal("the console queued nothing of what was typed: the test cannot see what key mode does with it")
	}
	restore, err := enableKeys(conin)
	if err != nil {
		t.Fatalf("enableKeys: %v", err)
	}
	t.Cleanup(func() { _ = restore() })
	if n := pendingEvents(t, conin); n != 0 {
		t.Errorf("%d events typed before key mode are still queued", n)
	}
}
