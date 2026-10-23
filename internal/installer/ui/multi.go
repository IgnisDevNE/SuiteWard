package ui

import "errors"

// Multi is a question with several answers.
type Multi struct {
	Label    string
	Choices  []Choice
	Defaults []int
	Min, Max int
	Hint     string
}

// Validate refuses a question that cannot be answered.
func (m Multi) Validate() error { return nil }

// Parse reads a typed answer.
func (m Multi) Parse(answer string) ([]int, error) { return nil, errors.New("not built") }

// TypedLabel is the text of the typed question.
func (m Multi) TypedLabel() string { return m.Label }

// MultiSelect asks a Multi.
func (s *Screen) MultiSelect(m Multi) ([]int, error) { return nil, errors.New("not built") }
