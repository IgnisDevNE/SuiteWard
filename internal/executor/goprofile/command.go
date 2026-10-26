package goprofile

import (
	"regexp"
	"slices"
	"strings"
	"time"
)

// Where the sandbox puts go's own files, under its /work, beside the repository.
const (
	work        = "/work"
	cacheDir    = ".suiteward/gocache"
	modCacheDir = ".suiteward/gomodcache"
)

// Command is the go test invocation of one side, as the argv that the container's process execs (no shell reads it, so
// the -run pattern has no quotes of its own), and the whole environment of that process: the sandbox inherits nothing.
// One invocation runs the named tests, sorted and each once, in their packages, sorted and each once (./dir, and . for
// the root: a dot in front keeps a directory that starts with a dash from reading as a flag). go test applies -run to
// every package of the invocation, so a test of the same name in another package of it runs too; the output of a test
// that was not named is not looked at. timeout is go test's own -timeout. With vendor, go reads vendor/ and nothing is
// fetched, so GOPROXY is off; otherwise it is the prefetched module files under ProxyDir. tests is not empty.
//
// GOFLAGS is empty, so go picks -mod=vendor for a vendor/ that agrees with go.mod and -mod=readonly otherwise, as in
// CI. GOTOOLCHAIN=local makes a go.mod that needs a newer Go fail. There is no GOPATH, GOPRIVATE, GONOSUMDB, GOINSECURE,
// GONOPROXY or CI variable.
func Command(tests []TestID, timeout time.Duration, vendor bool) (argv, env []string) {
	var names, dirs []string
	for _, t := range tests {
		names = append(names, t.Name)
		dirs = append(dirs, t.Package)
	}
	slices.Sort(names)
	slices.Sort(dirs)
	names, dirs = slices.Compact(names), slices.Compact(dirs)
	for i, name := range names {
		names[i] = regexp.QuoteMeta(name)
	}
	argv = []string{"go", "test", "-json", "-count=1", "-shuffle=on", "-timeout=" + timeout.String(), "-run", "^(" + strings.Join(names, "|") + ")$"}
	for _, dir := range dirs {
		if dir != "." {
			dir = "./" + dir
		}
		argv = append(argv, dir)
	}

	proxy := "off"
	if !vendor {
		proxy = "file://" + work + "/" + ProxyDir
	}
	env = []string{
		"GOTOOLCHAIN=local",
		"GOWORK=off",
		"GOFLAGS=",
		"GOPROXY=" + proxy,
		"GOSUMDB=off",
		"HOME=" + work + "/" + HomeDir,
		"TMPDIR=" + work + "/" + TmpDir,
		"GOCACHE=" + work + "/" + cacheDir,
		"GOMODCACHE=" + work + "/" + modCacheDir,
		"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	return argv, env
}
