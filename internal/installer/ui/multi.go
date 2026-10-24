package ui

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Multi is a question whose answer is some of its choices. Screen.MultiSelect draws it as a list to be picked with the keys (the arrows
// move, space toggles a row, Enter confirms), and asks it by typing where keys cannot be read: the rows numbered, and the answer the
// numbers or the names of the rows, comma-separated.
type Multi struct {
	Label    string
	Choices  []Choice
	Defaults []int // the indexes of the rows that are picked at first
	// Min and Max bound the number of rows that may be picked. A Max of 0 has no upper bound. The list does not take an Enter below Min or
	// a pick above Max, and says why.
	Min, Max int
	// Hint is how a typed answer is written, in the typed question after the label: "numbers or owner/name, comma-separated, at most 20".
	// Empty takes "numbers or names, comma-separated", with ", at most Max" when there is one.
	Hint string
}

// Validate refuses a question that cannot be answered, which is a bug of the caller: no choices, a default that is not a choice or is listed
// twice, bounds that no number of picks can meet, and defaults that the bounds refuse (no defaults at all is fine: the user picks).
func (m Multi) Validate() error {
	n := len(m.Choices)
	switch {
	case n == 0:
		return fmt.Errorf("the multiple choice %q has no choices", m.Label)
	case m.Min < 0 || m.Max < 0:
		return fmt.Errorf("the multiple choice %q has a negative bound (min %d, max %d)", m.Label, m.Min, m.Max)
	case m.Max > 0 && m.Min > m.Max:
		return fmt.Errorf("the multiple choice %q needs at least %d and at most %d", m.Label, m.Min, m.Max)
	case m.Min > n:
		return fmt.Errorf("the multiple choice %q needs at least %d of its %d choices", m.Label, m.Min, n)
	}
	for i, d := range m.Defaults {
		if d < 0 || d >= n || slices.Contains(m.Defaults[:i], d) {
			return fmt.Errorf("the multiple choice %q has the default %d, which is not one of its %d choices or is listed twice", m.Label, d, n)
		}
	}
	if k := len(m.Defaults); k > 0 && m.countError(k) != nil {
		return fmt.Errorf("the multiple choice %q starts with %d rows picked, but it takes %s: its defaults must meet its bounds", m.Label, k, m.rangeText())
	}
	return nil
}

// hint is the text of the typed answer, in the typed question.
func (m Multi) hint() string {
	if m.Hint != "" {
		return m.Hint
	}
	if m.Max > 0 {
		return fmt.Sprintf("numbers or names, comma-separated, at most %d", m.Max)
	}
	return "numbers or names, comma-separated"
}

// TypedLabel is the label of the typed question, "label (hint)": what Resolver.Value asks, with the default after it.
func (m Multi) TypedLabel() string { return m.Label + " (" + m.hint() + ")" }

// rangeText is how many rows may be picked, in words.
func (m Multi) rangeText() string {
	switch {
	case m.Max == 0:
		return fmt.Sprintf("at least %d", m.Min)
	case m.Min == m.Max:
		return fmt.Sprintf("exactly %d", m.Min)
	case m.Min == 0:
		return fmt.Sprintf("at most %d", m.Max)
	}
	return fmt.Sprintf("between %d and %d", m.Min, m.Max)
}

// countError is the error of picking n rows, nil when n is within the bounds.
func (m Multi) countError(n int) error {
	if n < m.Min || (m.Max > 0 && n > m.Max) {
		return fmt.Errorf("%d chosen: pick %s", n, m.rangeText())
	}
	return nil
}

// Parse reads a typed answer: the numbers (from 1) or the names of the rows, comma-separated, a name in any case. Empty items are skipped
// and a row that is named twice is one row. It returns the indexes in the order they were typed, or an error for a token that is no row
// and for a number of rows outside the bounds.
// shortcut: a name that holds a comma cannot be typed; upgrade when a choice has one.
func (m Multi) Parse(answer string) ([]int, error) {
	var picked []int
	for _, token := range strings.Split(answer, ",") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		i := m.index(token)
		if i < 0 {
			return nil, fmt.Errorf("%q is not one of the choices", token)
		}
		if !slices.Contains(picked, i) {
			picked = append(picked, i)
		}
	}
	if err := m.countError(len(picked)); err != nil {
		return nil, err
	}
	return picked, nil
}

