package installer

import (
	"context"
	"errors"
	"flag"
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

// A command that succeeds ends on a page that waits for Enter: showSummary for the install, the upgrade, backup, backup init and restore,
// holdPage for the others. A command that fails ends on its error: endConsole shows it on the screen, which waits for Enter, and Main prints
// it after the screen is closed, as it always did. An interrupt, a usage error and plain output show nothing and wait for nothing.

// endConsole ends the console of a command, for every command that opens one: a command that failed has its error shown on the screen,
// which waits for Enter (Screen.Fail: not in plain mode, and not for an interrupt or a usage error), then the Screen is closed, and what
// the terminal failed to take is joined to the command's own error (err points at its result), as doctor does with its exit code: the
// command may have succeeded, and what it printed was lost. Doctor's failed checks are results and not an error: its page is its own.
func endConsole(scr *ui.Screen, err *error) {
	if *err != nil && !errors.Is(*err, ErrUsage) && !errors.Is(*err, flag.ErrHelp) && scr.Fail(secretInURL.ReplaceAllString((*err).Error(), "://***@")) {
		*err = failureShown{*err}
	}
	if closeErr := scr.Close(); closeErr != nil {
		*err = errors.Join(*err, closeErr)
	}
}

// failureShown is the error of a command whose failure the screen showed and waited on. A Ctrl-C at that wait closes the page and is not an
// interrupt of the work: Main keeps the command's own exit code for it.
type failureShown struct{ err error }

func (e failureShown) Error() string { return e.err.Error() }
func (e failureShown) Unwrap() error { return e.err }

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
// already, and does not even build the rows (they may ask the service for its status, or run doctor). A row without a key is a warning of
// the summary (summaryRows.warn). Ctrl-C there only skips the wait: the work is done. A row that says a Ctrl-C ended the checks the summary
// was built with (interruptedKey) is shown with the rest, and then ends the command as interrupted.
func showSummary(scr *ui.Screen, rows func() []ui.KV) error {
	if !scr.FullScreen() {
		return nil
	}
	var kept []ui.KV
	var warnings []string
	interrupted := false
	for _, kv := range rows() {
		switch kv.Key {
		case "":
			warnings = append(warnings, kv.Value)
		case interruptedKey:
			interrupted = true
		default:
			kept = append(kept, kv)
		}
	}
	if err := scr.Summary(kept, warnings); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	if interrupted {
		return fmt.Errorf("the final checks of the instance did not finish: %w", context.Canceled)
	}
	return nil
}

// holdPage keeps the last page of a command on the screen until Enter, so that what it printed can be read before the screen closes: a
// command that ends on text and not on a summary (uninstall, install host, enroll, doctor, an upgrade with nothing to do) ends with it.
// A plain run has nothing to hold. As with showSummary, Ctrl-C only skips the wait: the work is done.
func holdPage(scr *ui.Screen) error {
	if err := scr.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
