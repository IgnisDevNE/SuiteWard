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

// Two spellings of one name fold to one key on NTFS and APFS, whether they are files, directories or one of each. A
// repository that has both Docs/ and docs/ reads unavailable, and that is accepted.
func TestCheckSourceRefusesTwoSpellingsOfOneName(t *testing.T) {
	tests := []struct {
		name    string
		entries []execution.TreeEntry
		files   []execution.ExtractedFile
	}{
		{"two entries in two cases", []execution.TreeEntry{entry("a.go", execution.GitRegular, "a"), entry("A.go", execution.GitRegular, "b")}, nil},
		{"two directories in two cases", []execution.TreeEntry{entry("Dir/a.go", execution.GitRegular, "a"), entry("dir/b.go", execution.GitRegular, "b")}, nil},
		{"a directory entry and a path below it in another case", []execution.TreeEntry{{Path: "Docs", Mode: execution.GitTree, BlobSHA1: gitBlob("tree")}, entry("docs/x", execution.GitRegular, "x")}, nil},
		{"two entries, one with a trailing dot", []execution.TreeEntry{entry("x", execution.GitRegular, "a"), entry("x.", execution.GitRegular, "b")}, nil},
		{"two entries, one with a trailing space", []execution.TreeEntry{entry("x", execution.GitRegular, "a"), entry("x ", execution.GitRegular, "b")}, nil},
		{"two entries in two Unicode cases", []execution.TreeEntry{entry("É.go", execution.GitRegular, "a"), entry("é.go", execution.GitRegular, "b")}, nil},
		{"two files in two cases", nil, []execution.ExtractedFile{regular("a.go", "a"), regular("A.go", "b")}},
		{"two directories in two cases, extracted", nil, []execution.ExtractedFile{regular("Dir/a.go", "a"), regular("dir/b.go", "b")}},
		{"two links in two cases", nil, []execution.ExtractedFile{link("a", "x"), link("A", "y")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := execution.CheckSource(tt.entries, tt.files); !errors.Is(err, execution.ErrInvalidSource) {
				t.Errorf("CheckSource = %+v, %v; want ErrInvalidSource", got, err)
			}
		})
	}
}

// The same spelling in two places is one name, and different names stay different.
func TestCheckSourceAllowsTheSameDirectoryNameUnderTwoParents(t *testing.T) {
	entries := []execution.TreeEntry{entry("a/Docs/x", execution.GitRegular, "x"), entry("b/docs/x", execution.GitRegular, "y"), entry("a/Docs/y", execution.GitRegular, "z")}
	files := []execution.ExtractedFile{regular("a/Docs/x", "x"), regular("b/docs/x", "y"), regular("a/Docs/y", "z")}
	if got, err := execution.CheckSource(entries, files); err != nil || !got.Intact() {
		t.Errorf("CheckSource = %+v, %v; want intact", got, err)
	}
}

func TestCheckSourceAllowsANameThatOnlyPrefixesAnother(t *testing.T) {
	entries := []execution.TreeEntry{entry("d", execution.GitRegular, "d"), entry("d.go", execution.GitRegular, "g"), entry("dd/x", execution.GitRegular, "x")}
	files := []execution.ExtractedFile{regular("d", "d"), regular("d.go", "g"), regular("dd/x", "x")}
	if got, err := execution.CheckSource(entries, files); err != nil || !got.Intact() {
		t.Errorf("CheckSource = %+v, %v; want intact", got, err)
	}
}

// Git's legacy mode 100664 is a regular file with group write, and an archive cannot tell it from 100644.
func TestCheckSourceReadsTheLegacyRegularModeAsRegular(t *testing.T) {
	entries := []execution.TreeEntry{entry("a.go", "100664", "a"), entry("run.sh", "100664", "#!/bin/sh\n")}
	got, err := execution.CheckSource(entries, []execution.ExtractedFile{regular("a.go", "a"), regular("run.sh", "#!/bin/sh\n")})
	if err != nil || !got.Intact() {
		t.Errorf("CheckSource = %+v, %v; want intact: 100664 is a regular file", got, err)
	}
	// It is not an executable one: an extraction with the executable bit is another mode.
	exec := execution.ExtractedFile{Path: "run.sh", Mode: execution.GitExecutable, BlobSHA1: gitBlob("#!/bin/sh\n")}
	got, err = execution.CheckSource(entries, []execution.ExtractedFile{regular("a.go", "a"), exec})
	if err != nil || !slices.Equal(got.Mismatched, []string{"run.sh"}) {
		t.Errorf("CheckSource = %+v, %v; want run.sh mismatched", got, err)
	}
}

