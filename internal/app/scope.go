package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/scope"
)

// runScope is `suiteward scope`. Its one subcommand, add, widens the .suiteward.yml of the checkout it runs in. It reads no setting and
// grants nothing: the owner approves the changed file on a pull request like any other widening (D-SCOPE-DISCOVERY).
func runScope(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "add" {
		return usageError(stderr)
	}
	list, patterns, err := parseScopeAdd(args[1:], stderr)
	if err != nil {
		return 2
	}
	return runScopeAdd(list, patterns, stdout, stderr)
}

// parseScopeAdd reads the arguments of `scope add`: the list to extend and the patterns. An error has been reported on stderr with the usage.
func parseScopeAdd(args []string, stderr io.Writer) (list string, patterns []string, err error) {
	flags := flag.NewFlagSet("scope add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = io.WriteString(stderr, usage) } // a failed write to stderr cannot be reported anywhere else
	runner := flags.Bool("runner", false, "add the patterns to the runner list instead of include")
	if err := flags.Parse(args); err != nil {
		return "", nil, err
	}
	patterns = flags.Args()
	switch {
	case len(patterns) == 0:
		err = errors.New("scope add needs at least one pattern")
	case slices.ContainsFunc(patterns, func(p string) bool { return strings.HasPrefix(p, "-") }):
		// flag stops at the first pattern, so a flag written after it would be added as a pattern.
		err = errors.New("a pattern that starts with - is taken for a flag, and flags go before the patterns; write /-name for a pattern of that name")
	}
	if err != nil {
		reportf(stderr, "%v\n", err)
		_, _ = io.WriteString(stderr, usage) // a failed write to stderr cannot be reported anywhere else
		return "", nil, err
	}
	if *runner {
		return "runner", patterns, nil
	}
	return "include", patterns, nil
}

func runScopeAdd(list string, patterns []string, stdout, stderr io.Writer) int {
	valid := true
	for _, p := range patterns {
		if err := checkScopePattern(p); err != nil {
			reportf(stderr, "%v\n", err)
			valid = false
		}
	}
	if !valid {
		return 1
	}
	raw, perm, err := readDeclaration()
	if err != nil {
		reportf(stderr, "%v\n", err)
		return 1
	}
	edit, err := addListItems(raw, list, patterns)
	var byHand byHandError
	if errors.As(err, &byHand) {
		return reportByHand(list, edit.fresh, byHand, stderr)
	}
	if err != nil {
		reportf(stderr, "%s was not changed: %v\n", scope.DeclarationPath, err)
		return 1
	}
	var report strings.Builder
	for _, p := range edit.skipped {
		fmt.Fprintf(&report, "already in the %s list: %s\n", list, p)
	}
	if len(edit.lines) > 0 {
		if err := replaceDeclaration(edit.result, perm); err != nil {
			reportf(stderr, "%s was not changed: %v\n", scope.DeclarationPath, err)
			return 1
		}
		fmt.Fprintf(&report, "%s\nThese patterns protect more files only once the owner approves this revision of %s on a pull request.\n",
			strings.Join(edit.lines, "\n"), scope.DeclarationPath)
	} else {
		fmt.Fprintf(&report, "Nothing to add: %s is unchanged.\n", scope.DeclarationPath)
	}
	_, _ = io.WriteString(stdout, report.String()) // a failed write to stdout cannot be reported anywhere else
	return 0
}

// checkScopePattern refuses a pattern that the domain refuses, and one that holds a tab or a character that cannot be seen: D-SCOPE shows such
// a pattern escaped and never offers one, and the file would hold text that its reader cannot tell from another.
func checkScopePattern(p string) error {
	if shown := commandtext.Visible(p); shown != p || strings.Contains(p, "\t") {
		return fmt.Errorf("the pattern \"%s\" holds a tab, a backslash or a character that cannot be seen", shown)
	}
	_, err := scope.ParsePattern(p)
	return err
}

// readDeclaration reads .suiteward.yml of the current directory, which must be a regular file that the command can replace.
func readDeclaration() ([]byte, fs.FileMode, error) {
	info, err := os.Lstat(scope.DeclarationPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, 0, fmt.Errorf("%s does not exist in this directory, and scope add changes a declaration but does not create one", scope.DeclarationPath)
	case err != nil:
		return nil, 0, err
	case !info.Mode().IsRegular():
		return nil, 0, fmt.Errorf("%s is not a regular file (it may be a symbolic link), and scope add does not replace it", scope.DeclarationPath)
	}
	raw, err := os.ReadFile(scope.DeclarationPath)
	return raw, info.Mode().Perm(), err
}

// replaceDeclaration replaces .suiteward.yml with data through a temporary file in the same directory, so a crash leaves the old file or the
// whole new one. CreateTemp makes the file 0600; it gets the mode of the file it replaces.
func replaceDeclaration(data []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(scope.DeclarationPath), ".suiteward-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	var serr error
	if werr == nil {
		serr = tmp.Sync() // before the rename, so that a crash leaves the old file or the whole new one
	}
	if err := errors.Join(werr, serr, tmp.Chmod(perm), tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), scope.DeclarationPath); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// byHandError is a declaration that the command will not edit: nothing is written and the user adds the lines.
type byHandError struct {
	reason string
	absent bool // the declaration has no such list, so the lines need their key
}

func (e byHandError) Error() string { return e.reason }

func byHand(format string, args ...any) error {
	return byHandError{reason: fmt.Sprintf(format, args...)}
}

// reportByHand tells the user why nothing was written, and what to add to the declaration instead: the patterns that the list lacks. The
// key goes with them only when the list is absent; pasted over a list that exists, it would be a second key.
func reportByHand(list string, fresh []string, why byHandError, stderr io.Writer) int {
	lines, err := itemLines("  - ", fresh, 0)
	if err != nil {
		reportf(stderr, "%v\n", err)
		return 1
	}
	instruction, items := "add these items to its "+list+" list", strings.Join(lines, "\n")
	if why.absent {
		instruction, items = "add this list to it", list+":\n"+items
	}
	reportf(stderr, "%s was not changed: %v\nTo add the patterns yourself, %s:\n%s\n", scope.DeclarationPath, why, instruction, items)
	return 1
}

// listEdit is what `scope add` does to a declaration.
type listEdit struct {
	result  []byte   // the declaration with the new lines in it
	lines   []string // the lines inserted, one for each fresh pattern
	fresh   []string // the patterns that the list lacks, each once
	skipped []string // the patterns that the list holds already, or that an earlier argument repeats
}

// addListItems inserts the patterns that the list under key lacks, as lines of text after its last item. It never writes the declaration
// again: the bytes of raw stay as they are around the new lines, because the owner approves the bytes (D-SCOPE). It leaves the edit to the user
// (a byHandError) when it cannot insert safely, and it checks the new bytes with the domain parser before it returns them.
func addListItems(raw []byte, key string, patterns []string) (listEdit, error) {
	old, parseErr := scope.ParseDeclaration(raw) // on an error, the zero declaration: it holds nothing
	held := declaredLists(old)
	var edit listEdit
	for _, p := range patterns {
		if slices.Contains(held[key], p) || slices.Contains(edit.fresh, p) {
			edit.skipped = append(edit.skipped, p)
		} else {
			edit.fresh = append(edit.fresh, p)
		}
	}
	if parseErr != nil {
		return edit, byHand("it is not a valid declaration: %v", parseErr)
	}
	if len(edit.fresh) == 0 {
		edit.result = raw
		return edit, nil
	}
	if total := len(held["include"]) + len(held["exclude"]) + len(held["runner"]) + len(edit.fresh); total > scope.MaxDeclarationPatterns {
		return edit, fmt.Errorf("it would hold %d patterns, and a declaration holds at most %d", total, scope.MaxDeclarationPatterns)
	}
	var err error
	if edit.result, edit.lines, err = insertItems(raw, key, edit.fresh); err != nil {
		return edit, err
	}
	want := held
	want[key] = append(want[key], edit.fresh...)
	updated, err := scope.ParseDeclaration(edit.result)
	if err != nil || !maps.EqualFunc(want, declaredLists(updated), slices.Equal[[]string]) {
		return edit, byHand("the new lines would not read back as the same declaration plus the new patterns")
	}
	return edit, nil
}

// declaredLists is the three pattern lists of a declaration, as the strings that were written.
func declaredLists(d scope.Declaration) map[string][]string {
	names := func(patterns []scope.Pattern) []string {
		out := make([]string, len(patterns))
		for i, p := range patterns {
			out[i] = p.String()
		}
		return out
	}
	return map[string][]string{"include": names(d.Include()), "exclude": names(d.Exclude()), "runner": names(d.Runner())}
}

// itemPrefix is what precedes the value of a list item: the indentation, the dash and the spaces after it.
var itemPrefix = regexp.MustCompile(`^ *- +`)

// quotingStyles are the YAML styles of the quotings that listQuoting names.
var quotingStyles = map[commandtext.Quoting]yaml.Style{
	commandtext.QuotingPlain: 0, commandtext.QuotingSingle: yaml.SingleQuotedStyle, commandtext.QuotingDouble: yaml.DoubleQuotedStyle,
}

// insertItems returns raw with a line for each pattern after the last item of the block list under key, and those lines. The lines have the
// indentation of that item, its line break (the one above it when it ends the file without one), and the quoting of the items of the list.
func insertItems(raw []byte, key string, patterns []string) ([]byte, []string, error) {
	list, err := blockList(raw, key)
	if err != nil {
		return nil, nil, err
	}
	last := list.Content[len(list.Content)-1] // a block list has an item
	lines := strings.SplitAfter(string(raw), "\n")
	// YAML also counts a lone CR, NEL, LS and PS as a line break, so its line numbers can run ahead of the lines of the file.
	if last.Line > len(lines) {
		return nil, nil, byHand("its line numbers do not match its lines")
	}
	anchor := lines[last.Line-1]
	text := strings.TrimRight(anchor, "\r\n")
	prefix := itemPrefix.FindString(text)
	if prefix == "" || !singleLineItem(text, last) {
		return nil, nil, byHand("the last item of %s is not a line of the form \"- value\"", key)
	}
	added, err := itemLines(prefix, patterns, quotingStyles[listQuoting(list)])
	if err != nil {
		return nil, nil, err
	}
	terminated := strings.HasSuffix(anchor, "\n")
	breakLine := anchor
	if !terminated {
		breakLine = lines[last.Line-2] // the item ends the file, so the line above it ends with a break
	}
	eol := "\n"
	if strings.HasSuffix(breakLine, "\r\n") {
		eol = "\r\n"
	}
	inserted := strings.Join(added, eol)
	if terminated {
		inserted += eol
	} else {
		inserted = eol + inserted
	}
	return []byte(strings.Join(lines[:last.Line], "") + inserted + strings.Join(lines[last.Line:], "")), added, nil
}

// blockList is the node of the block sequence under key in raw, which ParseDeclaration has accepted: one document that holds a mapping, with
// the key once.
func blockList(raw []byte, key string) (*yaml.Node, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	top := root.Content[0]
	for i := 0; i < len(top.Content); i += 2 {
		name, list := top.Content[i], top.Content[i+1]
		if name.Value != key {
			continue
		}
		switch {
		case name.Anchor != "" || list.Anchor != "" || (name.Style|list.Style)&yaml.TaggedStyle != 0:
			return nil, byHand("%s has an anchor or a tag", key)
		case list.Kind != yaml.SequenceNode || list.Style&yaml.FlowStyle != 0:
			return nil, byHand("%s is not a block list (a flow list, as in [a, b], is not edited)", key)
		}
		return list, nil
	}
	return nil, byHandError{reason: fmt.Sprintf("there is no %s list", key), absent: true}
}

// itemLines is the line of each pattern as an item of a list: the prefix, then the value written in style as scope.AddPatterns writes it
// (the encoder decides what the style allows, so that a plain item that YAML would read as another scalar or as syntax gets its quotes).
func itemLines(prefix string, patterns []string, style yaml.Style) ([]string, error) {
	lines := make([]string, len(patterns))
	for i, p := range patterns {
		item := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: p, Style: scope.ItemStyle(p, style)}}}
		out, err := yaml.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("write the pattern %q as a list item: %w", p, err)
		}
		lines[i] = prefix + strings.TrimSuffix(strings.TrimPrefix(string(out), "- "), "\n")
	}
	return lines, nil
}
