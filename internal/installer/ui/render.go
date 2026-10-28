package ui

import (
	"fmt"
	"slices"
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
	itemTitle    // the title of a Page: a section title
	itemField    // a label/value row of a Page, indented
	itemRule     // the rule that sets a question apart: a drawing, with no text of its own
	itemCritical // a warning that must not be missed: a red box
)

// The glyphs in front of a Check and a Warn line: the frame draws them, and Close prints the same ones.
const (
	checkGlyph = "✓"
	warnGlyph  = "!"
)

// item is one thing on the page of a step.
type item struct {
	kind   itemKind
	text   string // the line; the name of a choice; the label of a bar; the label of a field
	desc   string // a choice's description; the value of a field
	num    int    // a choice's number
	pad    int    // the width of the choices' name column, or of the labels of a block of fields
	def    bool   // the default choice
	radio  bool   // a row of a Select list: a radio glyph in front instead of the number
	sel    bool   // the highlighted row of a Select list
	frac   float64
	status string // a bar's status; empty shows the percentage
	kept   bool   // a Keep line: it outlives its page
}

// durable says that Close prints the item whatever page the run ends on: a Check, a Warn, a Critical or a Keep line.
func (it item) durable() bool {
	return it.kept || it.kind == itemCheck || it.kind == itemWarn || it.kind == itemCritical
}

// plainLines is the item as text, as Close prints it: a Check and a Warn line with the glyph the frame draws.
func (it item) plainLines() []string {
	switch it.kind {
	case itemText:
		return strings.Split(it.text, "\n")
	case itemCheck:
		return markedLines(checkGlyph, it.text)
	case itemWarn, itemCritical:
		return markedLines(warnGlyph, it.text)
	case itemTitle:
		return []string{it.text}
	case itemField:
		return []string{fmt.Sprintf("  %-*s  %s", it.pad, it.text, it.desc)}
	case itemRule:
		return nil
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
	s    string
	w    int
	keep keepLevel // how far a page that is trimmed to fit may go in trimming the row
	grp  int       // the rows of one warning share a number, so that a warning is trimmed whole
}

// keepLevel says whether a page that is taller than the body may trim a row.
type keepLevel int

const (
	keepNone keepLevel = iota // the row is explanation, which goes first
	keepWarn                  // the row is part of a warning, which goes after it, whole
	keepRow                   // the row is part of a summary row that a window too small for all of them drops, whole, after the warnings
	keepBox                   // the row is part of a critical box or of a key row of a summary, which never goes
)

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

func (b *rowBuilder) row() row { return row{s: b.sb.String(), w: b.w} }

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
	page := pageRows{cursor: -1, tail: -1}
	if summary {
		page.rows = s.summaryRows(textW)
		page.tail, page.summary = len(page.rows), true
	} else {
		page = s.contentRows(textW)
	}
	content, cursor := s.fitBody(page, bodyH, textW)
	hint := "Ctrl-C to quit"
	switch {
	case summary, s.waiting, s.failure != "":
		hint = "Enter to finish"
	case s.secret: // the hidden read cannot be interrupted
		hint = "Enter to confirm"
	case s.multi != nil:
		hint = "↑/↓ move · Space toggle · Enter confirm · Ctrl-C quit"
	case s.selN > 0:
		hint = "↑/↓ to choose · Enter to confirm · Ctrl-C to quit"
	case cursor >= 0 && s.hasDef:
		hint = "Enter takes the default · Ctrl-C to quit"
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
		right, rightTone = "failed", toneRed
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
		glyph, t, bold = warnGlyph, toneRed, true // the step the command failed in
	case n == s.cur:
		glyph, t, bold = "●", toneGreen, true
	}
	b.put(toneDefault, false, " ")
	b.put(t, bold, glyph+" "+fit(s.steps[i], stepsWidth-4))
	return b.row()
}

// pageRows is the rows of a page, and what a page that is taller than the body must not lose.
type pageRows struct {
	rows   []row
	cursor int // the row that holds the cursor, -1 with no pending line
	head   int // the rows of the title of a Page, which stay at the top; 0 for a page that Page did not open
	// tail is the first row of what the page waits on, which stays at the bottom: the rule above a question, the error of a command that
	// failed, or the end of a page that waits for Enter. -1 for a page that waits on nothing, which shows its last rows.
	tail    int
	summary bool // the page is a summary: it has no title, and what it trims is its warnings and then its rows (see trim)
}

