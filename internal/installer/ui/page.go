package ui

import (
	"fmt"
	"unicode/utf8"
)

// Block is a piece of the explanation on a Page.
type Block struct {
	text string
	rows []KV
}

// Para is a paragraph of text. A newline in it starts a new row.
func Para(text string) Block { return Block{text: text} }

// Fields is a block of label/value rows, indented, with the labels muted and aligned.
func Fields(rows ...KV) Block { return Block{rows: rows} }

// Page starts a clean page of the current step, for one question: a title (bold, in the brand green) and the blocks under it, one
// blank line between them. Ask, Select, MultiSelect, Choose, Secret or Prompt then asks the question, set apart from the explanation by a
// blank line and a rule. Warn and Critical lines that come after the blocks are set off by a blank line too.
//
// A page is not a step: the step list stays as it is, and the Check, Warn, Critical and Keep lines of the page before it are kept for
// Close. The title, the paragraphs and the fields are for the full screen only: Page writes nothing in plain mode (and while the screen is
// suspended), so what a flow printed with Say stays what it was. What Close prints of the page that is current is the title, the blocks and
// the lines written after them, as text.
func (s *Screen) Page(title string, blocks ...Block) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.drawing() { // before newPage: it forgets what plain mode printed last, and plain output must not change
		return
	}
	s.newPage()
	s.paged, s.apart = true, true
	s.add(item{kind: itemTitle, text: oneLine(title)})
	for _, b := range blocks {
		switch {
		case len(b.rows) > 0:
			s.add(item{kind: itemText})
			s.addFields(b.rows)
		case b.text != "":
			s.add(item{kind: itemText})
			s.add(item{kind: itemText, text: clean(b.text)})
		}
	}
	s.draw()
}

// addFields adds the rows of a block of fields, with the labels in a column as wide as the longest.
func (s *Screen) addFields(rows []KV) {
	keyW := 0
	for _, kv := range rows {
		keyW = max(keyW, utf8.RuneCountInString(oneLine(kv.Key)))
	}
	for _, kv := range rows {
		s.add(item{kind: itemField, text: oneLine(kv.Key), desc: oneLine(kv.Value), pad: keyW})
	}
}

// Critical prints a warning that must not be missed, of the weight of "without this nothing can be restored": a red box on a full
// screen, a Warn line in plain mode (the text as given, a newline added when it is missing). Close prints it again on the normal screen,
// like a Warn. Use it for the few warnings that nothing may hide among the others.
func (s *Screen) Critical(format string, args ...any) {
	s.mark(itemCritical, fmt.Sprintf(format, args...))
}

// setApart sets the first question of a page that Page opened apart from the explanation above it: a blank line and a rule. The caller
// holds the lock.
func (s *Screen) setApart() {
	if !s.apart || !s.drawing() {
		return
	}
	s.apart = false
	s.endLine()
	s.blankLine()
	s.add(item{kind: itemRule})
}

// setOff puts a blank line between the text of a page that Page opened and the warning of kind (itemWarn or itemCritical) that follows it.
// A run of Warn lines stays together, and a critical box always stands apart. The caller holds the lock.
func (s *Screen) setOff(kind itemKind) {
	if !s.paged || len(s.items) == 0 {
		return
	}
	if kind == itemCritical || s.items[len(s.items)-1].kind != itemWarn {
		s.blankLine()
	}
}

// blankLine adds an empty row unless the page ends in one.
func (s *Screen) blankLine() {
	if n := len(s.items); n > 0 && s.items[n-1].kind == itemText && s.items[n-1].text == "" {
		return
	}
	s.add(item{kind: itemText})
}
