package run

import (
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// maxArgvBytes bounds the arguments of one go test invocation: the command line of the container runtime holds them, and a Windows
// command line stops at about 32,000 characters (a Linux argument at 128 KiB), with the image, the limits and the environment around them.
const maxArgvBytes = 16 << 10

// chunks splits tests, sorted by package and name, into the groups that one go test invocation each can name within maxArgvBytes.
func chunks(tests []goprofile.TestID, timeout time.Duration, vendor bool) [][]goprofile.TestID {
	return nil
}
