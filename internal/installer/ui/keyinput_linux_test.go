//go:build linux

package ui

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openPty opens a pseudo-terminal: the master a person's terminal emulator holds, and the slave a program reads its keys from.
func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no pseudo-terminal here: %v", err)
	}
	t.Cleanup(func() { _ = master.Close() })
	n, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatalf("name the pseudo-terminal: %v", err)
	}
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatalf("unlock the pseudo-terminal: %v", err)
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open the pseudo-terminal's slave: %v", err)
	}
	t.Cleanup(func() { _ = slave.Close() })
	return master, slave
}

func termios(t *testing.T, f *os.File) unix.Termios {
	t.Helper()
	tio, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatalf("read the terminal mode: %v", err)
	}
	return *tio
}

// Key mode is what makes an arrow readable before a newline, and Ctrl-C must stay the terminal's interrupt, not a byte. Restore puts the
// exact previous mode back.
func TestEnableKeysReadsKeysAtOnceKeepsSignalsAndRestoresTheMode(t *testing.T) {
	master, slave := openPty(t)
	before := termios(t, slave)
	if need := uint32(unix.ICANON | unix.ECHO | unix.ISIG); before.Lflag&need != need {
		t.Fatalf("a new pseudo-terminal should be cooked, its lflag is %#x", before.Lflag)
	}
	restore, err := enableKeys(slave)
	if err != nil {
		t.Fatalf("enableKeys: %v", err)
	}
	during := termios(t, slave)
	if during.Lflag&(unix.ICANON|unix.ECHO) != 0 {
		t.Errorf("line input or echo is still on: lflag %#x", during.Lflag)
	}
	if during.Lflag&unix.ISIG == 0 {
		t.Error("signal generation was turned off: Ctrl-C would arrive as a character and not cancel the run")
	}
	if during.Oflag != before.Oflag || during.Iflag != before.Iflag {
		t.Errorf("output or input flags changed: oflag %#x -> %#x, iflag %#x -> %#x", before.Oflag, during.Oflag, before.Iflag, during.Iflag)
	}
	if during.Cc[unix.VMIN] != 1 || during.Cc[unix.VTIME] != 0 {
		t.Errorf("a read must wait for one byte: VMIN %d, VTIME %d", during.Cc[unix.VMIN], during.Cc[unix.VTIME])
	}

	read := func(want string) {
		t.Helper()
		got := make(chan string, 1)
		go func() {
			buf := make([]byte, len(want))
			n, _ := io.ReadFull(slave, buf)
			got <- string(buf[:n])
		}()
		select {
		case s := <-got:
			if s != want {
				t.Errorf("the slave read %q, want %q", s, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("the slave did not receive %q: a key must arrive before any newline", want)
		}
	}
	write := func(s string) {
		t.Helper()
		if _, err := master.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	write("\x1b[B") // an arrow arrives at once, with no newline
	read("\x1b[B")
	write("\x03a") // Ctrl-C is the terminal's interrupt: only the letter arrives
	read("a")

	if err := restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if after := termios(t, slave); after != before {
		t.Errorf("the terminal mode was not restored.\nbefore: %+v\nafter:  %+v", before, after)
	}
}

// Keys typed while nothing reads are queued by the terminal (the line discipline), and key mode would hand them to the question that opens
// next: an Enter that nobody pressed for it. Key mode starts by dropping them, and keeps what is typed after.
func TestEnableKeysDropsWhatWasTypedBeforeKeyMode(t *testing.T) {
	master, slave := openPty(t)
	if _, err := master.WriteString("x\r"); err != nil { // a whole line, typed ahead
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond) // the line is in the queue of the terminal
	restore, err := enableKeys(slave)
	if err != nil {
		t.Fatalf("enableKeys: %v", err)
	}
	t.Cleanup(func() { _ = restore() })
	if _, err := master.WriteString("a"); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 1)
		n, _ := io.ReadFull(slave, buf)
		got <- string(buf[:n])
	}()
	select {
	case s := <-got:
		if s != "a" {
			t.Errorf("the first key the question reads is %q, want the key typed after key mode started (a)", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the key typed after key mode started did not arrive")
	}
}

// The wait for a key sees a byte without reading it, and ends with its context.
func TestInputReadinessWaitsForAKeyWithoutReadingItAndEndsWithItsContext(t *testing.T) {
	master, slave := openPty(t)
	restore, err := enableKeys(slave)
	if err != nil {
		t.Fatalf("enableKeys: %v", err)
	}
	t.Cleanup(func() { _ = restore() })
	ready := inputReadiness(slave)
	if ready == nil {
		t.Fatal("this terminal cannot be waited on for a key")
	}
	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() { waited <- ready(ctx) }()
	select {
	case err := <-waited:
		t.Fatalf("the wait ended with no key typed: %v", err)
	case <-time.After(300 * time.Millisecond):
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

	if _, err := master.WriteString("a"); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	if err := ready(ctx2); err != nil {
		t.Fatalf("a key was typed and the wait did not see it: %v", err)
	}
	buf := make([]byte, 1)
	if n, err := slave.Read(buf); n != 1 || buf[0] != 'a' || err != nil {
		t.Errorf("the read after the wait got %q, %v, want the key that was typed (a): the wait must not take it", buf[:n], err)
	}
}
