// Package prefetch is the executor-side replacement for a networked install of Go modules (ADR 0028, section 11). From one side's go.mod and
// go.sum it plans which files of the module proxy the sandbox needs, and it fetches them from proxy.golang.org only and streams them into the
// sandbox's stdin tar, which the sandbox then reads as a read-only file:// module proxy. The sandbox never has a network, and no file is written
// to the host.
package prefetch

import (
	"errors"
	"io"
)

var (
	// ErrInstallFailed marks a refusal made before any fetch: the go.mod or the go.sum does not validate, a replace points outside the tree, or
	// the plan passes its bound. It is deterministic: the same input is refused again.
	ErrInstallFailed = errors.New("prefetch: install failed")
	// ErrFetchFailed marks an infrastructure failure: the proxy, the network, a module the proxy does not serve (a private one among them), a
	// file that is not what the proxy should send. It is counted and retried.
	ErrFetchFailed = errors.New("prefetch: fetch failed")
	// ErrTooLarge marks a fetch that passes its byte bound, or a module zip whose directory cannot be read within the bounded tail. It is
	// deterministic.
	ErrTooLarge = errors.New("prefetch: too large")
)

// Limits are the bounds of one side's prefetch (runtime contract, limits.prefetchModules and limits.prefetchBytes). PlanSide uses Modules and
// Fetch uses Bytes.
type Limits struct {
	Modules int
	Bytes   int64
}

// File is one file of the module proxy, named relative to the proxy's base: <escaped path>/@v/<escaped version>.mod or .zip.
type File struct{ Name string }

// Plan is what one side needs. A side that vendors needs nothing.
type Plan struct {
	Vendor bool
	Files  []File
}

// Sink takes each fetched file, with its size known before the first byte, as an entry of the sandbox's tar. It must read exactly size bytes.
type Sink interface {
	Add(name string, size int64, r io.Reader) error
}

// Stats counts what Fetch put into the sink. Unpacked is the size of the module zips once extracted, read from their directories, for the size
// check that the caller makes before go test runs.
type Stats struct {
	Files           int
	Bytes, Unpacked int64
}
