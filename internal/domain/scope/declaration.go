package scope

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"

	"go.yaml.in/yaml/v3"
)

// DeclarationPath is the repository-root path of the scope declaration.
const DeclarationPath = ".suiteward.yml"

// ErrInvalidDeclaration reports a declaration that does not satisfy schema
// version 1.
var ErrInvalidDeclaration = errors.New("invalid scope declaration")

// Declaration is a validated .suiteward.yml, or its absence.
type Declaration struct {
	present bool
	raw     []byte
	include []Pattern
	exclude []Pattern
	runner  []Pattern
}

// ParseDeclaration validates raw as a schema version 1 declaration: a single
// YAML mapping with the integer key version (1) and the optional pattern
// sequences include, exclude and runner, and nothing else. The mapping is
// walked as nodes, not decoded into a struct: yaml.v3 would coerce any scalar
// into a string, a whole float into an int, and expand merge keys (<<) into
// keys the file does not spell out.
func ParseDeclaration(raw []byte) (Declaration, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return Declaration{}, fmt.Errorf("%w: empty", ErrInvalidDeclaration)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return Declaration{}, fmt.Errorf("%w: no document", ErrInvalidDeclaration)
		}
		return Declaration{}, fmt.Errorf("%w: %v", ErrInvalidDeclaration, err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Declaration{}, fmt.Errorf("%w: more than one YAML document", ErrInvalidDeclaration)
	}

	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return Declaration{}, fmt.Errorf("%w: the document must be a mapping", ErrInvalidDeclaration)
	}
	fields := make(map[string]yaml.Node, len(top.Content)/2)
	for i := 0; i < len(top.Content); i += 2 {
		key, value := top.Content[i], top.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			return Declaration{}, fmt.Errorf("%w: unsupported key at line %d", ErrInvalidDeclaration, key.Line)
		}
		switch key.Value {
		case "version", "include", "exclude", "runner":
		default:
			return Declaration{}, fmt.Errorf("%w: unknown key %q at line %d", ErrInvalidDeclaration, key.Value, key.Line)
		}
		if _, dup := fields[key.Value]; dup {
			return Declaration{}, fmt.Errorf("%w: key %q appears twice", ErrInvalidDeclaration, key.Value)
		}
		fields[key.Value] = *value
	}

	if err := checkVersion(fields["version"]); err != nil {
		return Declaration{}, err
	}
	include, err := parseList("include", fields["include"])
	if err != nil {
		return Declaration{}, err
	}
	exclude, err := parseList("exclude", fields["exclude"])
	if err != nil {
		return Declaration{}, err
	}
	runner, err := parseList("runner", fields["runner"])
	if err != nil {
		return Declaration{}, err
	}
	return Declaration{present: true, raw: slices.Clone(raw), include: include, exclude: exclude, runner: runner}, nil
}

func checkVersion(node yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!int" {
		return fmt.Errorf("%w: version must be the integer 1", ErrInvalidDeclaration)
	}
	var version int
	if err := node.Decode(&version); err != nil || version != 1 {
		return fmt.Errorf("%w: version must be the integer 1, got %q", ErrInvalidDeclaration, node.Value)
	}
	return nil
}

// parseList validates an optional sequence of distinct pattern strings; an
// absent key (a zero node) is an empty list.
func parseList(key string, node yaml.Node) ([]Pattern, error) {
	if node.Kind == 0 {
		return []Pattern{}, nil
	}
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%w: %s must be a sequence", ErrInvalidDeclaration, key)
	}
	patterns := make([]Pattern, 0, len(node.Content))
	seen := make(map[string]bool, len(node.Content))
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode || item.ShortTag() != "!!str" {
			return nil, fmt.Errorf("%w: %s item at line %d is not a string", ErrInvalidDeclaration, key, item.Line)
		}
		pattern, err := ParsePattern(item.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrInvalidDeclaration, key, err)
		}
		if seen[item.Value] {
			return nil, fmt.Errorf("%w: %s lists %q twice", ErrInvalidDeclaration, key, item.Value)
		}
		seen[item.Value] = true
		patterns = append(patterns, pattern)
	}
	return patterns, nil
}

// NoDeclaration is the declaration of a repository without .suiteward.yml.
func NoDeclaration() Declaration {
	return Declaration{include: []Pattern{}, exclude: []Pattern{}, runner: []Pattern{}}
}

// Present reports whether the declaration file exists.
func (d Declaration) Present() bool {
	return d.present
}

// Raw returns a copy of the exact declaration bytes, nil when absent.
func (d Declaration) Raw() []byte {
	return slices.Clone(d.raw)
}

// Include returns a copy of the include patterns in input order.
func (d Declaration) Include() []Pattern {
	return slices.Clone(d.include)
}

// Exclude returns a copy of the exclude patterns in input order.
func (d Declaration) Exclude() []Pattern {
	return slices.Clone(d.exclude)
}

// Runner returns a copy of the runner patterns in input order.
func (d Declaration) Runner() []Pattern {
	return slices.Clone(d.runner)
}
