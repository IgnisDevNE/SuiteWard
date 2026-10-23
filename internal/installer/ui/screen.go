// Package ui draws the installer as a full-screen "window" in the SuiteWard colors, or as plain lines.
//
// A Screen is the console of one installer run. It is an io.Writer, so code that prints with fmt.Fprintf to a writer keeps working, and
// it has the calls of a screen: a step list (Steps, Step), lines (Say, Check, Warn, Keep), questions (Ask, Prompt, Choose, Secret),
// live lines (Progress, Spin) and a Summary. In full-screen mode it draws on the alternate screen: a frame with a title bar, the step
// column, a content column and a hint line. In plain mode it writes only text, with no escape sequence, and the lines of Say and Write
// are exactly what the caller wrote.
//
// What the two modes do with the calls:
//
//   - Say, Write: the text as given. Full screen splits it into lines (a pending line without its newline is kept, and the cursor
//     parks at its end), drops control characters, and lets a carriage return overwrite the pending line: the next character that is
//     written replaces it, and a newline right after the carriage return, even in the next write, ends it as it is.
//   - Check, Warn: a line with a glyph (✓ and !); plain mode prints the text as given (a newline is added when missing), without the
//     glyph. Close prints every one again, with its glyph, whatever step it was written in, like a Keep line.
//   - Steps, Step: the step column and "step i of N"; plain mode prints nothing, so a flow's plain output is unchanged. A new step
//     starts a clean page.
//   - Progress, Spin: one live row per label; plain mode prints a line only when the label or the status text changes.
//   - Keep: a line like Say that Close prints again on the normal screen, so it survives the alternate screen. Plain mode has nothing
//     to repeat: the line is already in the output.
//   - Summary: a screen of key/value rows and warnings, with no step column. Full screen waits for Enter so it can be read; its rows
//     and warnings are kept for Close, and a second Summary replaces the first. Plain mode prints "key: value" lines and the warnings
//     as given.
//   - Wait: holds the page until Enter, for a command that ends on text that must be read (a Summary would replace it). Close prints
//     the page as usual. Plain mode returns at once.
//   - Close: leaves the alternate screen, and prints on the normal screen what must not be lost: the Check, Warn and Keep lines of the
//     pages that are gone, then the current page in the order it was written (only its Check, Warn and Keep lines when a Summary was
//     shown), then the Summary. So a failure leaves the page it stopped on, and every check and warning of the run, in the scrollback.
//   - Suspend: while suspended, every call that writes text writes it straight to the terminal, as plain mode does, and the screen
//     keeps none of it: a child's output (setup prints a TOTP secret) is not redrawn after Resume or repeated by Close.
//
// Questions must go through Ask, Prompt, Choose and Secret so that the answer joins its question on the page; they read from the input
// that was passed to Open, and nothing else may write to the terminal while one is open. A prompt ends with the context's error when
// Options.Context is done. Display calls ignore a failed write, as the installer's say does; the first failure comes back from Close.
//
// A Screen is safe for concurrent use, except that prompts must be asked from one goroutine at a time. Close is not called by a panic
// on another goroutine: the deferred Close of the goroutine that opened the screen does not run then, and the terminal stays on the
// alternate screen.
//
// shortcut: the size is read once, a resize is not followed; the layout counts runes, not terminal columns, so wide characters misalign;
// the glyphs ◆ ● ○ have an ambiguous width in East Asian locales; every change redraws the whole frame; the content column keeps no
// scrollback, only the last rows that fit; a step list longer than the body is cut, and the steps beyond it are not drawn. Upgrade each
// when a real terminal shows the problem.
package ui

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	enterAltScreen = "\x1b[?1049h\x1b[?7l\x1b[?25l\x1b[2J" // alternate screen, no autowrap, hidden cursor, cleared
	leaveAltScreen = "\x1b[0m\x1b[?7h\x1b[?25h\x1b[?1049l" // reset colors, autowrap, shown cursor, normal screen

	maxItems = 500 // the page keeps this many lines; older ones are gone from the frame anyway
)

