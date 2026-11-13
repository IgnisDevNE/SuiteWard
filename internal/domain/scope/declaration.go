package scope

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

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
	// entries is every entry of the execution block, protected-only ones included, in the order of the contract (root first,
	// then by folder in byte order, then by profile id): the scalar form is one running Go entry at the root, and a
	// declaration with no block has none. Parts lists the running ones; Reductions needs the rest to tell an entry that
	// is made protected only from one that is removed.
	entries  []entry
	listForm bool
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
	block, err := parseExecution(fields["execution"])
	if err != nil {
		return Declaration{}, err
	}
	return Declaration{present: true, raw: slices.Clone(raw), include: include, exclude: exclude, runner: runner, execution: block.policy, entries: block.entries, listForm: block.list}, nil
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
	// executionFields are the string keys allowed under execution, each with the values it accepts. The scalar profile
	// takes go only, whatever profileIDs holds: a repository that runs another profile uses the list form.
	executionFields = map[string][]string{"profile": {"go"}, "canonical": canonicalLevels, "receipt": receiptLevels}
	// profileIDs is the closed set of profile ids of the list form, sorted. The domain imports no executor package, so it
	// keeps its own copy, which the runner's registry is held equal to. cpp joins it when its profile lands.
	profileIDs = []string{"go"}
	// entryKeys are the keys of an entry of the list form.
	entryKeys = []string{"profile", "folder", "run"}
)

const (
	maxProfileEntries = 8   // the most entries of the list form
	maxFolderBytes    = 512 // the longest folder, as long as a result's package
)

// ProfileIDs returns the closed set of profile ids that a declaration accepts, sorted.
func ProfileIDs() []string { return slices.Clone(profileIDs) }

// entry is an entry of the execution block: a profile and its folder ("." for the root), and whether it runs. An entry
// that does not run is protected only.
type entry struct {
	profile, folder string
	run             bool
}

// executionBlock is a parsed execution mapping: the policy that Execution returns, every entry, and whether the block
// uses the list form.
type executionBlock struct {
	policy  Execution
	entries []entry
	list    bool
}

// parseExecution validates the optional execution mapping: only the keys profile (the scalar form, go), profiles (the
// list form), canonical and receipt, each once, never both profile and profiles, each string from its fixed list. A level
// beside a running entry (the scalar profile is one at the root) sets the other level to advisory unless it is named; a
// level when nothing runs is refused: it would read as a policy that runs nothing. An empty mapping, an absent key (a zero
// node) and a list of protected-only entries are no policy.
func parseExecution(node yaml.Node) (executionBlock, error) {
	if node.Kind == 0 {
		return executionBlock{}, nil
	}
	if node.Kind != yaml.MappingNode {
		return executionBlock{}, fmt.Errorf("%w: execution must be a mapping", ErrInvalidDeclaration)
	}
	values := make(map[string]string, len(executionFields))
	var list *yaml.Node
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			return executionBlock{}, fmt.Errorf("%w: unsupported key under execution at line %d", ErrInvalidDeclaration, key.Line)
		}
		accepted, scalar := executionFields[key.Value]
		if !scalar && key.Value != "profiles" {
			return executionBlock{}, fmt.Errorf("%w: unknown key %q under execution at line %d", ErrInvalidDeclaration, key.Value, key.Line)
		}
		if _, dup := values[key.Value]; dup || key.Value == "profiles" && list != nil {
			return executionBlock{}, fmt.Errorf("%w: execution key %q appears twice", ErrInvalidDeclaration, key.Value)
		}
		if !scalar {
			list = value
			continue
		}
		if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" {
			return executionBlock{}, fmt.Errorf("%w: execution %s at line %d is not a string", ErrInvalidDeclaration, key.Value, value.Line)
		}
		if !slices.Contains(accepted, value.Value) {
			return executionBlock{}, fmt.Errorf("%w: execution %s %q is not one of %q", ErrInvalidDeclaration, key.Value, value.Value, accepted)
		}
		values[key.Value] = value.Value
	}
	block := executionBlock{list: list != nil}
	switch profile, scalar := values["profile"]; {
	case scalar && list != nil:
		return executionBlock{}, fmt.Errorf("%w: execution names both profile and profiles", ErrInvalidDeclaration)
	case scalar:
		block.entries = []entry{{profile: profile, folder: ".", run: true}}
	case list != nil:
		entries, err := parseProfiles(list)
		if err != nil {
			return executionBlock{}, err
		}
		block.entries = entries
	}
	label := runningLabel(block.entries)
	if label == "" {
		if len(values) > 0 {
			return executionBlock{}, fmt.Errorf("%w: execution levels without a running profile", ErrInvalidDeclaration)
		}
		return block, nil
	}
	block.policy = Execution{Profile: label, Canonical: cmp.Or(values["canonical"], "advisory"), Receipt: cmp.Or(values["receipt"], "advisory")}
	return block, nil
}

