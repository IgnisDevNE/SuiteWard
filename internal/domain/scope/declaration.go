package scope

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"

	"go.yaml.in/yaml/v3"
)

// MaxDeclarationPatterns is the most patterns a declaration may list across include, exclude and runner.
const MaxDeclarationPatterns = 1000

// DeclarationPath is the repository-root path of the scope declaration.
const DeclarationPath = ".suiteward.yml"

// ErrInvalidDeclaration reports a declaration that does not satisfy schema
// version 1.
var ErrInvalidDeclaration = errors.New("invalid scope declaration")

// Declaration is a validated .suiteward.yml, or its absence.
type Declaration struct {
	present   bool
	raw       []byte
	include   []Pattern
	exclude   []Pattern
	runner    []Pattern
	execution Execution
}

// ParseDeclaration validates raw as a schema version 1 declaration: a single
// YAML mapping with the integer key version (1), the optional pattern
// sequences include, exclude and runner, and the optional mapping execution,
// and nothing else. The mapping is walked as nodes, not decoded into a struct:
// yaml.v3 would coerce any scalar into a string, a whole float into an int,
// and expand merge keys (<<) into keys the file does not spell out.
//
// The execution key was added to version 1 without a new version: an older
// binary refuses a declaration that has it (an unknown key), which is meant, as
// it could not apply the policy the owner declared.
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
		return Declaration{}, fmt.Errorf("%w: %w", ErrInvalidDeclaration, err)
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
		case "version", "include", "exclude", "runner", "execution":
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
	total := 0
	for _, key := range []string{"include", "exclude", "runner"} {
		if node := fields[key]; node.Kind == yaml.SequenceNode {
			total += len(node.Content)
		}
	}
	if total > MaxDeclarationPatterns {
		return Declaration{}, fmt.Errorf("%w: %d patterns in a declaration of at most %d", ErrInvalidDeclaration, total, MaxDeclarationPatterns)
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
	execution, err := parseExecution(fields["execution"])
	if err != nil {
		return Declaration{}, err
	}
	return Declaration{present: true, raw: slices.Clone(raw), include: include, exclude: exclude, runner: runner, execution: execution}, nil
}

func checkVersion(node yaml.Node) error {
	if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!int" {
		return fmt.Errorf("%w: version must be the integer 1", ErrInvalidDeclaration)
	}
	var version int
	if err := node.Decode(&version); err != nil {
		return fmt.Errorf("%w: version must be the integer 1, got %q: %w", ErrInvalidDeclaration, node.Value, err)
	}
	if version != 1 {
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

// The levels of the execution policy in the order of weakening: a lower position is a weaker policy, and off is the weakest.
var (
	canonicalLevels = []string{"off", "advisory", "required"}
	receiptLevels   = []string{"off", "advisory", "before-approval", "required"}
	// executionFields are the keys allowed under execution, each with the values it accepts.
	executionFields = map[string][]string{"profile": {"go"}, "canonical": canonicalLevels, "receipt": receiptLevels}
)

// parseExecution validates the optional execution mapping: only the keys profile, canonical and receipt, each once,
// each a string from its fixed list. A profile alone sets both levels to advisory. Without a profile nothing runs,
// whatever levels are written, so the result is no policy; an absent key (a zero node) is the same.
func parseExecution(node yaml.Node) (Execution, error) {
	if node.Kind == 0 {
		return Execution{}, nil
	}
	if node.Kind != yaml.MappingNode {
		return Execution{}, fmt.Errorf("%w: execution must be a mapping", ErrInvalidDeclaration)
	}
	values := make(map[string]string, len(executionFields))
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			return Execution{}, fmt.Errorf("%w: unsupported key under execution at line %d", ErrInvalidDeclaration, key.Line)
		}
		accepted, known := executionFields[key.Value]
		if !known {
			return Execution{}, fmt.Errorf("%w: unknown key %q under execution at line %d", ErrInvalidDeclaration, key.Value, key.Line)
		}
		if _, dup := values[key.Value]; dup {
			return Execution{}, fmt.Errorf("%w: execution key %q appears twice", ErrInvalidDeclaration, key.Value)
		}
		if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" {
			return Execution{}, fmt.Errorf("%w: execution %s at line %d is not a string", ErrInvalidDeclaration, key.Value, value.Line)
		}
		if !slices.Contains(accepted, value.Value) {
			return Execution{}, fmt.Errorf("%w: execution %s %q is not one of %q", ErrInvalidDeclaration, key.Value, value.Value, accepted)
		}
		values[key.Value] = value.Value
	}
	if values["profile"] == "" { // a profile that is given is never empty: it is one of the accepted values
		return Execution{}, nil
	}
	return Execution{Profile: values["profile"], Canonical: cmp.Or(values["canonical"], "advisory"), Receipt: cmp.Or(values["receipt"], "advisory")}, nil
}

