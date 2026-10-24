//go:build linux

package ui

import (
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