// contentRows is the page of the current step. The pending line (and with it the cursor) comes after the lines, and the spinner,
// which is a live line, after that.
func (s *Screen) contentRows(width int) pageRows {
	labelW := 0
	for _, it := range s.items {
		if it.kind == itemBar {
			labelW = min(maxLabelWidth, max(labelW, utf8.RuneCountInString(it.text)))
		}
	}
	p := pageRows{cursor: -1, tail: -1}
	listed := s.items[:len(s.items)-s.selN] // the rows of an open Select list are drawn below, as a window of the list
	for i, it := range listed {
		if it.kind == itemRule && p.tail < 0 {
			p.tail = len(p.rows)
		}
		rows := s.itemRows(it, width, labelW)
		if i == 0 && it.kind == itemTitle {
			p.head = len(rows)
		}
		for j := range rows {
			switch it.kind {
			case itemWarn:
				rows[j].keep, rows[j].grp = keepWarn, i+1
			case itemCritical:
				rows[j].keep = keepBox
			}
		}
		p.rows = append(p.rows, rows...)
	}
	if s.selN > 0 {
		choices := s.items[len(listed):]
		cur := slices.IndexFunc(choices, func(it item) bool { return it.sel })
		p.rows = append(p.rows, s.listRows(len(p.rows), len(choices), cur, func(i int) row { return s.choiceRow(choices[i], width) })...)
	}
	if s.selNote != "" {
		p.rows = append(p.rows, s.markedRows(warnGlyph, toneAmber, toneAmber, s.selNote, width)...)
	}
	if s.multi != nil {
		p.rows = append(p.rows, s.multiRows(len(p.rows), width)...)
	}
	if s.partial != "" {
		p.rows = append(p.rows, s.textRows(s.partial, width)...)
		p.cursor = len(p.rows) - 1
	}
	if s.spinning {
		p.rows = append(p.rows, s.spinRow(width))
	}
	if s.failure != "" { // the error, under the page it stopped on, marked in the red of the failed step; no line is pending, so no cursor
		if p.tail < 0 {
			p.tail = len(p.rows)
		}
		if len(p.rows) > 0 {
			p.rows = append(p.rows, row{})
		}
		p.rows, p.cursor = append(p.rows, s.markedRows(warnGlyph, toneRed, toneDefault, s.failure, width)...), -1
	}
	if p.tail < 0 && s.waiting {
		p.tail = len(p.rows)
	}
	return p
}

// trimmedRow is the row that says a page was trimmed to fit.
func (s *Screen) trimmedRow(width int) row {
	b := s.rb()
	b.put(toneMuted, false, fit("… enlarge the window to read the rest", width))
	return b.row()
}

// fitBody is the rows of a page that show in a body of bodyH rows, and the row of the cursor. A page that is taller shows its last rows,
// as a terminal would (shortcut: no scrollback), except a page that Page opened and that waits on something: it keeps its title at the top
// and what it waits on at the bottom, and trims what is between them (see trim). A summary keeps its key rows and trims what is below
// them. When the title, the question and the boxes do not fit together, the last rows show, behind a first row that says that rows are hidden.
func (s *Screen) fitBody(p pageRows, bodyH, width int) ([]row, int) {
	n := len(p.rows)
	if n <= bodyH {
		return p.rows, p.cursor
	}
	if (p.head > 0 || p.summary) && p.tail >= p.head {
		if gone, first, ok := p.trim(n - bodyH); ok {
			rows, cursor := make([]row, 0, bodyH), -1
			for i, r := range p.rows {
				if i == first {
					rows = append(rows, s.trimmedRow(width))
				}
				if gone[i] {
					continue
				}
				if i == p.cursor {
					cursor = len(rows)
				}
				rows = append(rows, r)
			}
			return rows, cursor
		}
	}
	cut := n - bodyH
	if bodyH < 2 { // no room for a row that says so beside one that is read
		return p.rows[cut:], p.cursor - cut
	}
	cursor := p.cursor - cut // the row that says that rows are hidden takes the place of the first of the rows that show
	if cursor == 0 {
		cursor = -1
	}
	return append([]row{s.trimmedRow(width)}, p.rows[cut+1:]...), cursor
}

