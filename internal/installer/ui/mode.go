package ui

// Mode is how a Screen draws.
type Mode int

const (
	// ModePlain writes lines with no escape sequence: for pipes, agents, CI, NO_COLOR, TERM=dumb and --plain.
	ModePlain Mode = iota
	// ModeFull takes over the terminal: the alternate screen, a frame, the step list and colors.
	ModeFull
)

// The smallest terminal the full-screen layout fits in.
const (
	minWidth  = 60
	minHeight = 20
)

// Env is what the choice of mode and color depends on. OSEnv reads it from the process; tests build it by hand.
type Env struct {
	StdinTTY, StdoutTTY bool
	Plain               bool              // the caller's explicit plain flag
	Width, Height       int               // the size of stdout, 0 when it is unknown
	Vars                map[string]string // the NO_COLOR, TERM, COLORTERM and WT_SESSION variables that are set
}

// Mode is full screen only when stdin and stdout are both terminals of at least 60x20, the caller did not ask for plain, TERM is not
// "dumb", and NO_COLOR is not set. NO_COLOR counts as set with any value, an empty one included; the alternative of keeping the layout
// without color was not taken, so plain output is the one fallback.
func (e Env) Mode() Mode {
	if e.Plain || !e.StdinTTY || !e.StdoutTTY || e.Width < minWidth || e.Height < minHeight {
		return ModePlain
	}
	if _, set := e.Vars["NO_COLOR"]; set {
		return ModePlain
	}
	if e.Vars["TERM"] == "dumb" {
		return ModePlain
	}
	return ModeFull
}

// Color is 24-bit when COLORTERM says truecolor or 24bit, or on Windows Terminal (WT_SESSION is set), and the nearest of the 256 colors
// otherwise.
func (e Env) Color() ColorLevel {
	if _, wt := e.Vars["WT_SESSION"]; wt {
		return ColorTrue
	}
	if v := e.Vars["COLORTERM"]; v == "truecolor" || v == "24bit" {
		return ColorTrue
	}
	return Color256
}