func TestCheckSourceAcceptsSymlinksThatResolveInsideTheTree(t *testing.T) {
	for name, links := range map[string][]execution.ExtractedFile{
		"to a sibling":                {link("a", "b.go")},
		"to a file below":             {link("a", "dir/b.go")},
		"up and back down":            {link("dir/a", "../b.go")},
		"to the tree's own root":      {link("dir/a", "..")},
		"to a directory":              {link("a", "dir")},
		"dot segments":                {link("a", "./dir/./b.go")},
		"to nothing, inside the tree": {link("a", "does/not/exist")},
		"through a link named in another case, staying inside": {link("sub/d", ".."), link("sub/a", "D/x")},
		"through a link to a directory":                        {link("d", "dir"), link("a", "d/b.go")},
		"through two links, within the cap":                    {link("l1", "l2"), link("l2", "l3"), link("l3", "l4"), link("a", "l1/b.go")},
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

		// NTFS and APFS ignore case, and Windows drops trailing dots and spaces, so these name the link sub/d.
		"an escape through a link named in another case": {link("sub/d", ".."), link("sub/a", "D/../x")},
		"an escape through a link named with a dot":      {link("sub/d", ".."), link("sub/a", "d./../x")},
		"an escape through a link named with a space":    {link("sub/d", ".."), link("sub/a", "d /../x")},
		"an escape through a link named in Unicode case": {link("sub/É", ".."), link("sub/a", "é/../x")},
		"an escape through a link spelled in upper case": {link("sub/D", ".."), link("sub/a", "d/../x")},
		"an escape through a link spelled with a dot":    {link("sub/d.", ".."), link("sub/a", "d/../x")},
		"a name that ends in a dot":                      {link("a", "dir./b.go")},
		"a name that ends in a space":                    {link("a", "dir /b.go")},
		"a name of dots":                                 {link("a", ".../b.go")},
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
		{"an entry below a file", []execution.TreeEntry{entry("d", execution.GitRegular, "d"), entry("d/x", execution.GitRegular, "x")}, nil},
		{"an entry far below a file", []execution.TreeEntry{entry("a/b", execution.GitRegular, "b"), entry("a/b/c/d", execution.GitRegular, "d")}, nil},
		{"a directory below a file", []execution.TreeEntry{entry("d", execution.GitRegular, "d"), {Path: "d/sub", Mode: execution.GitTree, BlobSHA1: gitBlob("tree")}}, nil},
		{"an entry below a symlink", []execution.TreeEntry{linkEntry("d", "."), linkEntry("d/x", "../y")}, nil},
		{"an entry below a submodule", []execution.TreeEntry{{Path: "d", Mode: execution.GitSubmodule, BlobSHA1: gitBlob("commit")}, entry("d/x", execution.GitRegular, "x")}, nil},
		{"a file below a file", nil, []execution.ExtractedFile{regular("d", "d"), regular("d/x", "x")}},
		{"a file below a symlink", nil, []execution.ExtractedFile{link("d", "."), regular("d/x", "x")}},
		{"a link below a link", nil, []execution.ExtractedFile{link("d", "."), link("d/x", "../y")}},

		// A filesystem that ignores case, and drops trailing dots and spaces, sees these as one name.
		{"an entry below a file in another case", []execution.TreeEntry{entry("d", execution.GitRegular, "d"), entry("D/x", execution.GitRegular, "x")}, nil},
		{"an entry below a file spelled with a dot", []execution.TreeEntry{entry("d.", execution.GitRegular, "d"), entry("d/x", execution.GitRegular, "x")}, nil},
		{"an entry below a submodule in another case", []execution.TreeEntry{{Path: "Vendor", Mode: execution.GitSubmodule, BlobSHA1: gitBlob("commit")}, entry("vendor/x", execution.GitRegular, "x")}, nil},
		{"a file below a link in another case", nil, []execution.ExtractedFile{link("D", "."), regular("d/x", "x")}},
		{"a link below a file in another case", nil, []execution.ExtractedFile{regular("d", "d"), link("D/x", "../y")}},
		{"a file below a link spelled with a space", nil, []execution.ExtractedFile{link("d ", "."), regular("d/x", "x")}},
		{"an unknown entry mode", []execution.TreeEntry{entry("a.go", "100600", "a")}, nil},
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