// Options is what Open needs.
type Options struct {
	Out     io.Writer  // the terminal; the screen draws nothing until it is opened
	In      io.Reader  // answers are read from it; pass the bufio.Reader the Resolver uses so that they share one buffer
	Version string     // shown in the title bar: "installer <Version>"
	Mode    Mode       // Env.Mode()
	Color   ColorLevel // Env.Color()
	Width   int        // the terminal's size; full screen needs at least 60x20
	Height  int
	// RestoreConsole undoes what the OS needed for escape sequences. Optional. Close calls it, and so does Open when it fails.
	RestoreConsole func() error
	// Context ends a prompt: Ask, Prompt, Choose and Summary return the context's error as soon as it is done. Optional; nil never
	// ends one. Pass the context that SIGINT cancels (cmd/suiteward traps it with signal.NotifyContext), or Ctrl-C does nothing at a
	// prompt until Enter. The read that was pending is abandoned in its goroutine and may still take a line from the input, so the
	// process is expected to exit and the input is not read again. Secret cannot be interrupted this way: its reader is the caller's.
	Context context.Context
	// InputIsTerminal says that In is a terminal. It does nothing yet.
	InputIsTerminal bool
}

// Choice is one numbered answer of Choose.
type Choice struct{ Name, Desc string }

// KV is a key/value row of the summary.
type KV struct{ Key, Value string }

// Screen is the installer's console. Open one, defer its Close, and call Close before printing an error to stderr: the error would
// vanish with the alternate screen otherwise.
type Screen struct {
	mu      sync.Mutex
	out     io.Writer
	in      *bufio.Reader
	ctx     context.Context
	version string
	color   ColorLevel
	w, h    int
	restore func() error
	err     error // the first write or restore failure

	full, suspended, closed bool

	steps []string
	cur   int // the current step, 1-based; 0 with no steps
	items []item
	// partial is the line that has no newline yet; it is the last row of the page and holds the cursor.
	partial   string
	pendingCR bool // the last character written was a carriage return: the line is overwritten by the next one, or ended by a newline
	keeping   bool // the items that Write commits now are Keep lines
	secret    bool // a hidden prompt is open
	spinning  bool
	spinText  string
	spinFor   time.Duration
	summary   bool
	waiting   bool // Wait is reading the Enter that ends it
	rows      []KV
	warns     []string
	// history is the Check, Warn and Keep lines of the pages that are gone, as Close prints them: the pages before the current one.
	history []string
	// summaryKeep is the last Summary's rows and warnings, printed after the page.
	summaryKeep []string

	plainSpin string            // plain mode: the spinner label last printed
	plainBars map[string]string // plain mode: the status text last printed for each bar
}

// Open returns a screen on o.Out. A full-screen one enters the alternate screen and draws its frame; call Close to leave it again.
// When Open fails it has already called o.RestoreConsole.
func Open(o Options) (*Screen, error) {
	if err := o.validate(); err != nil {
		if o.RestoreConsole != nil {
			err = errors.Join(err, o.RestoreConsole())
		}
		return nil, err
	}
	ctx := o.Context
	if ctx == nil {
		ctx = context.Background()
	}
	s := &Screen{
		out: o.Out, in: bufio.NewReader(o.In), ctx: ctx, version: oneLine(o.Version), color: o.Color, w: o.Width, h: o.Height,
		restore: o.RestoreConsole, full: o.Mode == ModeFull, plainBars: map[string]string{},
	}
	if s.full {
		s.write(enterAltScreen)
		s.draw()
	}
	return s, nil
}

func (o Options) validate() error {
	if o.Out == nil || o.In == nil {
		return errors.New("the screen needs an output and an input")
	}
	if o.Mode == ModeFull && (o.Width < minWidth || o.Height < minHeight) {
		return fmt.Errorf("the full-screen layout needs a terminal of at least %dx%d, got %dx%d", minWidth, minHeight, o.Width, o.Height)
	}
	return nil
}

// FullScreen reports whether the screen is drawing a frame (false in plain mode, and after Close).
func (s *Screen) FullScreen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.full
}