// trim chooses the rows between the title and what the page waits on that go, so that need rows fewer remain, in this order:
//  1. the blank rows, from the end, the one above the rule last: no word is lost;
//  2. the explanation, from the end, behind a row that says so (first is the row of the first one that goes, where that row is drawn);
//  3. whole warnings, from the end, behind that row too;
//  4. the whole rows of a summary that are not key rows, from the end, behind that row too.
//
// A critical box and the key rows of a summary are never trimmed. ok is false when that is not enough.
func (p pageRows) trim(need int) (gone []bool, first int, ok bool) {
	gone, first = make([]bool, len(p.rows)), -1
	drop := func(i int) { gone[i], need = true, need-1 }
	blank := func(i int) bool { return !gone[i] && p.rows[i].w == 0 }
	for i := p.tail - 2; i >= p.head && need > 0; i-- {
		if blank(i) {
			drop(i)
		}
	}
	if need > 0 && blank(p.tail-1) {
		drop(p.tail - 1)
	}
	if need <= 0 {
		return gone, -1, true
	}
	need++                 // the row that says that words are missing takes one
	words := func(i int) { // a row that goes with its words: the row that says so is drawn where the first of them was
		drop(i)
		if first < 0 || i < first {
			first = i
		}
	}
	for i := p.tail - 1; i >= p.head && need > 0; i-- {
		if !gone[i] && p.rows[i].keep == keepNone && p.rows[i].w > 0 {
			words(i)
		}
	}
	for _, level := range []keepLevel{keepWarn, keepRow} {
		for i := p.tail - 1; i >= p.head && need > 0; i-- {
			if g := p.rows[i].grp; !gone[i] && p.rows[i].keep == level {
				for j := i; j >= p.head && p.rows[j].grp == g; j-- {
					words(j)
				}
			}
		}
	}
	return gone, first, need <= 0
}

func (s *Screen) itemRows(it item, width, labelW int) []row {
	switch it.kind {
	case itemText:
		return s.textRows(it.text, width)
	case itemCheck:
		return s.markedRows(checkGlyph, toneGreen, toneDefault, it.text, width)
	case itemWarn:
		return s.markedRows(warnGlyph, toneAmber, toneAmber, it.text, width)
	case itemChoice:
		return []row{s.choiceRow(it, width)}
	case itemBar:
		return []row{s.barRow(it, width, labelW)}
	case itemTitle:
		return s.styledRows(toneGreen, true, it.text, width)
	case itemField:
		return s.fieldRows(it.text, it.desc, min(width/3, it.pad), 2, width)
	case itemRule:
		return s.styledRows(toneMuted, false, strings.Repeat("─", width), width)
	case itemCritical:
		return s.criticalRows(it.text, width)
	default:
		panic(fmt.Sprintf("ui: unknown item kind %d", it.kind))
	}
}

// styledRows is a text wrapped to the column, every row in one tone.
func (s *Screen) styledRows(t tone, bold bool, text string, width int) []row {
	var rows []row
	for _, l := range wrapLines(text, width) {
		b := s.rb()
		b.put(t, bold, l)
		rows = append(rows, b.row())
	}
	return rows
}

// criticalRows is a box with a red border around the text, as wide as the column; the text keeps the terminal's color, in bold.
func (s *Screen) criticalRows(text string, width int) []row {
	inner := max(1, width-4)
	edge := func(left, right string) row {
		b := s.rb()
		b.put(toneRed, false, left+strings.Repeat("─", width-2)+right)
		return b.row()
	}
	rows := []row{edge("╭", "╮")}
	for _, l := range wrapLines(text, inner) {
		b := s.rb()
		b.put(toneRed, false, "│")
		b.put(toneDefault, false, " ")
		b.put(toneDefault, true, l)
		b.put(toneDefault, false, strings.Repeat(" ", inner-utf8.RuneCountInString(l))+" ")
		b.put(toneRed, false, "│")
		rows = append(rows, b.row())
	}
	return append(rows, edge("╰", "╯"))
}

