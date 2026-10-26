//go:build windows

package ui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

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
	t.Cleanup(func() { _ = windows.FlushConsoleInputBuffer(h) }) // what a test typed is not left for the shell that runs the tests
	return f
}

// typeIntoConsole queues events in the input of the console, as the keyboard does.
func typeIntoConsole(t *testing.T, f *os.File, events ...inputRecord) {
	t.Helper()
	var written uint32
	write := windows.NewLazySystemDLL("kernel32.dll").NewProc("WriteConsoleInputW")
	if r, _, err := write.Call(f.Fd(), addressOf(&events[0]), uintptr(len(events)), addressOf(&written)); r == 0 {
		t.Fatalf("type into the console: %v", err)
	}
	runtime.KeepAlive(&written)
}

// keyEvents are the events of a key that is pressed and let go: the key down comes with the character, and the key up has none.
func keyEvents(char uint16, virtual, scan uint16) []inputRecord {
	return []inputRecord{
		{eventType: 1, keyDown: 1, repeat: 1, virtual: virtual, scan: scan, char: char},
		{eventType: 1, keyDown: 0, repeat: 1, virtual: virtual, scan: scan, char: char},
	}
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
	typeIntoConsole(t, conin, keyEvents('\r', 0x0d, 0x1c)...)
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

// The wait for a key sees a key that makes a character, and nothing else: a key let go, a modifier or a window event that the console queues
// would only end it for a read that then waits for a key. It takes nothing a read needs, and it ends with its context. (An arrow is a
// character here: a console in virtual-terminal input mode queues it as the characters of its escape sequence.)
func TestInputReadinessWaitsForACharacterWithoutReadingItAndEndsWithItsContext(t *testing.T) {
	conin := openConsoleInput(t)
	restore, err := enableKeys(conin)
	if err != nil {
		t.Fatalf("enableKeys: %v", err)
	}
	t.Cleanup(func() { _ = restore() })
	ready := inputReadiness(conin)
	if ready == nil {
		t.Fatal("this console cannot be waited on for a key")
	}
	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() { waited <- ready(ctx) }()
	// A focus event, Shift pressed and let go, and the key up of a letter: none of them is a character.
	typeIntoConsole(t, conin, inputRecord{eventType: 0x10}, inputRecord{eventType: keyEvent, keyDown: 1, repeat: 1, virtual: 0x10, scan: 0x2a},
		inputRecord{eventType: keyEvent, repeat: 1, virtual: 0x10, scan: 0x2a}, inputRecord{eventType: keyEvent, repeat: 1, virtual: 0x41, scan: 0x1e, char: 'x'})
	select {
	case err := <-waited:
		t.Fatalf("the wait ended with no character typed: %v", err)
	case <-time.After(350 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-waited:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the wait ended with %v, want the end of its context", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the context did not end the wait")
	}

	typeIntoConsole(t, conin, keyEvents('a', 0x41, 0x1e)...)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := ready(ctx2); err != nil {
		t.Fatalf("a character was typed and the wait did not see it: %v", err)
	}
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 1)
		n, _ := conin.Read(buf)
		got <- string(buf[:n])
	}()
	select {
	case s := <-got:
		if s != "a" {
			t.Errorf("the read after the wait got %q, want the character that was typed (a)", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the wait took the character: the read after it has nothing to read")
	}
}

// A question in cooked mode is not answered by a line typed before it: flushInput drops what the console queued, and leaves its mode alone.
func TestFlushInputDropsWhatWasTypedAheadAndLeavesTheModeAlone(t *testing.T) {
	conin := openConsoleInput(t)
	h := windows.Handle(conin.Fd())
	var before uint32
	if err := windows.GetConsoleMode(h, &before); err != nil {
		t.Fatalf("read the console input mode: %v", err)
	}
	typeIntoConsole(t, conin, keyEvents('\r', 0x0d, 0x1c)...)
	if pendingEvents(t, conin) == 0 {
		t.Fatal("the console queued nothing of what was typed: the test cannot see what the flush does with it")
	}
	if err := flushInput(conin); err != nil {
		t.Fatalf("flushInput: %v", err)
	}
	if n := pendingEvents(t, conin); n != 0 {
		t.Errorf("%d events typed ahead are still queued", n)
	}
	var after uint32
	if err := windows.GetConsoleMode(h, &after); err != nil || after != before {
		t.Errorf("the console input mode changed from %#x to %#x (%v)", before, after, err)
	}
}

// A file is not a console: its input cannot be flushed, and that is an error, not a silent no-op.
func TestFlushInputRejectsANonConsole(t *testing.T) {
	if err := flushInput(notAConsole(t)); err == nil {
		t.Fatal("flushInput accepted a file")
	}
}
