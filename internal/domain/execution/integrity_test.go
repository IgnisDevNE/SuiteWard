package execution_test

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
)

// gitBlob is the object ID git gives a blob, computed here by its definition.
func gitBlob(content string) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("blob %d\x00%s", len(content), content)))
	return hex.EncodeToString(sum[:])
}

func entry(path string, mode execution.GitMode, content string) execution.TreeEntry {
	return execution.TreeEntry{Path: path, Mode: mode, BlobSHA1: gitBlob(content)}
}

func regular(path, content string) execution.ExtractedFile {
	return execution.ExtractedFile{Path: path, Mode: execution.GitRegular, BlobSHA1: gitBlob(content)}
}

func link(path, target string) execution.ExtractedFile {
	return execution.ExtractedFile{Path: path, Mode: execution.GitSymlink, Target: target}
}

func linkEntry(path, target string) execution.TreeEntry {
	return entry(path, execution.GitSymlink, target)
}

func TestBlobSHA1MatchesGit(t *testing.T) {
	// The IDs `git hash-object` gives these contents.
	for content, want := range map[string]string{
		"":              "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
		"hello world\n": "3b18e512dba79e4c8300dd08aeb37f8e728b8dad",
		"a\x00b":        "20b5be91886d0b6f26dc98a225c0dac05fe2c86e",
		"../x":          "eda76f7226728b0f13def6ba3452fc35bb8d3961",
	} {
		if got := execution.BlobSHA1([]byte(content)); got != want {
			t.Errorf("BlobSHA1(%q) = %q, want %q", content, got, want)
		}
	}
}

func TestCheckSourceAcceptsTheExtractionOfItsTree(t *testing.T) {
	entries := []execution.TreeEntry{
		entry("a.go", execution.GitRegular, "package a\n"),
		entry("bin/run.sh", execution.GitExecutable, "#!/bin/sh\n"),
		entry("empty.txt", execution.GitRegular, ""),
		linkEntry("latest", "a.go"),
		{Path: "bin", Mode: execution.GitTree, BlobSHA1: gitBlob("a tree object")}, // a directory names no file
		{Path: "vendor/lib", Mode: execution.GitSubmodule, BlobSHA1: gitBlob("a commit")},
	}
	files := []execution.ExtractedFile{
		regular("a.go", "package a\n"),
		{Path: "bin/run.sh", Mode: execution.GitExecutable, BlobSHA1: gitBlob("#!/bin/sh\n")},
		regular("empty.txt", ""),
		link("latest", "a.go"),
	}
	got, err := execution.CheckSource(entries, files)
	if err != nil || !got.Intact() {
		t.Fatalf("CheckSource = %+v, %v; want an intact extraction", got, err)
	}
	if want := []string{"vendor/lib"}; !slices.Equal(got.Submodules, want) {
		t.Errorf("Submodules = %v, want %v: skipped and listed", got.Submodules, want)
	}
	if len(got.Missing)+len(got.Extra)+len(got.Mismatched) != 0 {
		t.Errorf("differences = %+v, want none", got)
	}
}

