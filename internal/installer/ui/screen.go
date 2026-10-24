// Package ui draws the installer as a full-screen "window" in the SuiteWard colors, or as plain lines.
//
// A Screen is the console of one installer run. It is an io.Writer, so code that prints with fmt.Fprintf to a writer keeps working, and
// it has the calls of a screen: a step list (Steps, Step), pages (Page), lines (Say, Check, Warn, Critical, Keep), questions (Ask, Prompt,
// Choose, Select, MultiSelect, Secret), live lines (Progress, Spin) and a Summary. In full-screen mode it draws on the alternate screen: a
// frame with a title bar, the step column, a content column and a hint line. In plain mode it writes only text, with no escape sequence,
// and the lines of Say and Write are exactly what the caller wrote.
//
// What the two modes do with the calls:
//
//   - Say, Write: the text as given. Full screen splits it into lines (a pending line without its newline is kept, and the cursor
//     parks at its end), drops control characters, and lets a carriage return overwrite the pending line: the next character that is
//     written replaces it, and a newline right after the carriage return, even in the next write, ends it as it is.
//   - Check, Warn: a line with a glyph (✓ and !), Warn in the warning amber; plain mode prints the text as given (a newline is added when
//     missing), without the glyph. Close prints every one again, with its glyph, whatever step it was written in, like a Keep line.
//   - Critical: a Warn for the few warnings that nothing may hide among the others (the recovery kit): a red box on a full screen, the
//     text as a Warn in plain mode. Close prints it again like a Warn.
//   - Page: a clean page of the step for one question: a title and paragraphs and label/value blocks (Para, Fields), then the question,
//     set apart from them by a rule. The step list and the Check, Warn, Critical and Keep lines of the page before it stay. Full screen
//     only: plain mode writes nothing, so the output of a script or an agent does not change.
//   - Steps, Step: the step column and "step i of N"; plain mode prints nothing, so a flow's plain output is unchanged. A new step
//     starts a clean page.
//   - Choose, Select: a numbered question that is answered by typing the number. Select, on a full screen whose terminal can be put in key
//     mode (Options.KeyInput), draws the choices with a highlighted row instead: the up and down arrows move it, Enter picks, and the
//     answer replaces the list on the question's line. Plain mode, and a screen that cannot read keys, ask Select by number as Choose.
//   - MultiSelect: Select with a checkbox per row: the arrows move, space toggles, Enter confirms, within the Min and Max of the caller.
//     Where keys cannot be read it asks by typing, "numbers or names, comma-separated"; Resolver.MultiSelect asks a plain screen the typed
//     question of Resolver.Value. Ask, and Choose, say in the hint line that Enter takes the default.
//   - Progress, Spin: one live row per label; plain mode prints a line only when the label or the status text changes.
//   - Keep: a line like Say that Close prints again on the normal screen, so it survives the alternate screen. Plain mode has nothing
//     to repeat: the line is already in the output.
//   - Summary: a screen of key/value rows and warnings, with no step column. Full screen waits for Enter so it can be read; its rows
//     and warnings are kept for Close, and a second Summary replaces the first. Plain mode prints "key: value" lines and the warnings
//     as given.
//   - Wait: holds the page until Enter, for a command that ends on text that must be read (a Summary would replace it). Close prints
//     the page as usual. Plain mode returns at once.
//   - Fail: shows the error of a command that failed under the page it stopped on, with "failed" in the title bar and the step marked, and
//     waits for Enter. Close does not print the error: the caller prints it, on stderr. Plain mode, an interrupt (a done context) and a
//     failed wait show nothing more and do not wait. On a terminal it first waits a moment for the signal of a Ctrl-C that
//     killed a child process (a Windows console), so that the command ends as interrupted and no failure page is drawn.
//   - Close: leaves the alternate screen, and prints on the normal screen what must not be lost: the Check, Warn and Keep lines of the
//     pages that are gone, then the current page in the order it was written (only its Check, Warn and Keep lines when a Summary was
//     shown), then the Summary. So a failure leaves the page it stopped on, and every check and warning of the run, in the scrollback.
//   - Suspend: while suspended, every call that writes text writes it straight to the terminal, as plain mode does, and the screen
//     keeps none of it: a child's output (setup prints a TOTP secret) is not redrawn after Resume or repeated by Close.
//
// Questions must go through Ask, Prompt, Choose, Select, MultiSelect and Secret so that the answer joins its question on the page; they
// read from the input that was passed to Open, and nothing else may write to the terminal while one is open. A prompt ends with the
// context's error when Options.Context is done. Display calls ignore a failed write, as the installer's say does; the first failure
// comes back from Close.
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
	// Context ends a prompt: Ask, Prompt, Choose, Select, Summary and Wait return the context's error as soon as it is done. Optional; nil never
	// ends one. Pass the context that SIGINT cancels (cmd/suiteward traps it with signal.NotifyContext), or Ctrl-C does nothing at a
	// prompt until Enter. The read that was pending is abandoned in its goroutine and may still take a line from the input, so the
	// process is expected to exit and the input is not read again. Secret cannot be interrupted this way: its reader is the caller's.
	Context context.Context
	// InputIsTerminal says that In is a terminal. On a Windows console, Ctrl-C ends the pending read with the end of the input before
	// the signal cancels Context, so a read that ends in an error waits a moment (interruptGrace) for the context and returns its
	// error, which is what lets the command report an interrupt. The end of an input that is not a terminal is the end of the input.
	InputIsTerminal bool
	// KeyInput puts the terminal's input in per-key mode for one Select: a key press is read when it is made, nothing is echoed, and an
	// arrow arrives as an escape sequence. Ctrl-C must stay a signal that cancels Context, not a character. restore puts the previous
	// mode back and is called once, when the Select ends, whatever the way. Optional; nil, or an error, makes Select ask by number.
	KeyInput func() (restore func() error, err error)
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
	keys    func() (func() error, error) // Options.KeyInput
	err     error                        // the first write or restore failure

	full, suspended, closed bool
	terminal                bool // the input is a terminal (Options.InputIsTerminal)
	selecting               bool // a Select list is open: the arrow keys move its highlight
	hasDef                  bool // the open question has a default: Enter takes it
	paged, apart            bool // Page opened the page; its first question is not asked yet, and is set apart

	steps []string
	cur   int // the current step, 1-based; 0 with no steps
	items []item
	// partial is the line that has no newline yet; it is the last row of the page and holds the cursor.
	partial   string
	pendingCR bool       // the last character written was a carriage return: the line is overwritten by the next one, or ended by a newline
	keeping   bool       // the items that Write commits now are Keep lines
	secret    bool       // a hidden prompt is open
	multi     *multiList // a MultiSelect list is open
	spinning  bool
	spinText  string
	spinFor   time.Duration
	summary   bool
	waiting   bool   // Wait is reading the Enter that ends it
	failure   string // Fail is showing this error under the page, and reading the Enter that ends it
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
		restore: o.RestoreConsole, keys: o.KeyInput, full: o.Mode == ModeFull, terminal: o.InputIsTerminal, plainBars: map[string]string{},
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
	if kind != itemCheck {
		s.setOff(kind)
	}
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
	s.items, s.partial, s.pendingCR, s.spinning, s.plainSpin = nil, "", false, false, ""
	s.paged, s.apart = false, false
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
func (s *Screen) readLine() (string, error) { return awaiting(s, s.readAnswer) }

