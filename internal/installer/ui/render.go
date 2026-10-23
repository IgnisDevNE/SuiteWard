package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// The layout, in terminal columns. Row 1 is the top border, row 2 the title bar, row 3 a rule, then the body, a rule, the hint line
// and the bottom border: six rows around the body.
const (
	frameRows     = 6
	stepsWidth    = 18 // the step column, between the left border and the vertical rule
	barCells      = 20 // cells of a progress bar, at most
	minBarCells   = 10
	maxLabelWidth = 14 // progress labels are cut to this
	spinnerFrames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
)

type itemKind int

const (
	itemText itemKind = iota
	itemCheck
	itemWarn
	itemChoice
	itemBar
)

// The glyphs in front of a Check and a Warn line: the frame draws them, and Close prints the same ones.
const (
	checkGlyph = "✓"
	warnGlyph  = "!"
)

// item is one thing on the page of a step.
type item struct {
	kind   itemKind
	text   string // the line; the name of a choice; the label of a bar
	desc   string // a choice's description
	num    int    // a choice's number
	pad    int    // the width of the choices' name column
	def    bool   // the default choice
	frac   float64
	status string // a bar's status; empty shows the percentage
	kept   bool   // a Keep line: it outlives its page
}

// durable says that Close prints the item whatever page the run ends on: a Check, a Warn or a Keep line.
func (it item) durable() bool { return it.kept || it.kind == itemCheck || it.kind == itemWarn }

// plainLines is the item as text, as Close prints it: a Check and a Warn line with the glyph the frame draws.
func (it item) plainLines() []string {
	switch it.kind {
	case itemText:
		return strings.Split(it.text, "\n")
	case itemCheck:
		return markedLines(checkGlyph, it.text)
	case itemWarn:
		return markedLines(warnGlyph, it.text)
	case itemChoice:
		return []string{choiceLine(it.num, it.pad, it.text, it.desc, it.def)}
	case itemBar:
		status := it.status
		if status == "" {
			status = fmt.Sprintf("%d%%", int(it.frac*100))
		}
		return []string{it.text + ": " + status}
	default:
		panic(fmt.Sprintf("ui: unknown item kind %d", it.kind))
	}
}

// row is one line of the content or the step column: its text with the color sequences in place, and its width in columns.
type row struct {
	s string
	w int
}

func (r row) pad(width int) string { return r.s + strings.Repeat(" ", max(0, width-r.w)) }

type rowBuilder struct {
	lv ColorLevel
	sb strings.Builder
	w  int
}

func (b *rowBuilder) put(t tone, bold bool, text string) {
	b.sb.WriteString(b.lv.paint(t, bold, text))
	b.w += utf8.RuneCountInString(text)
}

func (b *rowBuilder) row() row { return row{b.sb.String(), b.w} }

func (s *Screen) rb() *rowBuilder { return &rowBuilder{lv: s.color} }

// frame is what a full redraw shows: every terminal row, and where the cursor goes.
type frame struct {
	rows           []string
	cursor         bool // a line is pending: show the cursor, parked at curRow and curCol (1-based)
	curRow, curCol int
}

// draw paints the whole frame with absolute row addresses and one write; the caller holds the lock.
func (s *Screen) draw() {
	if !s.drawing() {
		return
	}
	f := s.frame()
	var b strings.Builder
	for i, r := range f.rows {
		fmt.Fprintf(&b, "\x1b[%d;1H%s", i+1, r)
	}
	if f.cursor {
		fmt.Fprintf(&b, "\x1b[%d;%dH\x1b[?25h", f.curRow, f.curCol)
	} else {
		b.WriteString("\x1b[?25l")
	}
	s.write(b.String())
}

