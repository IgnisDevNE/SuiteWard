package installer

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakeTerminal returns a Terminal fed by input, and the buffer the prompts are written to.
func fakeTerminal(tty bool, input string) (Terminal, *bytes.Buffer) {
	out := &bytes.Buffer{}
	term := Terminal{In: strings.NewReader(input), Out: out, IsTerminal: tty}
	term.ReadPassword = func() ([]byte, error) { return []byte("typed-secret"), nil }
	return term, out
}

func TestValuePrecedenceFlagThenAnswersThenPrompt(t *testing.T) {
	flags := map[string]string{"runtime": "docker"}
	answers := map[string]string{"runtime": "podman", "tier": "isolated"}

	term, out := fakeTerminal(true, "co-located\n")
	r := newTestResolver(t, flags, answers, term)
	if got, err := r.Value("runtime", "Runtime", ""); err != nil || got != "docker" {
		t.Fatalf("flag must win over the answers file: %q, %v", got, err)
	}
	if got, err := r.Value("tier", "Tier", ""); err != nil || got != "isolated" {
		t.Fatalf("the answers file must win over a prompt: %q, %v", got, err)
	}
	if out.Len() != 0 {
		t.Fatalf("a question that has an answer was prompted: %q", out)
	}
	if got, err := r.Value("other", "Other", ""); err != nil || got != "co-located" {
		t.Fatalf("the prompt answer was not used: %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "Other") {
		t.Fatalf("the prompt does not show the label: %q", out)
	}
}

func TestValuePromptDefault(t *testing.T) {
	term, out := fakeTerminal(true, "\n")
	got, err := newTestResolver(t, nil, nil, term).Value("http-port", "HTTP port", "8081")
	if err != nil || got != "8081" {
		t.Fatalf("an empty line must take the default: %q, %v", got, err)
	}
	if !strings.Contains(out.String(), "8081") {
		t.Fatalf("the prompt does not show the default: %q", out)
	}
}

func TestValuePromptWithoutDefaultNeedsAnAnswer(t *testing.T) {
	term, _ := fakeTerminal(true, "\n")
	if _, err := newTestResolver(t, nil, nil, term).Value("owner", "Owner", ""); err == nil {
		t.Fatal("an empty answer to a question without default was accepted")
	}
	term, _ = fakeTerminal(true, "")
	if _, err := newTestResolver(t, nil, nil, term).Value("owner", "Owner", ""); err == nil {
		t.Fatal("end of input was accepted as an answer")
	}
}

func TestValueWithoutTerminalNamesTheFlagAndTheKey(t *testing.T) {
	term, out := fakeTerminal(false, "typed-on-stdin\n")
	_, err := newTestResolver(t, nil, nil, term).Value("job-timeout", "Job timeout", "")
	if err == nil {
		t.Fatal("a missing answer without a terminal must fail")
	}
	if !strings.Contains(err.Error(), "--job-timeout") || !strings.Contains(err.Error(), `"job-timeout"`) {
		t.Fatalf("the error does not name the flag and the answers key: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("prompted without a terminal: %q", out)
	}
}

func TestValueWithoutTerminalTakesTheDefault(t *testing.T) {
	term, _ := fakeTerminal(false, "")
	got, err := newTestResolver(t, nil, nil, term).Value("http-port", "HTTP port", "8081")
	if err != nil || got != "8081" {
		t.Fatalf("a default needs no input: %q, %v", got, err)
	}
}

func TestIntResolution(t *testing.T) {
	term, _ := fakeTerminal(false, "")
	r := newTestResolver(t, map[string]string{"http-port": "18081", "mcp-port": "x"}, nil, term)
	if got, err := r.Int("http-port", "HTTP port", 8081); err != nil || got != 18081 {
		t.Fatalf("got %d, %v", got, err)
	}
	if got, err := r.Int("other-port", "Other", 9000); err != nil || got != 9000 {
		t.Fatalf("default: got %d, %v", got, err)
	}
	if _, err := r.Int("mcp-port", "MCP port", 8091); err == nil || !strings.Contains(err.Error(), "mcp-port") {
		t.Fatalf("a non-number must name its key: %v", err)
	}
}

func TestSecretIsNeverTakenFromAFlag(t *testing.T) {
	term, _ := fakeTerminal(true, "")
	r := newTestResolver(t, map[string]string{"github-app-key": "-----BEGIN PRIVATE KEY-----"}, nil, term)
	_, err := r.Secret("github-app-key", "App key")
	if err == nil || !strings.Contains(err.Error(), "argument") {
		t.Fatalf("a secret on the command line must be refused: %v", err)
	}
}

func TestSecretSources(t *testing.T) {
	t.Run("answers file", func(t *testing.T) {
		term, _ := fakeTerminal(false, "")
		got, err := newTestResolver(t, nil, map[string]string{"token": "from-file"}, term).Secret("token", "Token")
		if err != nil || string(got) != "from-file" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("terminal prompt without echo", func(t *testing.T) {
		term, out := fakeTerminal(true, "")
		got, err := newTestResolver(t, nil, nil, term).Secret("token", "Token")
		if err != nil || string(got) != "typed-secret" {
			t.Fatalf("got %q, %v", got, err)
		}
		if strings.Contains(out.String(), "typed-secret") || !strings.Contains(out.String(), "Token") {
			t.Fatalf("the prompt must show the label and never the secret: %q", out)
		}
	})
	t.Run("stdin without a terminal", func(t *testing.T) {
		term, _ := fakeTerminal(false, "line one\nline two\n")
		got, err := newTestResolver(t, nil, nil, term).Secret("pem", "PEM")
		if err != nil || string(got) != "line one\nline two" {
			t.Fatalf("got %q, %v (stdin is read whole, minus its final line ending)", got, err)
		}
	})
	t.Run("empty stdin", func(t *testing.T) {
		term, _ := fakeTerminal(false, "")
		_, err := newTestResolver(t, nil, nil, term).Secret("pem", "PEM")
		if err == nil || !strings.Contains(err.Error(), `"pem"`) {
			t.Fatalf("an empty secret must fail naming its key: %v", err)
		}
	})
}

func TestFlagsSetReturnsOnlyFlagsTheUserSet(t *testing.T) {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.Int("http-port", 8081, "")
	fs.String("runtime", "podman", "")
	fs.String("owner", "", "")
	if err := fs.Parse([]string{"--http-port", "18081", "--owner", ""}); err != nil {
		t.Fatal(err)
	}
	got := FlagsSet(fs)
	if len(got) != 2 || got["http-port"] != "18081" || got["owner"] != "" {
		t.Fatalf("got %v", got)
	}
	if _, ok := got["runtime"]; ok {
		t.Fatal("a flag left at its default was reported as set")
	}
}

func TestLoadAnswers(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	got, err := LoadAnswers(write("ok.yaml", "runtime: podman\nhttp-port: 18081\nrepositories: acme/api,acme/web\nverbose: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got["runtime"] != "podman" || got["http-port"] != "18081" || got["repositories"] != "acme/api,acme/web" || got["verbose"] != "true" {
		t.Fatalf("got %v", got)
	}
	if _, err := LoadAnswers(write("nested.yaml", "github:\n  app: 1\n")); err == nil {
		t.Fatal("a nested answers file was accepted")
	}
	if _, err := LoadAnswers(write("list.yaml", "- a\n- b\n")); err == nil {
		t.Fatal("a list was accepted as an answers file")
	}
	if _, err := LoadAnswers(filepath.Join(dir, "missing.yaml")); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing file must be reported as one: %v", err)
	}
	empty, err := LoadAnswers(write("empty.yaml", ""))
	if err != nil || len(empty) != 0 {
		t.Fatalf("an empty file holds no answers: %v, %v", empty, err)
	}
}

func newTestResolver(t *testing.T, flags, answers map[string]string, term Terminal) *Resolver {
	t.Helper()
	r, err := NewResolver(flags, answers, term)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNewResolverRefusesAnIncompleteTerminal(t *testing.T) {
	complete, _ := fakeTerminal(true, "")
	for name, mutate := range map[string]func(*Terminal){
		"no input":    func(term *Terminal) { term.In = nil },
		"no output":   func(term *Terminal) { term.Out = nil },
		"no password": func(term *Terminal) { term.ReadPassword = nil },
	} {
		term := complete
		mutate(&term)
		if _, err := NewResolver(nil, nil, term); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Without a terminal nothing is read through ReadPassword, so it may be absent.
	noTTY, _ := fakeTerminal(false, "")
	noTTY.ReadPassword = nil
	if _, err := NewResolver(nil, nil, noTTY); err != nil {
		t.Errorf("a terminal-less console without ReadPassword was refused: %v", err)
	}
}

// A value is the text the user wrote: a number-like token must not be rewritten by the YAML decoder.
func TestLoadAnswersKeepsTheTextOfEveryScalar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answers.yaml")
	content := "version: 1.10\ntoken: 123456789012345678901234567890123456789012345678901234567890123456\nhex: 0x10\nexp: 1e5\noct: 0o17\nflag: yes\nempty:\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadAnswers(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"version": "1.10", "token": strings.Repeat("1234567890", 6) + "123456", "hex": "0x10", "exp": "1e5", "oct": "0o17", "flag": "yes", "empty": "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
}

func TestLoadAnswersRefusesAFileOthersCanRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes do not apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "answers.yaml")
	if err := os.WriteFile(path, []byte("runtime: podman\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o640, 0o604, 0o644, 0o666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		_, err := LoadAnswers(path)
		if err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("mode %o: got %v, want a refusal that names chmod 600", mode, err)
		}
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAnswers(path); err != nil {
		t.Errorf("an owner-only file was refused: %v", err)
	}
}