// awaiting is readLine for any read of the input: it calls read in a goroutine and gives up with the context's error when the context is
// done, or soon after a read that failed on a terminal (afterTerminalError). The caller holds no lock.
func awaiting[T any](s *Screen, read func() (T, error)) (T, error) {
	var zero T
	if err := s.ctx.Err(); err != nil {
		return zero, err
	}
	if s.ctx.Done() == nil { // a context that is never done needs no goroutine
		return read()
	}
	type result struct {
		v   T
		err error
	}
	got := make(chan result, 1)
	go func() {
		v, err := read()
		got <- result{v, err}
	}()
	select {
	case r := <-got:
		if r.err != nil && s.terminal {
			return zero, s.afterTerminalError(r.err)
		}
		return r.v, r.err
	case <-s.ctx.Done():
		return zero, s.ctx.Err()
	}
}

// interruptGrace is how long a read that failed on a terminal waits for the context before its error is believed: on a Windows console
// the signal that cancels the context follows the aborted read by a few milliseconds. A variable so that a test does not wait for it.
var interruptGrace = 250 * time.Millisecond

// afterTerminalError is the context's error when the context is done within interruptGrace, and err otherwise.
func (s *Screen) afterTerminalError(err error) error {
	if s.interrupted() {
		return s.ctx.Err()
	}
	return err
}