// parseProfiles validates the list form: 1 to maxProfileEntries entries, a (profile, folder) pair once, and returns them in
// the order of the contract. Entries may nest, and entries of different profiles may share a folder.
func parseProfiles(node *yaml.Node) ([]entry, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("%w: execution profiles at line %d is not a sequence", ErrInvalidDeclaration, node.Line)
	}
	if n := len(node.Content); n < 1 || n > maxProfileEntries {
		return nil, fmt.Errorf("%w: execution profiles holds %d entries, want 1 to %d", ErrInvalidDeclaration, n, maxProfileEntries)
	}
	entries := make([]entry, 0, len(node.Content))
	for _, item := range node.Content {
		e, err := parseEntry(item)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(entries, func(other entry) bool { return other.profile == e.profile && other.folder == e.folder }) {
			return nil, fmt.Errorf("%w: execution profiles lists %s at the folder %q twice", ErrInvalidDeclaration, e.profile, e.folder)
		}
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b entry) int {
		return cmp.Or(strings.Compare(folderOrder(a.folder), folderOrder(b.folder)), strings.Compare(a.profile, b.profile))
	})
	return entries, nil
}

// folderOrder is the sort key of a folder: no folder is empty, so the root sorts before every other.
func folderOrder(folder string) string {
	if folder == "." {
		return ""
	}
	return folder
}

// parseEntry validates one entry of the list form: only the keys profile (required, a string of profileIDs), folder (a
// string, a clean relative path; absent is the root) and run (a YAML boolean, true when absent), each once.
func parseEntry(node *yaml.Node) (entry, error) {
	if node.Kind != yaml.MappingNode {
		return entry{}, fmt.Errorf("%w: execution profiles entry at line %d is not a mapping", ErrInvalidDeclaration, node.Line)
	}
	e := entry{folder: ".", run: true}
	seen := make(map[string]bool, len(entryKeys))
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i], node.Content[i+1]
		if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" {
			return entry{}, fmt.Errorf("%w: unsupported key in an execution profiles entry at line %d", ErrInvalidDeclaration, key.Line)
		}
		if !slices.Contains(entryKeys, key.Value) {
			return entry{}, fmt.Errorf("%w: unknown key %q in an execution profiles entry at line %d", ErrInvalidDeclaration, key.Value, key.Line)
		}
		if seen[key.Value] {
			return entry{}, fmt.Errorf("%w: execution profiles entry key %q appears twice", ErrInvalidDeclaration, key.Value)
		}
		seen[key.Value] = true
		if key.Value == "run" {
			if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!bool" || value.Decode(&e.run) != nil {
				return entry{}, fmt.Errorf("%w: execution profiles run at line %d is not a boolean", ErrInvalidDeclaration, value.Line)
			}
			continue
		}
		if value.Kind != yaml.ScalarNode || value.ShortTag() != "!!str" {
			return entry{}, fmt.Errorf("%w: execution profiles %s at line %d is not a string", ErrInvalidDeclaration, key.Value, value.Line)
		}
		if key.Value == "profile" {
			if !slices.Contains(profileIDs, value.Value) {
				return entry{}, fmt.Errorf("%w: execution profiles profile %q is not one of %q", ErrInvalidDeclaration, value.Value, profileIDs)
			}
			e.profile = value.Value
			continue
		}
		if err := checkFolder(value.Value); err != nil {
			return entry{}, fmt.Errorf("%w: execution profiles folder at line %d %w", ErrInvalidDeclaration, value.Line, err)
		}
		e.folder = value.Value
	}
	if !seen["profile"] {
		return entry{}, fmt.Errorf("%w: execution profiles entry at line %d names no profile", ErrInvalidDeclaration, node.Line)
	}
	return e, nil
}