// index is the row a token names: its number, or its name in any case. -1 for none.
func (m Multi) index(token string) int {
	if n, err := strconv.Atoi(token); err == nil && n >= 1 && n <= len(m.Choices) {
		return n - 1
	}
	return slices.IndexFunc(m.Choices, func(c Choice) bool { return strings.EqualFold(c.Name, token) })
}

// MultiSelect asks m with the keys: the rows are drawn with a checkbox each and the defaults picked, the up and down arrows move the
// cursor (past an end it goes round), space toggles the row under it and Enter confirms. An Enter below m.Min, and a pick above m.Max,
// are refused with a line under the list. A list longer than the page scrolls with the cursor. The answer replaces the list on the
// question's line, and the call returns the indexes of the picked rows in the order of the list. The terminal is put in key mode
// (Options.KeyInput) for the length of the call and back whatever the way out, as for Select. Plain mode, a screen with no way to read
// keys and a terminal that cannot be put in key mode ask by typing: the rows numbered, then "Pick (hint) [defaults]: ", where an empty
// answer takes the defaults, and a bad answer is explained and asked again. The end of the input is an error that wraps io.EOF.
func (s *Screen) MultiSelect(m Multi) (picked []int, err error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if err := s.ctx.Err(); err != nil {
		return nil, fmt.Errorf("read the choices for %s: %w", m.Label, err)
	}
	restore := s.enterKeys()
	if restore == nil {
		return s.askMulti(m)
	}
	defer func() {
		if rerr := restore(); rerr != nil { // a terminal left in key mode must be reported, whatever the choices were
			picked, err = nil, errors.Join(err, fmt.Errorf("put the terminal input back: %w", rerr))
		}
	}()
	s.openMulti(m)
	err = s.readKeys("the choices for "+m.Label, func(k key) bool { return s.multiKey(m, k) })
	picked = s.closeMulti(err == nil)
	if err != nil {
		return nil, err
	}
	return picked, nil
}

// askMulti is MultiSelect by typing.
func (s *Screen) askMulti(m Multi) ([]int, error) {
	s.mu.Lock()
	s.listChoices(m.Label, m.Choices, m.Defaults...)
	s.mu.Unlock()
	numbers := make([]string, len(m.Defaults))
	for i, d := range m.Defaults {
		numbers[i] = strconv.Itoa(d + 1)
	}
	def := strings.Join(numbers, ",")
	prompt := "Pick (" + m.hint() + ")"
	if def != "" {
		prompt += " [" + def + "]"
	}
	prompt += ": "
	for {
		line, err := s.prompt(prompt, def != "")
		if err != nil {
			return nil, fmt.Errorf("read the choices for %s: %w", m.Label, err)
		}
		if line == "" {
			line = def
		}
		picked, err := m.Parse(line)
		if err == nil {
			return picked, nil
		}
		s.Say("%v.\n", err)
	}
}

// multiList is the list of an open MultiSelect: the screen draws it under the page.
type multiList struct {
	label string
	rows  []Choice // the choices, each as one line
	pad   int      // the width of the widest name
	on    []bool   // the rows that are picked
	cur   int      // the row under the cursor
	note  string   // why the last Enter or pick was refused; empty when nothing was
}

// count is the number of rows that are picked.
func (l *multiList) count() int {
	n := 0
	for _, on := range l.on {
		if on {
			n++
		}
	}
	return n
}