// drawing reports whether text is going to the frame; the caller holds the lock. Plain, closed and suspended screens write text
// straight to the terminal instead.
func (s *Screen) drawing() bool { return s.full && !s.suspended }

// write sends text to the terminal; the caller holds the lock.
func (s *Screen) write(text string) {
	_, err := io.WriteString(s.out, text)
	s.note(err)
}

func (s *Screen) note(err error) {
	if err != nil && s.err == nil {
		s.err = err
	}
}

// Write is the console as an io.Writer: Say with the text already formatted.
func (s *Screen) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drawing() {
		s.append(string(p))
		s.draw()
		return len(p), nil
	}
	n, err := s.out.Write(p)
	s.note(err)
	return n, err
}

// emit prints text as Write does; the caller holds the lock.
func (s *Screen) emit(text string) {
	if s.drawing() {
		s.append(text)
		s.draw()
		return
	}
	s.write(text)
}

// Say prints formatted text, as the installer's say does: a newline is only there when the format has one.
func (s *Screen) Say(format string, args ...any) {
	_, _ = fmt.Fprintf(s, format, args...) // a failed write is kept for Close
}

// Check prints a line that reports something that is fine.
func (s *Screen) Check(format string, args ...any) { s.mark(itemCheck, fmt.Sprintf(format, args...)) }

// Warn prints a line that the reader should notice. Close prints it again on the normal screen.
func (s *Screen) Warn(format string, args ...any) { s.mark(itemWarn, fmt.Sprintf(format, args...)) }

func (s *Screen) mark(kind itemKind, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.drawing() {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		s.write(text)
		return
	}
	text = clean(strings.TrimSuffix(text, "\n"))
	s.add(item{kind: kind, text: text})
	s.draw()
}

// Keep prints a line like Say and remembers it: Close prints it again on the normal screen, after the alternate screen is gone. Full
// screen ends the line if the text does not.
func (s *Screen) Keep(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.drawing() {
		s.write(text)
		return
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	s.endLine() // a pending line is not part of the Keep line: it stays a line of the page
	s.keeping = true
	s.emit(text)
	s.keeping = false
}

// endLine commits the pending line, if there is one, as a line of its own, and forgets a pending carriage return.
func (s *Screen) endLine() {
	if s.partial != "" {
		s.add(item{kind: itemText, text: s.partial})
	}
	s.partial, s.pendingCR = "", false
}

// Steps sets the names of the step list and makes the first one current. Plain mode prints nothing.
func (s *Screen) Steps(names ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.steps = s.steps[:0]
	for _, name := range names {
		s.steps = append(s.steps, oneLine(name))
	}
	s.cur = min(1, len(s.steps))
	s.newPage()
	s.draw()
}

// Step makes step n (1-based) current, marks the earlier ones done and starts a clean page. A step outside the list is a bug of the
// caller, so it panics.
func (s *Screen) Step(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 1 || n > len(s.steps) {
		panic(fmt.Sprintf("ui: step %d is outside the %d steps of the list", n, len(s.steps)))
	}
	s.cur = n
	s.newPage()
	s.draw()
}

func (s *Screen) newPage() {
	s.archive(s.items)
	s.items,s.partial, s.pendingCR, s.spinning, s.plainSpin = nil, "", false, false, ""
	clear(s.plainBars)
}

// Progress shows the bar of label at fraction (0 to 1; anything else is clamped) with a status text; an empty status shows the
// percentage. Calling it again with the same label updates the bar in place. Plain mode prints "label: status" when the text changes
// (the percentage moves in steps of ten there).
func (s *Screen) Progress(label string, fraction float64, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !(fraction > 0) { // not greater also catches NaN
		fraction = 0
	}
	fraction = math.Min(fraction, 1)
	if !s.drawing() {
		if status == "" {
			status = fmt.Sprintf("%d%%", int(fraction*100)/10*10)
		}
		if s.plainBars[label] != status {
			s.plainBars[label] = status
			s.write(label + ": " + status + "\n")
		}
		return
	}
	label, status = oneLine(label), oneLine(status)
	for i := range s.items {
		if it := &s.items[i]; it.kind == itemBar && it.text == label {
			it.frac, it.status = fraction, status
			s.draw()
			return
		}
	}
	s.add(item{kind: itemBar, text: label, frac: fraction, status: status})
	s.draw()
}

// Spin shows a spinner line with the label and the elapsed time, below everything else on the page. The caller ticks it: call Spin
// again with a longer elapsed time to move it (the frame of the animation comes from the elapsed time). Plain mode prints "label..."
// once, until EndSpin or another label.
func (s *Screen) Spin(label string, elapsed time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.drawing() {
		if s.plainSpin != label {
			s.plainSpin = label
			s.write(label + "...\n")
		}
		return
	}
	s.spinning, s.spinText, s.spinFor = true, oneLine(label), elapsed
	s.draw()
}

// EndSpin removes the spinner; report the outcome with Check or Warn.
func (s *Screen) EndSpin() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.plainSpin = ""
	if s.spinning {
		s.spinning = false
		s.draw()
	}
}