// checkFolder holds a folder to the contract: a clean relative path of at most maxFolderBytes, valid UTF-8, with no control,
// format, line separator or paragraph separator character (the service refuses the two separators in a result's package, which
// holds the folder). The root has one spelling only (no folder), so "." is refused. Its messages never repeat the text, which is
// the repository's and not yet validated; once it is, parseProfiles quotes it in its duplicate-pair error. The YAML decoder
// already refuses invalid UTF-8; the check is kept because the contract states it.
func checkFolder(folder string) error {
	switch {
	case folder == "":
		return errors.New("is empty")
	case folder == ".":
		return errors.New("is the root, which is spelled by leaving the folder out")
	case len(folder) > maxFolderBytes:
		return fmt.Errorf("is %d bytes, over the limit of %d", len(folder), maxFolderBytes)
	case !utf8.ValidString(folder):
		return errors.New("is not UTF-8")
	case strings.IndexFunc(folder, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) }) >= 0:
		return errors.New("holds a control, format, line separator or paragraph separator character")
	}
	if strings.HasPrefix(folder, "/") || strings.HasSuffix(folder, "/") || slices.ContainsFunc(strings.Split(folder, "/"), func(segment string) bool {
		return segment == "" || segment == "." || segment == ".."
	}) {
		return errors.New("is not a clean relative path")
	}
	return nil
}

// runningLabel is the label of the running entries: their profile ids, unique, sorted bytewise and joined by "-". It is
// empty when nothing runs.
func runningLabel(entries []entry) string {
	var ids []string
	for _, e := range entries {
		if e.run {
			ids = append(ids, e.profile)
		}
	}
	slices.Sort(ids)
	return strings.Join(slices.Compact(ids), "-")
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

// Execution is the execution policy of a declaration with a running entry: the label of its profile, the canonical level
// (off, advisory or required) and the receipt level (off, advisory, before-approval or required) it runs at. The label
// is the ids of the running entries, unique, sorted bytewise and joined by "-": go, and with several profiles, for
// example, cpp-go. Two entries of one profile are one id.
type Execution struct {
	Profile, Canonical, Receipt string
}

// Execution returns the execution policy, and false when no entry runs (no section, an empty one, or a list of
// protected-only entries): then nothing runs, so "not configured" is told apart from a profile with its levels off.
func (d Declaration) Execution() (Execution, bool) {
	return d.execution, d.execution.Profile != ""
}

// Part is a running entry of the execution declaration: a profile id and its folder, "." for the root.
type Part struct{ Profile, Folder string }

// Parts returns the running entries in the order of the contract: the root first, then by folder in byte order, then by
// profile id in byte order. The scalar form gives [{go .}]. It is nil when nothing runs.
func (d Declaration) Parts() []Part {
	var parts []Part
	for _, e := range d.entries {
		if e.run {
			parts = append(parts, Part{Profile: e.profile, Folder: e.folder})
		}
	}
	return parts
}

// ListForm reports whether the execution block uses the list form (profiles) and not the scalar profile.
func (d Declaration) ListForm() bool { return d.listForm }

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