func (s *Screen) openMulti(m Multi) {
	l := &multiList{label: oneLine(m.Label), rows: make([]Choice, len(m.Choices)), on: make([]bool, len(m.Choices))}
	for i, c := range m.Choices {
		l.rows[i] = Choice{oneLine(c.Name), oneLine(c.Desc)}
		l.pad = max(l.pad, utf8.RuneCountInString(l.rows[i].Name))
	}
	for _, d := range m.Defaults {
		l.on[d] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setApart()
	s.multi = l
	s.draw()
}

// multiKey applies a key press to the open list and reports whether it was the Enter that ends it. Esc and the other keys do nothing.
func (s *Screen) multiKey(m Multi, k key) (done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, n := s.multi, len(m.Choices)
	switch k {
	case keyUp:
		l.cur, l.note = (l.cur+n-1)%n, ""
	case keyDown:
		l.cur, l.note = (l.cur+1)%n, ""
	case keySpace:
		l.note = ""
		if !l.on[l.cur] && m.Max > 0 && l.count() >= m.Max {
			l.note = "Pick " + m.rangeText() + "."
		} else {
			l.on[l.cur] = !l.on[l.cur]
		}
	case keyEnter:
		if m.countError(l.count()) == nil {
			return true
		}
		l.note = "Pick " + m.rangeText() + "."
	default:
		return false
	}
	s.draw()
	return false
}

// closeMulti ends the list and returns the picked indexes. With ok the list is replaced on the page by one line, "label: a, b" ("none"
// when nothing is picked); without, the question and its rows stay as text, like a numbered question that was cut short.
func (s *Screen) closeMulti(ok bool) []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.multi
	s.multi = nil
	var picked []int
	var names []string
	for i, on := range l.on {
		if on {
			picked = append(picked, i)
			names = append(names, l.rows[i].Name)
		}
	}
	switch {
	case ok && len(names) == 0:
		s.add(item{kind: itemText, text: l.label + ": none"})
	case ok:
		s.add(item{kind: itemText, text: l.label + ": " + strings.Join(names, ", ")})
	default:
		s.add(item{kind: itemText, text: l.label})
		for i := range l.rows {
			s.add(item{kind: itemText, text: l.plainRow(i)})
		}
	}
	s.draw()
	return picked
}

// plainRow is row i as text, "[x] name   description", the way Close prints a list that was cut short.
func (l *multiList) plainRow(i int) string {
	box := "[ ]"
	if l.on[i] {
		box = "[x]"
	}
	return strings.TrimRight(fmt.Sprintf("%s %-*s   %s", box, l.pad, l.rows[i].Name, l.rows[i].Desc), " ")
}

// minListRows is the fewest rows that the list keeps when the page above it leaves no room.
const minListRows = 5

// multiRows is the question and its list under the rows of the page above, which are above rows long (see listRows).
func (s *Screen) multiRows(above, width int) []row {
	l := s.multi
	rows := s.textRows(l.label, width)
	rows = append(rows, s.listRows(above+len(rows), len(l.rows), l.cur, func(i int) row { return s.multiRow(i, width) })...)
	if l.note != "" {
		rows = append(rows, s.markedRows(warnGlyph, toneAmber, toneAmber, l.note, width)...)
	}
	return rows
}

// listRows is the n rows of a list that ends the page, under the rows above it, which are above rows long; at draws row i. A list that
// does not fit in what is left of the body shows a window of it around row cur (the first rows when cur is -1), with a line above and
// below that say how many rows are out of sight.
func (s *Screen) listRows(above, n, cur int, at func(i int) row) []row {
	room := max(minListRows, s.h-frameRows-above-1) // one row is kept for the note
	top, shown := 0, n
	if n > room {
		shown = room - 2
		top = min(max(cur-shown/2, 0), n-shown)
	}
	var rows []row
	if shown < n {
		rows = append(rows, s.moreRow("↑", top, "above"))
	}
	for i := top; i < top+shown; i++ {
		rows = append(rows, at(i))
	}
	if shown < n {
		rows = append(rows, s.moreRow("↓", n-top-shown, "below"))
	}
	return rows
}

// moreRow tells how many rows of the list are out of sight on one side; it is blank when there are none, so the list does not jump.
func (s *Screen) moreRow(arrow string, count int, side string) row {
	b := s.rb()
	if count > 0 {
		b.put(toneMuted, false, fmt.Sprintf("  %s %d more %s", arrow, count, side))
	}
	return b.row()
}

// multiRow is "> [x] name   description": a mark for the cursor, a checkbox, the name in a column and the description muted. A picked
// row is green and the row under the cursor is bold.
func (s *Screen) multiRow(i, width int) row {
	l := s.multi
	box, mark, t := "[ ]", " ", toneDefault
	if l.on[i] {
		box, t = "[x]", toneGreen
	}
	if i == l.cur {
		mark = ">"
	}
	pad := min(l.pad, width/2)
	head := fmt.Sprintf("%s %s %-*s", mark, box, pad, fit(l.rows[i].Name, pad))
	b := s.rb()
	b.put(t, i == l.cur, head)
	if d := l.rows[i].Desc; d != "" {
		b.put(toneMuted, false, "   "+fit(d, width-utf8.RuneCountInString(head)-3))
	}
	return b.row()
}
