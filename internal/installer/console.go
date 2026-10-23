package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The console of one command. Every command opens one Screen (Env.openConsole) and makes it the Terminal's output, so everything that
// prints to that writer, the say helpers included, goes through it. The spinner, the bars and the summary exist only in full screen:
// a plain run prints what it printed before the Screen existed.
//
// The steps of each flow, in the order of its code (a step starts a clean page, so what must outlive it is a Warn, a Keep line or a
// summary row):
//
//	install, co-located   Environment (runtime, ports, image), Checks (ports, secrets, images), GitHub, Start (the stack comes up),
//	                      Owner (setup), Done (summary)
//	install, isolated     Checks (host, adoption, ports), Images, GitHub, Units (env file, secrets, units), Start (database, service,
//	                      the wait for it), Owner (setup), Done (summary)
//	install --upgrade     Plan (what changes, the question), Backup, Images, Replace (the stack comes up on the new release), Check
//	                      (the service settles, summary)
//	uninstall             Confirm, Remove
//	install host          Podman, Account, Metadata, Next (the configuration directory and the command for the service account)
//	enroll                Instance (finding the stack), Owner (status, confirmation, setup)
//	backup init           Keys (the instance, the key pair), Kit (the file, the typed word), Settings (the recipient)
//	backup                Status, Dump, Artifacts, Encrypt
//	restore               Verify (the kit, the backup, the release), Preview (what it holds, the confirmation), Safety backup, Stop, Load (the
//	                      dump, the artifacts), Start (the migration, the service), Compare (/status with the backup's)
//	doctor                Doctor (the checks as Check and Warn lines)

// endConsole closes the Screen when a command ends and joins what the terminal failed to take to the command's own error (err points
// at its result), as doctor does with its exit code: the command may have succeeded, and what it printed was lost.
func endConsole(scr *ui.Screen, err *error) {
	if closeErr := scr.Close(); closeErr != nil {
		*err = errors.Join(*err, closeErr)
	}
}

// screenOn is the Screen that out is, or a plain one that writes to out: a caller that has no terminal (a test, a library call) still
// talks to the one Screen API.
func screenOn(out io.Writer) *ui.Screen {
	if s, ok := out.(*ui.Screen); ok {
		return s
	}
	s, err := ui.Open(ui.Options{Out: out, In: strings.NewReader(""), Mode: ui.ModePlain})
	if err != nil {
		panic(fmt.Sprintf("installer: a plain screen cannot be opened on %T: %v", out, err)) // only a missing writer fails, and nothing prints to one
	}
	return s
}

// spinTick is how often the spinner of a running command moves; a variable so that a test does not wait for it.
var spinTick = 200 * time.Millisecond

// working is spinning for a command that can fail.
func working(scr *ui.Screen, label string, fn func() error) (err error) {
	spinning(scr, label, func() { err = fn() })
	return err
}

// spinning runs fn under the spinner of label, which a ticking goroutine moves until fn returns. Plain output has no spinner.
func spinning(scr *ui.Screen, label string, fn func()) {
	if !scr.FullScreen() {
		fn()
		return
	}
	start, done, stopped := time.Now(), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(spinTick)
		defer tick.Stop()
		for {
			scr.Spin(label, time.Since(start))
			select {
			case <-done:
				return
			case <-tick.C:
			}
		}
	}()
	defer func() {
		close(done)
		<-stopped // no tick draws once the spinner is gone
		scr.EndSpin()
	}()
	fn()
}

// waitBar draws the progress bar of a wait. Plain output has no bar, and a wait that is given none (nil) draws none.
type waitBar func(fraction float64, status string)

func barOn(scr *ui.Screen, label string) waitBar {
	if !scr.FullScreen() {
		return nil
	}
	return func(fraction float64, status string) { scr.Progress(label, fraction, status) }
}

func (b waitBar) set(fraction float64, status string) {
	if b != nil {
		b(fraction, status)
	}
}

// showSummary shows the summary screen of a finished install or upgrade, and waits for Enter. A plain run has printed its closing lines
// already, and does not even build the rows (they may ask the service for its status). Ctrl-C there only skips the wait: the work is done.
func showSummary(scr *ui.Screen, rows func() []ui.KV) error {
	if !scr.FullScreen() {
		return nil
	}
	if err := scr.Summary(rows(), nil); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
