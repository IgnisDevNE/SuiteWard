package installer

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/term"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// Terminal is the console the prompts talk to; tests replace it.
type Terminal struct {
	In           io.Reader
	Out          io.Writer
	IsTerminal   bool                   // stdin is an interactive terminal
	ReadPassword func() ([]byte, error) // reads one line without echo; used only when IsTerminal
}

// OSTerminal is the process's console: stdin, stdout, and golang.org/x/term for no-echo input.
func OSTerminal() Terminal {
	fd := int(os.Stdin.Fd())
	return Terminal{
		In: os.Stdin, Out: os.Stdout, IsTerminal: term.IsTerminal(fd),
		ReadPassword: func() ([]byte, error) { return term.ReadPassword(fd) },
	}
}

// Resolver answers every question of the installer from, in order: the flag, the answers file, then a prompt.
// A flag and its answers-file key share one name.
type Resolver struct {
	flags, answers map[string]string
	term           Terminal
	in             *bufio.Reader // shared with the Screen: both read from the one buffer
	screen         *ui.Screen    // every question goes through it
}

// validate refuses a console that cannot read or write.
func (t Terminal) validate() error {
	if t.In == nil || t.Out == nil || (t.IsTerminal && t.ReadPassword == nil) {
		return errors.New("the console needs an input and an output, and a password reader when it is a terminal")
	}
	return nil
}

// NewResolver returns a Resolver over the flags the user set, the answers file's values and a terminal. Its questions go through the
// Screen that is the terminal's output (Env.openConsole makes it one), or through a plain one on that output.
func NewResolver(flags, answers map[string]string, term Terminal) (*Resolver, error) {
	if err := term.validate(); err != nil {
		return nil, err
	}
	in := bufio.NewReader(term.In) // the Screen's own reader when term.In is it: bufio keeps a reader of its default size
	screen, ok := term.Out.(*ui.Screen)
	if !ok {
		var err error
		if screen, err = ui.Open(ui.Options{Out: term.Out, In: in, Mode: ui.ModePlain}); err != nil {
			return nil, err
		}
	}
	return &Resolver{flags: flags, answers: answers, term: term, in: in, screen: screen}, nil
}

// FlagsSet returns the flags of fs that were set on the command line.
func FlagsSet(fs *flag.FlagSet) map[string]string {
	set := map[string]string{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = f.Value.String() })
	return set
}

// LoadAnswers reads a flat YAML answers file; every value is a scalar.
func LoadAnswers(path string) (map[string]string, error) {
	// The file may hold the App key or passwords, so on systems with POSIX modes it must be private to its owner.
	if info, err := os.Stat(path); err == nil && runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("the answers file %s is readable by others (mode %o); run chmod 600 on it", path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read the answers file: %w", err)
	}
	// Decoding into strings keeps the text the user wrote: 1.10, 0x10 and a long digit token must not be read as numbers.
	var answers map[string]string
	if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&answers); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse the answers file %s (every value must be a single scalar): %w", path, err)
	}
	if answers == nil {
		answers = map[string]string{}
	}
	return answers, nil
}

func (r *Resolver) say(format string, args ...any) { _, _ = fmt.Fprintf(r.term.Out, format, args...) }

// Value resolves a non-secret answer. With def non-empty a missing answer takes the default when input is not possible.
func (r *Resolver) Value(key, label, def string) (string, error) {
	if v, ok := r.flags[key]; ok {
		return v, nil
	}
	if v, ok := r.answers[key]; ok {
		return v, nil
	}
	if !r.term.IsTerminal {
		if def != "" {
			return def, nil
		}
		return "", fmt.Errorf("%s is required: pass --%s or set %q in the answers file (stdin is not a terminal, so it cannot be asked)", label, key, key)
	}
	line, err := r.screen.Ask(label, def) // an empty answer is the default
	if err != nil {
		return "", err
	}
	if line == "" {
		return "", fmt.Errorf("%s is required: pass --%s or set %q in the answers file", label, key, key)
	}
	return line, nil
}

// Int resolves a non-secret integer answer.
func (r *Resolver) Int(key, label string, def int) (int, error) {
	v, err := r.Value(key, label, strconv.Itoa(def))
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("%s (%q) is not a number: %q", label, key, v)
	}
	return n, nil
}

// Secret resolves a secret from the answers file, a no-echo prompt, or stdin. A flag never carries a secret.
func (r *Resolver) Secret(key, label string) ([]byte, error) {
	if _, ok := r.flags[key]; ok {
		return nil, fmt.Errorf("%s must not be given as an argument (it would show in the process list): put %q in the answers file, type it at the prompt, or pipe it to stdin", label, key)
	}
	if v, ok := r.answers[key]; ok {
		return nonEmpty(key, label, []byte(v))
	}
	if r.term.IsTerminal {
		secret, err := r.screen.Secret(label, r.term.ReadPassword)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", label, err)
		}
		return nonEmpty(key, label, secret)
	}
	all, err := io.ReadAll(r.in)
	if err != nil {
		return nil, fmt.Errorf("read %s from stdin: %w", label, err)
	}
	all = bytes.TrimSuffix(bytes.TrimSuffix(all, []byte("\n")), []byte("\r"))
	return nonEmpty(key, label, all)
}

func nonEmpty(key, label string, secret []byte) ([]byte, error) {
	if len(secret) == 0 {
		return nil, fmt.Errorf("%s is empty: set %q in the answers file, type it at the prompt, or pipe it to stdin", label, key)
	}
	return secret, nil
}

// ask prints a question and reads one line of the terminal. A flag or the answers file never answers it, so it is for the choices
// that only the person at the terminal makes. The end of the input is io.EOF.
func (r *Resolver) ask(question string) (string, error) { return r.screen.Prompt(question) }

// Select resolves a choice among a few answers as Value resolves a text one: the flag, then the answers file, then the default when
// there is no terminal to ask. At a full screen it lists the choices (name and description) to be picked with the arrow keys; a plain
// screen asks Value's typed question, "label (a or b) [def]: ", exactly as before. def is one of the choices' names; the answer is
// the name of the one that was picked.
func (r *Resolver) Select(key, label string, choices []ui.Choice, def string) (string, error) {
	if !r.asks(key) || !r.screen.FullScreen() {
		return r.Value(key, label+" ("+orList(choices)+")", def)
	}
	i := slices.IndexFunc(choices, func(c ui.Choice) bool { return c.Name == def })
	if i < 0 {
		return "", fmt.Errorf("the default %q of %s is not one of its choices", def, key)
	}
	picked, err := r.screen.Select(label, choices, i)
	if err != nil {
		return "", err
	}
	return choices[picked].Name, nil
}

// orList is the names of the choices as a sentence: "a or b", "a, b or c".
func orList(choices []ui.Choice) string {
	names := make([]string, len(choices))
	for i, c := range choices {
		names[i] = c.Name
	}
	if len(names) < 2 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// MultiSelect resolves a multiple choice as Select resolves a single one.
func (r *Resolver) MultiSelect(key string, m ui.Multi) ([]string, error) {
	return nil, errors.New("not built")
}
