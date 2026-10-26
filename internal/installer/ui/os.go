package ui

import (
	"context"
	"io"
	"os"

	"golang.org/x/term"
)

// OSEnv reads the inputs of the mode choice from the process: whether stdin and stdout are terminals, the size of stdout, and the
// environment variables Env names.
func OSEnv(plain bool) Env {
	e := Env{Plain: plain, Vars: map[string]string{}}
	for _, name := range []string{"NO_COLOR", "TERM", "COLORTERM", "WT_SESSION"} {
		if v, set := os.LookupEnv(name); set {
			e.Vars[name] = v
		}
	}
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	e.StdinTTY, e.StdoutTTY = term.IsTerminal(in), term.IsTerminal(out)
	if e.StdoutTTY {
		// An unknown size leaves 0x0, which Mode reads as too small for the full screen.
		if w, h, err := term.GetSize(out); err == nil {
			e.Width, e.Height = w, h
		}
	}
	return e
}

// OpenOS opens a screen on the process's console, reading answers from in; ctx ends a prompt (see Options.Context). The mode comes
// from OSEnv; on Windows the console must also accept virtual-terminal sequences, and if it does not the screen is plain. Pass the
// bufio.Reader the Resolver reads from as in, so that no buffered input is lost between them.
func OpenOS(ctx context.Context, in io.Reader, version string, plain bool) (*Screen, error) {
	env := OSEnv(plain)
	o := Options{Out: os.Stdout, In: in, Context: ctx, Version: version, Mode: env.Mode(), Color: env.Color(), Width: env.Width, Height: env.Height, InputIsTerminal: env.StdinTTY}
	if env.StdinTTY { // a plain run at a terminal needs it too: a hidden line is read in key mode (no echo, Ctrl-C kept as a signal)
		o.KeyInput = func() (func() error, error) { return enableKeys(os.Stdin) }
		o.InputReady = inputReadiness(os.Stdin) // nil where the terminal cannot be waited on without reading it
		o.FlushInput = inputFlusher(os.Stdin)
	}
	if o.Mode == ModeFull {
		restore, err := enableVT(os.Stdout)
		if err != nil {
			o.Mode = ModePlain // shortcut: the reason is not reported, only that the console cannot draw; plain output still works
		} else {
			o.RestoreConsole = restore
		}
	}
	return Open(o)
}

// inputFlusher is Options.FlushInput of the terminal behind f, nil where the input cannot be flushed, as inputReadiness is nil where it cannot
// be waited on. It flushes once to find out, which drops what was typed before the screen opened, as the first question would. That a handle
// cannot be flushed (a console opened for reading only, whose mode cannot be set either) only degrades the screen: the drop is a guard against
// stray keys and no more, so no question fails for it, and the screen is never given a flush that is known to fail. shortcut: the reason is
// not reported, only that the guard is off; report it if a reader of the logs ever needs to know why stray keys answered a question.
func inputFlusher(f *os.File) func() error {
	if flushInput(f) != nil {
		return nil
	}
	return func() error { return flushInput(f) }
}
