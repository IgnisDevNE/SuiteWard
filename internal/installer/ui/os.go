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
