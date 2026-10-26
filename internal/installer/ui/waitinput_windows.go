//go:build windows

package ui

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"slices"

	"golang.org/x/sys/windows"
)

// inputRecord is the INPUT_RECORD of a key event: what the console queues for a key.
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

const (
	keyEvent      = 1   // INPUT_RECORD.EventType of a key event
	keyPollEvery  = 100 // milliseconds a wait for a key waits on the console before it looks at its context again
	peekedRecords = 16  // events the wait looks at in one go
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procPeekConsole  = kernel32.NewProc("PeekConsoleInputW")
	procReadConsoleI = kernel32.NewProc("ReadConsoleInputW")
)

// addressOf is the address of what p points to, for a call into the console API. shortcut: it goes through reflect.ValueOf(p).Pointer()
// because an inline unsafe.Pointer is flagged by gosec G103, which has no exception here; what p points to has escaped to the heap by then
// (it is an interface value), where it stays put, and the caller keeps it alive with runtime.KeepAlive until the call returns.
func addressOf(p any) uintptr { return reflect.ValueOf(p).Pointer() }

// inputReadiness is the wait for a key of the console behind f that Options.InputReady is: it waits on the console and looks at its events,
// and reads none a key needs, so the end of its context leaves nothing pending. It is nil where the console cannot be waited on.
func inputReadiness(f *os.File) func(context.Context) error {
	h := windows.Handle(f.Fd())
	if ev, err := windows.WaitForSingleObject(h, 0); err != nil || (ev != windows.WAIT_OBJECT_0 && ev != uint32(windows.WAIT_TIMEOUT)) {
		return nil
	}
	return func(ctx context.Context) error {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			ev, err := windows.WaitForSingleObject(h, keyPollEvery)
			switch {
			case err != nil:
				return fmt.Errorf("wait for a key: %w", err)
			case ev != windows.WAIT_OBJECT_0: // nothing yet
			default:
				if seen, err := skipToCharacter(h); err != nil || seen {
					return err
				}
			}
		}
	}
}

// skipToCharacter drops the events of the console input that make no character (a key let go, a modifier, an arrow, a focus change), up to
// the first key press that makes one, and says whether there is one: the read after it then has a character to read and does not block.
func skipToCharacter(h windows.Handle) (bool, error) {
	var records [peekedRecords]inputRecord
	var n uint32
	if r, _, err := procPeekConsole.Call(uintptr(h), addressOf(&records[0]), uintptr(len(records)), addressOf(&n)); r == 0 {
		return false, fmt.Errorf("look at the keys of the console: %w", err)
	}
	seen := slices.IndexFunc(records[:n], func(r inputRecord) bool { return r.eventType == keyEvent && r.keyDown != 0 && r.char != 0 })
	drop := int(n)
	if seen >= 0 {
		drop = seen
	}
	if drop > 0 {
		var read uint32
		if r, _, err := procReadConsoleI.Call(uintptr(h), addressOf(&records[0]), uintptr(drop), addressOf(&read)); r == 0 {
			return false, fmt.Errorf("drop the events that make no character: %w", err)
		}
		runtime.KeepAlive(&read)
	}
	return seen >= 0, nil
}
