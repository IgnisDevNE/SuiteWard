package scope

import (
	"errors"
)

// DeclarationPath is the repository-root path of the scope declaration.
const DeclarationPath = ".suiteward.yml"

// ErrInvalidDeclaration reports a declaration that does not satisfy schema
// version 1.
var ErrInvalidDeclaration = errors.New("invalid scope declaration")

// Declaration is a validated .suiteward.yml, or its absence.
type Declaration struct{}

// ParseDeclaration validates raw as a schema version 1 declaration.
func ParseDeclaration(raw []byte) (Declaration, error) {
	return Declaration{}, errors.New("ParseDeclaration: not implemented")
}

// NoDeclaration is the declaration of a repository without .suiteward.yml.
func NoDeclaration() Declaration {
	panic("not implemented")
}

// Present reports whether the declaration file exists.
func (d Declaration) Present() bool {
	panic("not implemented")
}

// Raw returns a copy of the exact declaration bytes, nil when absent.
func (d Declaration) Raw() []byte {
	panic("not implemented")
}

// Include returns a copy of the include patterns in input order.
func (d Declaration) Include() []Pattern {
	panic("not implemented")
}

// Exclude returns a copy of the exclude patterns in input order.
func (d Declaration) Exclude() []Pattern {
	panic("not implemented")
}

// Runner returns a copy of the runner patterns in input order.
func (d Declaration) Runner() []Pattern {
	panic("not implemented")
}