// readAnswer reads one line from the input. The last line may lack its newline; the end of the input is io.EOF.
func (s *Screen) readAnswer() (string, error) {
	line, err := s.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readLine reads one answer without the lock held, so that the screen stays usable while it waits, and gives up with the context's
// error when the context is done: the read goes on in its goroutine, abandoned (see Options.Context).
func (s *Screen) readLine() (string, error) {
	if err := s.ctx.Err(); err != nil {
		return "", err
	}
	if s.ctx.Done() == nil { // a context that is never done needs no goroutine
		return s.readAnswer()
	}
	type answer struct {
		line string
		err  error
	}
	got := make(chan answer, 1)
	go func() {
		line, err := s.readAnswer()
		got <- answer{line, err}
	}()
	select {
	case a := <-got:
		return a.line, a.err
	case <-s.ctx.Done():
		return "", s.ctx.Err()
	}
}

// Prompt prints text exactly as given, without a newline, and reads one line: the answer, trimmed. Full screen parks the cursor after
// the text and then shows the answer on that line; the end of the input is io.EOF, and a done context is its error.
func (s *Screen) Prompt(text string) (string, error) {
	s.mu.Lock()
	s.emit(text)
	s.mu.Unlock()
	line, err := s.readLine()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drawing() {
		s.emit(line + "\n") // a terminal echoes the typed line itself; the frame has to draw it
	}
	return line, nil
}

// Ask prints "label [def]: " ("label: " without a default) and returns the trimmed answer, def when it is empty.
func (s *Screen) Ask(label, def string) (string, error) {
	prompt := label + ": "
	if def != "" {
		prompt = fmt.Sprintf("%s [%s]: ", label, def)
	}
	line, err := s.Prompt(prompt)
	if err != nil {
		return "", fmt.Errorf("read the answer for %s: %w", label, err)
	}
	if line == "" {
		return def, nil
	}
	return line, nil
}

// Choose prints the label and the numbered choices with the default marked, then asks for a number until it gets one: typing the
// number of a choice answers it and an empty line takes the default. It returns the index of the choice, 0-based. An invalid answer
// is explained and asked again; the end of the input is an error that wraps io.EOF.
func (s *Screen) Choose(label string, choices []Choice, def int) (int, error) {
	if len(choices) == 0 || def < 0 || def >= len(choices) {
		return 0, fmt.Errorf("choose %q needs a default among its %d choices, got %d", label, len(choices), def)
	}
	s.mu.Lock()
	s.listChoices(label, choices, def)
	s.mu.Unlock()
	prompt := fmt.Sprintf("Choose [%d]: ", def+1)
	for {
		line, err := s.Prompt(prompt)
		if err != nil {
			return 0, fmt.Errorf("read the choice for %s: %w", label, err)
		}
		if n, ok := parseChoice(line, def, len(choices)); ok {
			return n, nil
		}
		s.Say("Enter a number from 1 to %d.\n", len(choices))
	}
}

// parseChoice reads an answer of n choices: empty is the default, otherwise a number from 1 to n.
func parseChoice(line string, def, n int) (int, bool) {
	if line == "" {
		return def, true
	}
	k, err := strconv.Atoi(line)
	if err != nil || k < 1 || k > n {
		return 0, false
	}
	return k - 1, true
}

func (s *Screen) listChoices(label string, choices []Choice, def int) {
	names := make([]string, len(choices))
	nameW := 0
	for i, c := range choices {
		names[i] = oneLine(c.Name)
		nameW = max(nameW, utf8.RuneCountInString(names[i]))
	}
	if !s.drawing() {
		s.write(label + "\n")
		for i, c := range choices {
			s.write(choiceLine(i+1, nameW, c.Name, c.Desc, i == def) + "\n")
		}
		return
	}
	s.add(item{kind: itemText, text: oneLine(label)})
	for i, c := range choices {
		s.add(item{kind: itemChoice, num: i + 1, text: names[i], desc: oneLine(c.Desc), pad: nameW, def: i == def})
	}
	s.draw()
}

// choiceLine is a choice as plain text: "1) name   description (default)".
func choiceLine(num, nameWidth int, name, desc string, def bool) string {
	mark := ""
	if def {
		mark = " (default)"
	}
	return fmt.Sprintf("%d) %-*s   %s%s", num, nameWidth, name, desc, mark)
}

// Secret prints "label (input is hidden): " and calls read, the caller's no-echo reader (term.ReadPassword on the terminal), then
// ends the line. The text is returned as read and never stored, shown or logged here. The read cannot be interrupted, so the hint
// of the frame offers Enter and not Ctrl-C while it is open, and Options.Context is not looked at until it returns.
func (s *Screen) Secret(label string, read func() ([]byte, error)) ([]byte, error) {
	if read == nil {
		return nil, fmt.Errorf("the secret %s needs a reader that does not echo", label)
	}
	s.mu.Lock()
	s.secret = true
	s.emit(label + " (input is hidden): ")
	s.mu.Unlock()
	secret, err := read()
	s.mu.Lock()
	s.secret = false
	s.emit("\n")
	s.mu.Unlock()
	return secret, err
}

// Summary shows key/value rows and warnings on a screen with no step column. Full screen waits for Enter, so the screen can be read
// before Close leaves it, and keeps the rows and warnings for Close to print on the normal screen (a second Summary replaces them):
// do not Keep them as well. Plain mode prints "key: value" lines and then the warnings as given. It returns an error only when
// waiting for Enter fails (a done context is one); the end of the input is not.
func (s *Screen) Summary(rows []KV, warnings []string) error {
	s.mu.Lock()
	if !s.drawing() {
		for _, r := range rows {
			s.write(r.Key + ": " + r.Value + "\n")
		}
		for _, w := range warnings {
			s.write(strings.TrimSuffix(w, "\n") + "\n")
		}
		s.mu.Unlock()
		return nil
	}
	s.summary, s.rows, s.warns, s.summaryKeep = true, nil, nil, nil
	for _, r := range rows {
		r = KV{oneLine(r.Key), oneLine(r.Value)}
		s.rows = append(s.rows, r)
		s.summaryKeep = append(s.summaryKeep, r.Key+": "+r.Value)
	}
	for _, w := range warnings {
		s.warns = append(s.warns, oneLine(w))
		s.summaryKeep = append(s.summaryKeep, oneLine(w))
	}
	s.draw()
	s.mu.Unlock()
	return s.waitForEnter()
}

// Wait holds the page on screen until Enter, so that it can be read before Close leaves the alternate screen, and Close then prints the
// page as usual: nothing is added to it. Plain mode has printed everything already and returns at once. It returns an error only when
// waiting for Enter fails (a done context is one); the end of the input is not.
func (s *Screen) Wait() error {
	s.mu.Lock()
	if !s.drawing() {
		s.mu.Unlock()
		return nil
	}
	s.waiting = true
	s.draw()
	s.mu.Unlock()
	err := s.waitForEnter()
	s.mu.Lock()
	s.waiting = false
	s.mu.Unlock()
	return err
}

// waitForEnter reads the line that ends a wait, without the lock held; the end of the input ends it too.
func (s *Screen) waitForEnter() error {
	if _, err := s.readLine(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("wait for Enter: %w", err)
	}
	return nil
}

// Suspend leaves the alternate screen and restores the normal terminal, for a child that needs the real TTY (setup prints a TOTP secret
// there). While suspended the screen draws nothing and every call that writes text writes it straight to the terminal, keeping none of
// it; Resume enters the alternate screen again and redraws the page as it was. Plain mode does nothing.
func (s *Screen) Suspend() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.full || s.suspended {
		return
	}
	s.suspended = true
	s.write(leaveAltScreen)
}