func TestCheckSourceReportsMissingExtraAndMismatchedPaths(t *testing.T) {
	entries := []execution.TreeEntry{
		entry("kept.go", execution.GitRegular, "kept"),
		entry("dropped_by_export_ignore.go", execution.GitRegular, "dropped"),
		entry("z_dropped.go", execution.GitRegular, "z"),
		entry("rewritten_by_export_subst.go", execution.GitRegular, "$Format:%H$"),
		entry("lfs.bin", execution.GitRegular, "the real bytes"),
		entry("mode.sh", execution.GitExecutable, "#!/bin/sh\n"),
		linkEntry("link", "kept.go"),
		{Path: "vendor/lib", Mode: execution.GitSubmodule, BlobSHA1: gitBlob("a commit")},
	}
	files := []execution.ExtractedFile{
		regular("kept.go", "kept"),
		regular("rewritten_by_export_subst.go", "0123456789abcdef0123456789abcdef01234567"),
		regular("lfs.bin", "version https://git-lfs.github.com/spec/v1\n"),
		regular("mode.sh", "#!/bin/sh\n"), // the executable bit was lost
		link("link", "other.go"),          // another target is another blob
		regular("planted.go", "planted"),
		regular("a_planted.go", "planted"),
		regular("vendor/lib", "a file where the submodule is"),
	}
	got, err := execution.CheckSource(entries, files)
	if err != nil {
		t.Fatal(err)
	}
	if got.Intact() {
		t.Error("an extraction with differences is intact")
	}
	if want := []string{"dropped_by_export_ignore.go", "z_dropped.go"}; !slices.Equal(got.Missing, want) {
		t.Errorf("Missing = %v, want %v", got.Missing, want)
	}
	if want := []string{"a_planted.go", "planted.go", "vendor/lib"}; !slices.Equal(got.Extra, want) {
		t.Errorf("Extra = %v, want %v", got.Extra, want)
	}
	if want := []string{"lfs.bin", "link", "mode.sh", "rewritten_by_export_subst.go"}; !slices.Equal(got.Mismatched, want) {
		t.Errorf("Mismatched = %v, want %v", got.Mismatched, want)
	}
}

func TestCheckSourceOfAnEmptyTreeAndExtraction(t *testing.T) {
	got, err := execution.CheckSource(nil, nil)
	if err != nil || !got.Intact() {
		t.Errorf("CheckSource(nil, nil) = %+v, %v; want intact", got, err)
	}
	got, err = execution.CheckSource(nil, []execution.ExtractedFile{regular("a.go", "a")})
	if err != nil || got.Intact() || !slices.Equal(got.Extra, []string{"a.go"}) {
		t.Errorf("a file with no tree = %+v, %v; want it extra", got, err)
	}
}

func TestCheckSourceAcceptsSymlinksThatResolveInsideTheTree(t *testing.T) {
	for name, links := range map[string][]execution.ExtractedFile{
		"to a sibling":                      {link("a", "b.go")},
		"to a file below":                   {link("a", "dir/b.go")},
		"up and back down":                  {link("dir/a", "../b.go")},
		"to the tree's own root":            {link("dir/a", "..")},
		"to a directory":                    {link("a", "dir")},
		"dot segments":                      {link("a", "./dir/./b.go")},
		"to nothing, inside the tree":       {link("a", "does/not/exist")},
		"through a link to a directory":     {link("d", "dir"), link("a", "d/b.go")},
		"through two links, within the cap": {link("l1", "l2"), link("l2", "l3"), link("l3", "l4"), link("a", "l1/b.go")},
	} {
		t.Run(name, func(t *testing.T) {
			var entries []execution.TreeEntry
			for _, l := range links {
				entries = append(entries, linkEntry(l.Path, l.Target))
			}
			got, err := execution.CheckSource(entries, links)
			if err != nil || !got.Intact() {
				t.Errorf("CheckSource = %+v, %v; want intact", got, err)
			}
		})
	}
}

func TestCheckSourceRefusesASymlinkThatLeavesTheTree(t *testing.T) {
	for name, links := range map[string][]execution.ExtractedFile{
		"up from the root":            {link("a", "../x")},
		"up from a directory":         {link("dir/a", "../../x")},
		"an absolute target":          {link("a", "/etc/passwd")},
		"an empty target":             {link("a", "")},
		"below a name that escapes":   {link("a", "dir/../../x")},
		"a loop":                      {link("a", "b"), link("b", "a")},
		"a link to itself":            {link("a", "a")},
		"more links than the cap":     {link("l1", "l2"), link("l2", "l3"), link("l3", "l4"), link("l4", "l5"), link("l5", "l6"), link("l6", "l7"), link("l7", "l8"), link("l8", "l9"), link("l9", "l10"), link("a", "l1")},
		"a backslash target":          {link("a", `..\..\evil`)},
		"a backslash inside a target": {link("a", `dir\b.go`)},
		"a drive-letter target":       {link("a", "C:/x")},
		"a colon in a target":         {link("a", "dir/b:stream")},
		"a NUL in a target":           {link("a", "b\x00.go")},
		"a backslash target mid-chain": {
			link("a", "l/b.go"), link("l", `..\x`),
		},
		"an escape after a link":      {link("d/t", ".."), link("s", "d/t/..")},
		"an escape through two links": {link("s/y", ".."), link("s/x", "y/../..")},
	} {
		t.Run(name, func(t *testing.T) {
			var entries []execution.TreeEntry
			for _, l := range links {
				entries = append(entries, linkEntry(l.Path, l.Target))
			}
			if got, err := execution.CheckSource(entries, links); !errors.Is(err, execution.ErrUnsafeSymlink) {
				t.Errorf("CheckSource = %+v, %v; want ErrUnsafeSymlink", got, err)
			}
		})
	}
}