// interrupted waits up to interruptGrace for the context to be done, and reports whether it is. A context that is never done is not waited
// for.
func (s *Screen) interrupted() bool {
	done := s.ctx.Done()
	if done == nil {
		return false
	}
	timer := time.NewTimer(interruptGrace)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// Prompt prints text exactly as given, without a newline, and reads one line: the answer, trimmed. Full screen parks the cursor after
// the text and then shows the answer on that line; the end of the input is io.EOF, and a done context is its error.
func (s *Screen) Prompt(text string) (string, error) { return s.prompt(text, false) }

// prompt is Prompt; hasDef says that the question has a default, which the hint line of the frame tells Enter takes.
func (s *Screen) prompt(text string, hasDef bool) (string, error) {
	s.mu.Lock()
	s.setApart()
	s.hasDef = hasDef
	s.emit(text)
	s.mu.Unlock()
	line, err := s.readLine()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hasDef = false
	if err != nil {
		return "", err
	}
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
	line, err := s.prompt(prompt, def != "")
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
	if err := checkChoices("choose", label, choices, def); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.listChoices(label, choices, def)
	s.mu.Unlock()
	prompt := fmt.Sprintf("Choose [%d]: ", def+1)
	for {
		line, err := s.prompt(prompt, true)
		if err != nil {
			return 0, fmt.Errorf("read the choice for %s: %w", label, err)
		}
		if n, ok := parseChoice(line, def, len(choices)); ok {
			return n, nil
		}
		s.Say("Enter a number from 1 to %d.\n", len(choices))
	}
}

// checkChoices refuses a list that has no choice, or a default that is not one of them: a bug of the caller.
func checkChoices(verb, label string, choices []Choice, def int) error {
	if len(choices) == 0 || def < 0 || def >= len(choices) {
		return fmt.Errorf("%s %q needs a default among its %d choices, got %d", verb, label, len(choices), def)
	}
	return nil
}

// Select asks like Choose with the arrow keys: the list is drawn with the default highlighted, the up and down arrows move the highlight
// (past an end it goes round) and Enter picks it. The answer replaces the list on the question's line, as Ask leaves its answer. It
// returns the index of the choice, 0-based. The terminal is put in key mode (Options.KeyInput) for the length of the call and back
// whatever the way out, a done context included. Plain mode, a screen with no way to read keys and a terminal that cannot be put in key
// mode ask by number, exactly as Choose does. The end of the input is an error that wraps io.EOF.
// shortcut: space and Esc do nothing; a list longer than the page shows its last rows; upgrade when a question needs one.
func (s *Screen) Select(label string, choices []Choice, def int) (idx int, err error) {
	if err := checkChoices("select", label, choices, def); err != nil {
		return 0, err
	}
	if err := s.ctx.Err(); err != nil {
		return 0, fmt.Errorf("read the choice for %s: %w", label, err)
	}
	restore := s.enterKeys()
	if restore == nil {
		return s.Choose(label, choices, def)
	}
	defer func() { // the one place the console is put back, so an abandoned read never has to
		if rerr := restore(); rerr != nil { // a terminal left in key mode must be reported, whatever the choice was
			idx, err = 0, errors.Join(err, fmt.Errorf("put the terminal input back: %w", rerr))
		}
	}()

	n, cur := len(choices), def
	s.openSelect(label, choices, def)
	if err := s.readKeys("the choice for "+label, func(k key) bool {
		switch k {
		case keyUp:
			cur = (cur + n - 1) % n
			s.moveSelect(n, cur)
		case keyDown:
			cur = (cur + 1) % n
			s.moveSelect(n, cur)
		case keyEnter:
			return true
		}
		return false
	}); err != nil {
		s.closeSelect(0, "")
		return 0, err
	}
	s.closeSelect(n, oneLine(label)+": "+oneLine(choices[cur].Name))
	return cur, nil
}

// enterKeys puts the terminal in key mode and returns the call that puts it back. It returns nil where keys cannot be read (plain mode,
// a screen with no way to put the terminal in key mode, a terminal that refuses): the caller asks by typing.
// shortcut: the reason a terminal refuses is not reported, only that the keys cannot be read; the typed question still works.
func (s *Screen) enterKeys() func() error {
	s.mu.Lock()
	keys := s.keys
	if !s.drawing() {
		keys = nil
	}
	s.mu.Unlock()
	if keys == nil {
		return nil
	}
	restore, err := keys()
	if err != nil {
		return nil
	}
	return restore
}

// readKeys reads key presses until handle returns true. what names the question in the error that ends it: the input that ends, a done
// context, or a Ctrl-C that arrived as a character (see errCtrlC). Space, Esc and the keys that have no meaning are handle's to ignore.
func (s *Screen) readKeys(what string, handle func(k key) (done bool)) error {
	for {
		k, err := awaiting(s, func() (key, error) { return readKey(s.in) })
		switch {
		case err != nil:
			return fmt.Errorf("read %s: %w", what, err)
		case k == keyCtrlC:
			return fmt.Errorf("read %s: %w", what, s.afterTerminalError(errCtrlC))
		case handle(k):
			return nil
		}
	}
}

// errCtrlC is the end of a Select by a Ctrl-C that arrived as a character and that no signal followed (see afterTerminalError). A
// terminal that keeps its signals raises one for the context to follow instead, so this is only a terminal that cannot: the command
// fails, and does not report an interrupt.
var errCtrlC = errors.New("interrupted by Ctrl-C, which reached the program as a key and not as a signal")

// openSelect draws the question and the list with the default highlighted.
func (s *Screen) openSelect(label string, choices []Choice, def int) {
	names, nameW := choiceNames(choices)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setApart()
	s.selecting = true
	s.add(item{kind: itemText, text: oneLine(label)})
	for i, c := range choices {
		s.add(item{kind: itemChoice, num: i + 1, text: names[i], desc: oneLine(c.Desc), pad: nameW, def: i == def, radio: true, sel: i == def})
	}
	s.draw()
}

// moveSelect highlights row to of the n that end the page: nothing else writes while a question is open.
func (s *Screen) moveSelect(n, to int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.items[len(s.items)-n:]
	for i := range rows {
		rows[i].sel = i == to
	}
	s.draw()
}

// closeSelect ends the key list. With a picked answer it replaces the question and its n rows with one line; with none, the list stays
// as it was drawn, like a numbered question that was cut short.
func (s *Screen) closeSelect(n int, answer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selecting = false
	if answer != "" {
		s.items = append(s.items[:len(s.items)-n-1], item{kind: itemText, text: answer})
	}
	s.draw()
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

// choiceNames is the names of the choices as one line each, and the width of the widest.
func choiceNames(choices []Choice) (names []string, width int) {
	names = make([]string, len(choices))
	for i, c := range choices {
		names[i] = oneLine(c.Name)
		width = max(width, utf8.RuneCountInString(names[i]))
	}
	return names, width
}

// listChoices writes the label and the numbered choices, with the defaults marked; the caller holds the lock.
func (s *Screen) listChoices(label string, choices []Choice, defs ...int) {
	names, nameW := choiceNames(choices)
	if !s.drawing() {
		s.write(label + "\n")
		for i, c := range choices {
			s.write(choiceLine(i+1, nameW, c.Name, c.Desc, slices.Contains(defs, i)) + "\n")
		}
		return
	}
	s.setApart()
	s.add(item{kind: itemText, text: oneLine(label)})
	for i, c := range choices {
		s.add(item{kind: itemChoice, num: i + 1, text: names[i], desc: oneLine(c.Desc), pad: nameW, def: slices.Contains(defs, i)})
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
	s.setApart()
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

// HiddenLine is declared for its tests; it reads nothing yet.
func (s *Screen) HiddenLine(ctx context.Context, label string, fallback func() ([]byte, error)) ([]byte, error) {
	return nil, errors.New("HiddenLine is not implemented")
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

// Fail shows the error of a command that failed under the page the command stopped on, with "failed" in the title bar and the current step
// marked, and waits for Enter so that it can be read before Close leaves the alternate screen. It reports whether it showed the error:
// plain mode, a suspended or closed screen and a done context (Ctrl-C) show nothing and do not wait. On a terminal Fail first waits up to
// interruptGrace for the context: a Windows console delivers a Ctrl-C to the child process before the signal reaches this one, so the
// error of a child that it killed arrives first, and no failure page is drawn for an interrupt. A wait that fails (Ctrl-C at the page,
// the end of the input) only ends it: the error is the command's own, and the caller prints it after Close, which does not repeat it.
func (s *Screen) Fail(text string) bool {
	s.mu.Lock()
	drawing := s.drawing()
	s.mu.Unlock()
	if s.ctx.Err() != nil || !drawing {
		return false
	}
	if s.terminal && s.interrupted() { // the signal of a Ctrl-C can follow the child's error: wait for it, as a prompt does
		return false
	}
	s.mu.Lock()
	if !s.drawing() {
		s.mu.Unlock()
		return false
	}
	s.endLine()
	s.failure = strings.TrimSpace(clean(text))
	if s.failure == "" {
		s.failure = "the command failed"
	}
	s.draw()
	s.mu.Unlock()
	_ = s.waitForEnter() // a failed wait ends it; the error that is shown is the command's
	return true
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