// NoDeclaration is the declaration of a repository without .suiteward.yml.
func NoDeclaration() Declaration {
	return Declaration{include: []Pattern{}, exclude: []Pattern{}, runner: []Pattern{}}
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

// Execution is the execution policy of a declaration that names a profile: the profile (go), the canonical level (off,
// advisory or required) and the receipt level (off, advisory, before-approval or required) it runs at.
type Execution struct {
	Profile, Canonical, Receipt string
}

// Execution returns the execution policy, and false when the declaration names no profile: then nothing runs, so
// "not configured" is told apart from a profile with its levels off.
func (d Declaration) Execution() (Execution, bool) {
	return d.execution, d.execution.Profile != ""
}

// levels returns the position of each level in its order of weakening; without a profile nothing runs, so both are off.
func (e Execution) levels() (canonical, receipt int) {
	if e.Profile == "" {
		return 0, 0
	}
	return slices.Index(canonicalLevels, e.Canonical), slices.Index(receiptLevels, e.Receipt)
}

// AddPatterns returns raw with include and runner appended to their lists. It creates a list that is missing (in block
// style), skips a pattern already in its list, quotes a new item as the items of its list are all quoted, keeps the
// comments yaml.v3 keeps, and never removes or reorders a pattern. The result is a valid declaration, so it errors
// when the additions pass MaxDeclarationPatterns; when there is nothing to add it is raw itself, byte for byte.
// Rewriting the file reindents it with two spaces, so the owner approves the new bytes (D-SCOPE-DISCOVERY).
func AddPatterns(raw []byte, include, runner []string) ([]byte, error) {
	return addToLists(raw, []listAdd{{"include", include}, {"runner", runner}})
}

// AddExcludes returns raw with paths appended to its exclude list, under the rules of AddPatterns. A path is added as
// the pattern it spells, so a name without a slash excludes that name at every depth.
func AddExcludes(raw []byte, paths []string) ([]byte, error) {
	return addToLists(raw, []listAdd{{"exclude", paths}})
}

// listAdd is the patterns to append to the list under key.
type listAdd struct {
	key      string
	patterns []string
}

func addToLists(raw []byte, adds []listAdd) ([]byte, error) {
	if _, err := ParseDeclaration(raw); err != nil {
		return nil, err
	}
	for _, add := range adds {
		for _, pattern := range add.patterns {
			if _, err := ParsePattern(pattern); err != nil {
				return nil, err
			}
		}
	}
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidDeclaration, err)
	}
	top := root.Content[0] // ParseDeclaration accepted raw: one document holding a mapping
	changed := false
	for _, add := range adds {
		if appendToList(top, add.key, add.patterns) {
			changed = true
		}
	}
	if !changed {
		return slices.Clone(raw), nil
	}
	var out bytes.Buffer
	encoder := yaml.NewEncoder(&out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	if _, err := ParseDeclaration(out.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// appendToList appends the patterns that the list under key lacks, creating the list when it is missing, and reports
// whether it added any.
func appendToList(top *yaml.Node, key string, patterns []string) bool {
	var list *yaml.Node
	present := map[string]bool{}
	var style yaml.Style // the quoting every existing item shares; none when they differ or the list is empty
	for i := 0; i < len(top.Content); i += 2 {
		if top.Content[i].Value == key {
			list = top.Content[i+1]
			for j, item := range list.Content {
				present[item.Value] = true
				if quoting := item.Style & (yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle); j == 0 {
					style = quoting
				} else if quoting != style {
					style = 0
				}
			}
		}
	}
	var added []*yaml.Node
	for _, pattern := range patterns {
		if !present[pattern] {
			present[pattern] = true
			added = append(added, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: pattern, Style: ItemStyle(pattern, style)})
		}
	}
	if len(added) == 0 {
		return false
	}
	if list == nil {
		list = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		top.Content = append(top.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, list)
	}
	list.Content = append(list.Content, added...)
	return true
}

// needsQuotes reports whether a plain scalar v would not read back as the string v: the merge key and the YAML 1.1 booleans, which
// the encoder leaves plain when the node is a string. ItemStyle applies it.
func needsQuotes(v string) bool {
	switch v {
	case "<<", "y", "Y", "yes", "Yes", "YES", "n", "N", "no", "No", "NO", "on", "On", "ON", "off", "Off", "OFF":
		return true
	}
	return false
}

// ItemStyle returns style, or the double-quoted style when style is plain and v needs quotes. appendToList and the
// review suggestions both use it, so that both write the same item.
func ItemStyle(v string, style yaml.Style) yaml.Style {
	if style&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle) == 0 && needsQuotes(v) {
		return yaml.DoubleQuotedStyle
	}
	return style
}