// Resume undoes Suspend.
func (s *Screen) Resume() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.full || !s.suspended {
		return
	}
	s.suspended = false
	s.write(enterAltScreen)
	s.draw()
}

// Close leaves the alternate screen, puts the console back, and prints on the normal screen what must not be lost (see the package
// documentation): the Check, Warn and Keep lines of the pages that are gone, then the current page in the order it was written, or
// only its Check, Warn and Keep lines when a Summary was shown, then the Summary. It is safe to call twice, so a deferred Close and an
// explicit one before an error is printed to stderr can both stand. After Close the screen writes plain text. It returns the first
// error the screen met while writing or restoring.
func (s *Screen) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.err
	}
	s.closed = true
	if s.full && !s.suspended {
		s.write(leaveAltScreen)
	}
	s.full = false
	if s.restore != nil {
		s.note(s.restore())
	}
	if lines := append(s.pageLines(), s.summaryKeep...); len(lines) > 0 {
		s.write(strings.Join(lines, "\n") + "\n")
	}
	return s.err
}

// pageLines is what Close prints before the Summary: the history, then the current page as plain text. A Summary replaces the page, so
// then only its Check, Warn and Keep lines stay, and the pending line and the spinner are gone.
func (s *Screen) pageLines() []string {
	lines := slices.Clone(s.history)
	for _, it := range s.items {
		if !s.summary || it.durable() {
			lines = append(lines, it.plainLines()...)
		}
	}
	if s.summary {
		return lines
	}
	if s.partial != "" {
		lines = append(lines, s.partial)
	}
	if s.spinning {
		lines = append(lines, s.spinText+"...")
	}
	return lines
}

