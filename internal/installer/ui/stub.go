package ui

// Compiling skeleton for the test-first commit: every method exists and does nothing, so the tests fail on behavior
// (the output is empty, the mode is always plain) rather than on a compilation error. The next commit replaces this file.

import (
	"errors"
	"io"
	"sync"
	"time"
)

var errNotImplemented = errors.New("not implemented")

type Mode int

const (
	ModePlain Mode = iota
	ModeFull
)

type ColorLevel int

const (
	ColorNone ColorLevel = iota
	Color256
	ColorTrue
)

type Env struct {
	StdinTTY, StdoutTTY, Plain bool
	Width, Height              int
	Vars                       map[string]string
}

func (Env) Mode() Mode { return ModePlain }

func (Env) Color() ColorLevel { return ColorNone }

func OSEnv(bool) Env { return Env{} }

type Options struct {
	Out            io.Writer
	In             io.Reader
	Version        string
	Mode           Mode
	Color          ColorLevel
	Width, Height  int
	RestoreConsole func() error
}

type Choice struct{ Name, Desc string }

type KV struct{ Key, Value string }

type Screen struct{ mu sync.Mutex }

type frame struct {
	rows           []string
	curRow, curCol int
	cursor         bool
}

type rgb struct{ r, g, b int }

type tone int

const (
	toneDefault tone = iota
	toneGreen
	toneMuted
	toneAmber
)

var green, muted, amber rgb

func nearest256(rgb) int { return 0 }

func (ColorLevel) paint(tone, bool, string) string { return "" }

func wrap(string, int) []string { return nil }

func fit(string, int) string { return "" }

func Open(Options) (*Screen, error) { return &Screen{}, nil }

func OpenOS(io.Reader, string, bool) (*Screen, error) { return nil, errNotImplemented }

func (*Screen) FullScreen() bool                          { return false }
func (*Screen) Write(p []byte) (int, error)               { return len(p), nil }
func (*Screen) Say(string, ...any)                        {}
func (*Screen) Check(string, ...any)                      {}
func (*Screen) Warn(string, ...any)                       {}
func (*Screen) Keep(string, ...any)                       {}
func (*Screen) Steps(...string)                           {}
func (*Screen) Step(int)                                  {}
func (*Screen) Progress(string, float64, string)          {}
func (*Screen) Spin(string, time.Duration)                {}
func (*Screen) EndSpin()                                  {}
func (*Screen) Prompt(string) (string, error)             { return "", errNotImplemented }
func (*Screen) Ask(string, string) (string, error)        { return "", errNotImplemented }
func (*Screen) Choose(string, []Choice, int) (int, error) { return 0, errNotImplemented }
func (*Screen) Secret(string, func() ([]byte, error)) ([]byte, error) {
	return nil, errNotImplemented
}
func (*Screen) Summary([]KV, []string) error { return errNotImplemented }
func (*Screen) Suspend()                     {}
func (*Screen) Resume()                      {}
func (*Screen) Close() error                 { return nil }

func (*Screen) frame() frame { return frame{} }