// frame lays out the whole terminal: exactly s.h rows of exactly s.w columns. The caller holds the lock.
func (s *Screen) frame() frame {
	bodyH, inner := s.h-frameRows, s.w-2
	summary := s.summary && s.failure == "" // a failure is shown on the page it stopped on, even after a summary
	steps := !summary
	textW := inner - 2
	if steps {
		textW = inner - stepsWidth - 1 - 2
	}
	var content []row
	cursor := -1
	if summary {
		content = s.summaryRows(textW)
	} else {
		content, cursor = s.contentRows(textW)
	}
	cut := max(0, len(content)-bodyH) // shortcut: no scrollback, the last rows that fit are shown
	content, cursor = content[cut:], cursor-cut
	hint := "Ctrl-C to quit"
	switch {
	case summary, s.waiting, s.failure != "":
		hint = "Enter to finish"
	case s.secret: // the hidden read cannot be interrupted
		hint = "Enter to confirm"
	case cursor >= 0:
		hint = "Enter to confirm · Ctrl-C to quit"
	}

	vert := s.color.paint(toneGreen, false, "│")
	rows := []string{s.border("╭", "─", "╮", inner), s.titleRow(inner), s.rule("├", "┬", inner, steps)}
	for i := range bodyH {
		var c row
		if i < len(content) {
			c = content[i]
		}
		line := vert
		if steps {
			line += s.stepCell(i).pad(stepsWidth) + vert
		}
		rows = append(rows, line+" "+c.pad(textW)+" "+vert)
	}
	hb := s.rb()
	hb.put(toneMuted, false, fit(hint, inner-2))
	rows = append(rows, s.rule("├", "┴", inner, steps), vert+" "+hb.row().pad(inner-2)+" "+vert, s.border("╰", "─", "╯", inner))

	f := frame{rows: rows}
	if cursor >= 0 && !summary {
		f.cursor, f.curRow, f.curCol = true, 4+cursor, stepsWidth+4+content[cursor].w
	}
	return f
}

// border is a full row of the frame in green: a corner, the line, a corner.
func (s *Screen) border(left, line, right string, inner int) string {
	return s.color.paint(toneGreen, false, left+strings.Repeat(line, inner)+right)
}

// rule is the horizontal rule under the title bar or above the hint; with the step column it carries the junction of the vertical rule.
func (s *Screen) rule(left, junction string, inner int, steps bool) string {
	if !steps {
		return s.border(left, "─", "┤", inner)
	}
	return s.color.paint(toneGreen, false, left+strings.Repeat("─", stepsWidth)+junction+strings.Repeat("─", inner-stepsWidth-1)+"┤")
}

// titleRow is "◆ SuiteWard installer <version>" on the left and the step counter (or "done") on the right.
func (s *Screen) titleRow(inner int) string {
	right, rightTone := "", toneDefault
	switch {
	case s.failure != "":
		right, rightTone = "failed", toneAmber
	case s.summary:
		right, rightTone = "done", toneGreen
	case len(s.steps) > 0:
		right, rightTone = fmt.Sprintf("step %d of %d", s.cur, len(s.steps)), toneMuted
	}
	const brand = "◆ SuiteWard"
	tail := " installer"
	if s.version != "" {
		tail += " " + s.version
	}
	b := s.rb()
	b.put(toneGreen, false, brand)
	b.put(toneDefault, false, fit(tail, inner-2-1-utf8.RuneCountInString(right)-utf8.RuneCountInString(brand)))
	gap := strings.Repeat(" ", max(1, inner-2-b.w-utf8.RuneCountInString(right)))
	b.put(toneDefault, false, gap)
	b.put(rightTone, false, right)
	return s.color.paint(toneGreen, false, "│") + " " + b.row().pad(inner-2) + " " + s.color.paint(toneGreen, false, "│")
}