func TestCheckSourceRefusesInputItCannotRead(t *testing.T) {
	good := entry("a.go", execution.GitRegular, "a")
	tests := []struct {
		name    string
		entries []execution.TreeEntry
		files   []execution.ExtractedFile
	}{
		{"an invalid entry path", []execution.TreeEntry{entry("../a.go", execution.GitRegular, "a")}, nil},
		{"an empty entry path", []execution.TreeEntry{entry("", execution.GitRegular, "a")}, nil},
		{"an entry path listed twice", []execution.TreeEntry{good, good}, nil},
		{"an unknown entry mode", []execution.TreeEntry{entry("a.go", "100664", "a")}, nil},
		{"an entry with no blob SHA-1", []execution.TreeEntry{{Path: "a.go", Mode: execution.GitRegular}}, nil},
		{"an entry with an upper-case SHA-1", []execution.TreeEntry{{Path: "a.go", Mode: execution.GitRegular, BlobSHA1: strings.ToUpper(gitBlob("a"))}}, nil},
		{"an entry with a short SHA-1", []execution.TreeEntry{{Path: "a.go", Mode: execution.GitRegular, BlobSHA1: gitBlob("a")[:39]}}, nil},
		{"an entry with a SHA-1 that is not hexadecimal", []execution.TreeEntry{{Path: "a.go", Mode: execution.GitRegular, BlobSHA1: strings.Repeat("g", 40)}}, nil},
		{"an invalid file path", nil, []execution.ExtractedFile{regular("a\\b.go", "a")}},
		{"a file listed twice", nil, []execution.ExtractedFile{regular("a.go", "a"), regular("a.go", "a")}},
		{"a file with a submodule mode", nil, []execution.ExtractedFile{{Path: "a", Mode: execution.GitSubmodule, BlobSHA1: gitBlob("a")}}},
		{"a file with a directory mode", nil, []execution.ExtractedFile{{Path: "a", Mode: execution.GitTree, BlobSHA1: gitBlob("a")}}},
		{"a file with an unknown mode", nil, []execution.ExtractedFile{{Path: "a", Mode: "0", BlobSHA1: gitBlob("a")}}},
		{"a regular file with no blob SHA-1", nil, []execution.ExtractedFile{{Path: "a", Mode: execution.GitRegular}}},
		{"a regular file with a malformed SHA-1", nil, []execution.ExtractedFile{{Path: "a", Mode: execution.GitRegular, BlobSHA1: "abc"}}},
		{"a regular file with a target", nil, []execution.ExtractedFile{{Path: "a", Mode: execution.GitRegular, BlobSHA1: gitBlob("a"), Target: "b"}}},
		{"a symlink with a blob SHA-1", nil, []execution.ExtractedFile{{Path: "a", Mode: execution.GitSymlink, BlobSHA1: gitBlob("b"), Target: "b"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := execution.CheckSource(tt.entries, tt.files); !errors.Is(err, execution.ErrInvalidSource) {
				t.Errorf("CheckSource = %+v, %v; want ErrInvalidSource", got, err)
			}
		})
	}
}
