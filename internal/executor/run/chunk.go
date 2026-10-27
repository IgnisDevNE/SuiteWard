package run

import (
	"regexp"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// maxArgvBytes bounds the arguments of one go test invocation: the command line of the container runtime holds them, and a Windows
// command line stops at about 32,000 characters (a Linux argument at 128 KiB), with the image, the limits and the environment around them.
const maxArgvBytes = 16 << 10

// chunks splits tests, in the order given, into the groups that one go test invocation each can name within maxArgvBytes. A group's size is
// estimated from what goprofile.Command writes: its fixed arguments, each distinct name once in the -run pattern and each distinct package
// once as a directory, and each of them with the byte that separates it. The estimate is never below the real size.
func chunks(tests []goprofile.TestID, timeout time.Duration, vendor bool) [][]goprofile.TestID {
	fixed := 0
	argv, _ := goprofile.Command(nil, timeout, vendor)
	for _, arg := range argv {
		fixed += len(arg) + 1
	}
	var out [][]goprofile.TestID
	var group []goprofile.TestID
	names, dirs, size := map[string]bool{}, map[string]bool{}, fixed
	for _, t := range tests {
		cost := func() (n int) {
			if !names[t.Name] {
				n += len(regexp.QuoteMeta(t.Name)) + 1
			}
			if !dirs[t.Package] {
				n += len(t.Package) + 3 // "./" and the separator; the root is "." and is shorter
			}
			return n
		}
		if len(group) > 0 && size+cost() > maxArgvBytes {
			out = append(out, group)
			group, names, dirs, size = nil, map[string]bool{}, map[string]bool{}, fixed
		}
		size += cost()
		names[t.Name], dirs[t.Package] = true, true
		group = append(group, t)
	}
	if len(group) > 0 {
		out = append(out, group)
	}
	return out
}
