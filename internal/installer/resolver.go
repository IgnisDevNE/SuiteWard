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
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
	"golang.org/x/term"
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
	in             *bufio.Reader
}

// NewResolver returns a Resolver over the flags the user set, the answers file's values and a terminal.
func NewResolver(flags, answers map[string]string, term Terminal) (*Resolver, error) {
	if term.In == nil || term.Out == nil || (term.IsTerminal && term.ReadPassword == nil) {
		return nil, errors.New("the console needs an input and an output, and a password reader when it is a terminal")
	}
	return &Resolver{flags: flags, answers: answers, term: term, in: bufio.NewReader(term.In)}, nil
}

// FlagsSet returns the flags of fs that were set on the command line.
func FlagsSet(fs *flag.FlagSet) map[string]string {
	set := map[string]string{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = f.Value.String() })
	return set
}

// LoadAnswers reads a flat YAML answers file; every value is a scalar.
func LoadAnswers(path string) (map[string]string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read the answers file: %w", err)
	}
	var parsed map[string]any
	if err := yaml.NewDecoder(bytes.NewReader(raw)).Decode(&parsed); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parse the answers file %s: %w", path, err)
	}
	answers := make(map[string]string, len(parsed))
	for key, value := range parsed {
		switch value.(type) {
		case map[string]any, []any:
			return nil, fmt.Errorf("answers file %s: %q must be a single value, not a list or a mapping", path, key)
		case nil:
			answers[key] = ""
		default:
			answers[key] = fmt.Sprint(value)
		}
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
	if def != "" {
		r.say("%s [%s]: ", label, def)
	} else {
		r.say("%s: ", label)
	}
	line, err := r.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", fmt.Errorf("read the answer for %s: %w", label, err)
	}
	switch line = strings.TrimSpace(line); {
	case line != "":
		return line, nil
	case def != "":
		return def, nil
	default:
		return "", fmt.Errorf("%s is required: pass --%s or set %q in the answers file", label, key, key)
	}
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
		r.say("%s (input is hidden): ", label)
		secret, err := r.term.ReadPassword()
		r.say("\n")
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
