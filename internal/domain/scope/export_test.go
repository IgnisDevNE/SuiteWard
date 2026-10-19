package scope

import "slices"

// Present reports whether the declaration file exists. Only the tests read it: production code compares scopes by
// Digest, which covers the presence byte and the raw bytes.
func (d Declaration) Present() bool { return d.present }

// Raw returns a copy of the exact declaration bytes, nil when absent.
func (d Declaration) Raw() []byte { return slices.Clone(d.raw) }