// stepCell is row i of the step column: done steps are green with a check, the current one green and bold with a dot, the rest muted.
func (s *Screen) stepCell(i int) row {
	b := s.rb()
	if i >= len(s.steps) {
		return b.row()
	}
	glyph, t, bold := "○", toneMuted, false
	switch n := i + 1; {
	case n < s.cur:
		glyph, t = "✓", toneGreen
	case n == s.cur && s.failure != "":
		glyph, t, bold = warnGlyph, toneAmber, true // the step the command failed in
	case n == s.cur:
		glyph, t, bold = "●", toneGreen, true
	}
	b.put(toneDefault, false, " ")
	b.put(t, bold, glyph+" "+fit(s.steps[i], stepsWidth-4))
	return b.row()
}

// contentRows is the page of the current step. The pending line (and with it the cursor) comes after the lines, and the spinner,
// which is a live line, after that. cursor is the index of the row that holds the cursor, -1 with no pending line.
func (s *Screen) contentRows(width int) (rows []row, cursor int) {
	labelW := 0
	for _, it := range s.items {
		if it.kind == itemBar {
			labelW = min(maxLabelWidth, max(labelW, utf8.RuneCountInString(it.text)))
		}
	}
	for _, it := range s.items {
		rows = append(rows, s.itemRows(it, width, labelW)...)
	}
	cursor = -1
	if s.partial != "" {
		rows = append(rows, s.textRows(s.partial, width)...)
		cursor = len(rows) - 1
	}
	if s.spinning {
		rows = append(rows, s.spinRow(width))
	}
	if s.failure != "" { // the error, under the page it stopped on, in the style of a warning; no line is pending, so no cursor
		if len(rows) > 0 {
			rows = append(rows, row{})
		}
		rows, cursor = append(rows, s.markedRows(warnGlyph, toneAmber, s.failure, width)...), -1
	}
	return rows, cursor
}

func (s *Screen) itemRows(it item, width, labelW int) []row {
	switch it.kind {
	case itemText:
		return s.textRows(it.text, width)
	case itemCheck:
		return s.markedRows(checkGlyph, toneGreen, it.text, width)
	case itemWarn:
		return s.markedRows(warnGlyph, toneAmber, it.text, width)
	case itemChoice:
		return []row{s.choiceRow(it, width)}
	case itemBar:
		return []row{s.barRow(it, width, labelW)}
	default:
		panic(fmt.Sprintf("ui: unknown item kind %d", it.kind))
	}
}

func (s *Screen) textRows(text string, width int) []row {
	var rows []row
	for _, l := range wrapLines(text, width) {
		b := s.rb()
		b.put(toneDefault, false, l)
		rows = append(rows, b.row())
	}
	return rows
}

// markedRows is a text with a glyph in front of its first line; the other lines are indented under the text.
func (s *Screen) markedRows(glyph string, t tone, text string, width int) []row {
	var rows []row
	for i, l := range wrapLines(text, width-2) {
		b := s.rb()
		if i == 0 {
			b.put(t, false, glyph+" ")
		} else {
			b.put(toneDefault, false, "  ")
		}
		b.put(toneDefault, false, l)
		rows = append(rows, b.row())
	}
	return rows
}

// markedLines is markedRows as text: the glyph in front of the first line, the other lines indented under the text.
func markedLines(glyph, text string) []string {
	lines := strings.Split(text, "\n")
	lines[0] = glyph + " " + lines[0]
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = "  " + lines[i]
		}
	}
	return lines
}

// choiceRow is "1) name   description"; the default is green and says so.
func (s *Screen) choiceRow(it item, width int) row {
	b := s.rb()
	pad := min(it.pad, width/2)
	head := fmt.Sprintf("%d) %-*s", it.num, pad, fit(it.text, pad))
	t, marker := toneDefault, ""
	if it.def {
		t, marker = toneGreen, " (default)"
	}
	b.put(t, false, head)
	b.put(toneMuted, false, "   "+fit(it.desc, width-utf8.RuneCountInString(head)-3-len(marker)))
	b.put(toneGreen, false, marker)
	return b.row()
}