// fieldRows is a label/value row: the label muted in a column of keyW, the value wrapped under itself. A block of fields is indented.
func (s *Screen) fieldRows(key, value string, keyW, indent, width int) []row {
	var rows []row
	for i, l := range wrap(value, width-indent-keyW-2) {
		b := s.rb()
		b.put(toneDefault, false, strings.Repeat(" ", indent))
		if i == 0 {
			b.put(toneMuted, false, fmt.Sprintf("%-*s  ", keyW, fit(key, keyW)))
		} else {
			b.put(toneDefault, false, strings.Repeat(" ", keyW+2))
		}
		b.put(toneDefault, false, l)
		rows = append(rows, b.row())
	}
	return rows
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
func (s *Screen) markedRows(glyph string, glyphTone, textTone tone, text string, width int) []row {
	var rows []row
	for i, l := range wrapLines(text, width-2) {
		b := s.rb()
		if i == 0 {
			b.put(glyphTone, false, glyph+" ")
		} else {
			b.put(toneDefault, false, "  ")
		}
		b.put(textTone, false, l)
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

// choiceRow is "1) name   description"; the default is green and says so. A row of a Select list has a glyph instead of the number,
// "● name" for the highlighted row, green and bold, and "○ name" for the others.
func (s *Screen) choiceRow(it item, width int) row {
	b := s.rb()
	pad := min(it.pad, width/2)
	name := fmt.Sprintf("%-*s", pad, fit(it.text, pad))
	head, t, bold, marker := fmt.Sprintf("%d) %s", it.num, name), toneDefault, false, ""
	switch {
	case it.radio && it.sel:
		head, t, bold = "● "+name, toneGreen, true
	case it.radio:
		head = "○ " + name
	case it.def:
		t = toneGreen
	}
	if it.def {
		marker = " (default)"
	}
	b.put(t, bold, head)
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

// keyRows are the rows of a summary that a window too small for all of it keeps: what runs, where it answers, and what doctor found.
// shortcut: the keys are the names the installer gives its rows, spelled here because Summary takes rows and warnings and nothing about
// them. Upgrade when a summary needs to say which of its rows matter. The older names (Tier, Instance) stay until every summary uses the
// plain ones, so that the order in which the commands are renamed does not matter.
var keyRows = []string{"Installation type", "Tier", "Instance ID", "Instance", "Status", "Doctor"}

// summaryRows is the key/value rows, then the warnings after a blank line. A row and a warning are trimmed whole (fitBody): the warnings
// first, from the end, then the rows that are not key rows.
func (s *Screen) summaryRows(width int) []row {
	keyW := 0
	for _, kv := range s.rows {
		keyW = min(width/3, max(keyW, utf8.RuneCountInString(kv.Key)))
	}
	var rows []row
	for i, kv := range s.rows {
		keep := keepRow
		if slices.Contains(keyRows, kv.Key) {
			keep = keepBox
		}
		for _, r := range s.fieldRows(kv.Key, kv.Value, keyW, 0, width) {
			r.keep, r.grp = keep, i+1
			rows = append(rows, r)
		}
	}
	if len(s.rows) > 0 && len(s.warns) > 0 {
		rows = append(rows, row{})
	}
	for i, w := range s.warns {
		for _, r := range s.markedRows(warnGlyph, toneAmber, toneAmber, w, width) {
			r.keep, r.grp = keepWarn, len(s.rows)+i+1
			rows = append(rows, r)
		}
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
// longer than a line is cut (after a path separator when one fits, see breakAt), and a trailing space stays (the cursor of a prompt goes after it).
// A bullet (a line that starts with "- " after its indent) hangs: what it wraps to is indented two more, under its text and not under the dash.
func wrap(text string, width int) []string {
	width = max(1, width)
	rest := strings.TrimLeft(text, " ")
	indent := len(text) - len(rest)
	if indent >= width {
		indent, rest = 0, text
	}
	pad := strings.Repeat(" ", indent)
	if body, bullet := strings.CutPrefix(rest, "- "); bullet && indent+2 < width {
		lines := wrap(pad+body, width-2)
		for i, l := range lines {
			lead := "  "
			if i == 0 {
				lead = "- "
			}
			lines[i] = pad + lead + strings.TrimPrefix(l, pad)
		}
		return lines
	}
	room := width - indent
	var lines []string
	var cur []rune
	for _, word := range strings.Split(rest, " ") {
		w := []rune(word)
		for len(w) > room {
			if len(cur) > 0 {
				lines, cur = append(lines, pad+string(cur)), nil
			}
			cut := breakAt(w, room)
			lines, w = append(lines, pad+string(w[:cut])), w[cut:]
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

// breakAt is where a word that is longer than room is cut: after the last "\" or "/" that fits, so that a path breaks between names
// (a separator at the very start does not count, it would leave a line of one character), and at room, in the middle of the word, when
// there is none. The row holds room runes at most.
func breakAt(w []rune, room int) int {
	for i := room - 1; i > 0; i-- {
		if w[i] == '\\' || w[i] == '/' {
			return i + 1
		}
	}
	return room
}
