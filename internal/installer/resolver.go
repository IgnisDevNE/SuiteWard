package installer

import (
	"flag"
	"io"
)

// Terminal is the console the prompts talk to; tests replace it.
type Terminal struct {
	In           io.Reader
	Out          io.Writer
	IsTerminal   bool                   // stdin is an interactive terminal
	ReadPassword func() ([]byte, error) // reads one line without echo; used only when IsTerminal
}

// OSTerminal is the process's console: stdin, stdout, and golang.org/x/term for no-echo input.
func OSTerminal() Terminal { return Terminal{} }

// Resolver answers every question of the installer from, in order: the flag, the answers file, then a prompt.
// A flag and its answers-file key share one name.
type Resolver struct{}

// NewResolver returns a Resolver over the flags the user set, the answers file's values and a terminal.
func NewResolver(flags, answers map[string]string, term Terminal) *Resolver { return &Resolver{} }

// FlagsSet returns the flags of fs that were set on the command line.
func FlagsSet(fs *flag.FlagSet) map[string]string { return nil }

// LoadAnswers reads a flat YAML answers file; every value is a scalar.
func LoadAnswers(path string) (map[string]string, error) { return nil, errNotImplemented }

// Value resolves a non-secret answer. With def non-empty a missing answer takes the default when input is not possible.
func (r *Resolver) Value(key, label, def string) (string, error) { return "", errNotImplemented }

// Int resolves a non-secret integer answer.
func (r *Resolver) Int(key, label string, def int) (int, error) { return 0, errNotImplemented }

// Secret resolves a secret from the answers file, a no-echo prompt, or stdin. A flag never carries a secret.
func (r *Resolver) Secret(key, label string) ([]byte, error) { return nil, errNotImplemented }