// barRow is "label  [████░░░░] status": the label column is as wide as the longest label of the page.
func (s *Screen) barRow(it item, width, labelW int) row {
	cells := min(barCells, max(minBarCells, width-labelW-5-8))
	filled := int(it.frac * float64(cells))
	status := it.status
	if status == "" {
		status = fmt.Sprintf("%d%%", int(it.frac*100))
	}
	b := s.rb()
	b.put(toneDefault, false, fmt.Sprintf("%-*s  [", labelW, fit(it.text, labelW)))
	b.put(toneGreen, false, strings.Repeat("█", filled))
	b.put(toneMuted, false, strings.Repeat("░", cells-filled))
	b.put(toneDefault, false, "] "+fit(status, width-labelW-5-cells))
	return b.row()
}

// spinRow is "⠋ label… (mm:ss)"; the animation frame follows the elapsed time, ten frames a second.
func (s *Screen) spinRow(width int) row {
	elapsed := max(time.Duration(0), s.spinFor)
	frames := []rune(spinnerFrames)
	secs := int(elapsed / time.Second)
	clock := fmt.Sprintf(" (%02d:%02d)", secs/60, secs%60)
	b := s.rb()
	b.put(toneGreen, false, string(frames[int(elapsed/(100*time.Millisecond))%len(frames)]))
	b.put(toneDefault, false, " "+fit(s.spinText+"…", width-2-len(clock))+clock)
	return b.row()
}

// summaryRows is the key/value rows, then the warnings after a blank line.
func (s *Screen) summaryRows(width int) []row {
	keyW := 0
	for _, kv := range s.rows {
		keyW = min(width/3, max(keyW, utf8.RuneCountInString(kv.Key)))
	}
	var rows []row
	for _, kv := range s.rows {
		for i, l := range wrap(kv.Value, width-keyW-2) {
			b := s.rb()
			if i == 0 {
				b.put(toneMuted, false, fmt.Sprintf("%-*s  ", keyW, fit(kv.Key, keyW)))
			} else {
				b.put(toneDefault, false, strings.Repeat(" ", keyW+2))
			}
			b.put(toneDefault, false, l)
			rows = append(rows, b.row())
		}
	}
	if len(s.rows) > 0 && len(s.warns) > 0 {
		rows = append(rows, row{})
	}
	for _, w := range s.warns {
		rows = append(rows, s.markedRows(warnGlyph, toneAmber, w, width)...)
	}
	return rows
}

// wrapLines wraps every line of text.
func wrapLines(text string, width int) []string {
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		lines = append(lines, wrap(para, width)...)
	}
	return lines
}

// wrap breaks one line of text at spaces into lines of at most width runes. The indent of the text stays on every line, a word that is
// longer than a line is cut, and a trailing space stays (the cursor of a prompt goes after it).
func wrap(text string, width int) []string {
	width = max(1, width)
	rest := strings.TrimLeft(text, " ")
	indent := len(text) - len(rest)
	if indent >= width {
		indent, rest = 0, text
	}
	pad := strings.Repeat(" ", indent)
	room := width - indent
	var lines []string
	var cur []rune
	for _, word := range strings.Split(rest, " ") {
		w := []rune(word)
		for len(w) > room {
			if len(cur) > 0 {
				lines, cur = append(lines, pad+string(cur)), nil
			}
			lines, w = append(lines, pad+string(w[:room])), w[room:]
		}
		if len(cur) > 0 && len(cur)+1+len(w) > room {
			lines, cur = append(lines, pad+string(cur)), nil
		}
		switch {
		case len(cur) > 0:
			cur = append(append(cur, ' '), w...)
		case len(w) > 0:
			cur = w
		}
	}
	if len(cur) > 0 || len(lines) == 0 {
		lines = append(lines, pad+string(cur))
	}
	return lines
}

// fit cuts text to width runes, ending in an ellipsis when it had to cut.
func fit(text string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(text)
	if len(r) <= width {
		return text
	}
	return string(r[:width-1]) + "…"
}
