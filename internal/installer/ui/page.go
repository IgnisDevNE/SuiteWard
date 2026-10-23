package ui

// Block is a piece of the explanation on a Page.
type Block struct {
	text string
	rows []KV
}

// Para is a paragraph of text.
func Para(text string) Block { return Block{text: text} }

// Fields is a block of label/value rows.
func Fields(rows ...KV) Block { return Block{rows: rows} }

// Page starts a clean page of the current step with a title and the blocks under it.
func (s *Screen) Page(title string, blocks ...Block) {}

// Critical prints a warning that must not be missed.
func (s *Screen) Critical(format string, args ...any) {}