// append adds written text to the page, as a terminal would show it: a newline ends the pending line, a carriage return makes the
// next character replace it (a progress line that rewrites itself ends up as its last version), a tab is a space, and other control
// characters, escape included, are dropped, so that text from a child or from the network cannot move the cursor or recolor the screen.
func (s *Screen) append(text string) {
	var b strings.Builder
	b.WriteString(s.partial)
	put := func(r rune) {
		if s.pendingCR {
			b.Reset()
			s.pendingCR = false
		}
		b.WriteRune(r)
	}
	for _, r := range text {
		switch {
		case r == '\n':
			s.pendingCR = false
			s.add(item{kind: itemText, text: b.String(), kept: s.keeping})
			b.Reset()
		case r == '\r':
			s.pendingCR = true
		case r == '\t':
			put(' ')
		case !unicode.IsControl(r):
			put(r)
		}
	}
	s.partial = b.String()
}

func (s *Screen) add(it item) {
	if s.items = append(s.items, it); len(s.items) > maxItems {
		cut := len(s.items) - maxItems
		s.archive(s.items[:cut])
		s.items = s.items[cut:]
	}
}

// archive moves the lines that must outlive their page, the Check, Warn and Keep lines, to the history that Close prints.
func (s *Screen) archive(items []item) {
	for _, it := range items {
		if it.durable() {
			s.history = append(s.history, it.plainLines()...)
		}
	}
}

// clean makes caller text safe to draw: tabs become spaces, other control characters are dropped, newlines stay.
func clean(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n':
			return r
		case r == '\t':
			return ' '
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, text)
}

// oneLine is clean for a text that must stay on one row: a newline becomes a space.
func oneLine(text string) string { return strings.ReplaceAll(clean(text), "\n", " ") }

// interruptGrace is how long a read that ended on a terminal waits for the context. It is unused yet.
var interruptGrace = 250 * time.Millisecond
