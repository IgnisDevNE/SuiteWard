package source

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	maxPathBytes = 4096    // the longest path of a tree entry in the runtime contract
	maxLinkBytes = 4096    // the longest symlink target the sandbox's kernel creates (PATH_MAX)
	maxTrailer   = 1 << 20 // the padding that may follow the end of the tar before the end of the gzip stream
)

type kind uint8

const (
	regular kind = iota + 1
	symbolic
)

// entry is one file or symlink of a commit archive, below the archive's top-level directory.
type entry struct {
	path   string
	kind   kind
	exec   bool   // the owner can execute it
	size   int64  // the bytes of a regular file; the length of the target of a symlink
	target string // the target of a symlink
	body   io.Reader
}

func mismatch(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSourceMismatch, fmt.Sprintf(format, args...))
}

// readError marks an error of the archive stream among the errors that the code reading an entry can return, so that walk
// classifies it and leaves the others (a failed write, a mismatch) as they are.
type readError struct{ err error }

func (e *readError) Error() string { return e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

type markReads struct{ r io.Reader }

func (m markReads) Read(p []byte) (int, error) {
	n, err := m.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = &readError{err}
	}
	return n, err
}

// meter counts the bytes of the compressed stream, and gives no more than its limit: a service that sends at most archiveBytes
// bytes and then aborts the response has an archive that is larger.
type meter struct {
	ctx   context.Context
	r     io.Reader
	limit int64
	n     int64
	err   error // the first error of the stream other than its end
}

func (m *meter) Read(p []byte) (int, error) {
	if err := m.ctx.Err(); err != nil {
		return 0, err
	}
	room := m.limit - m.n
	if room <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > room {
		p = p[:room]
	}
	n, err := m.r.Read(p)
	m.n += int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		m.err = err
	}
	return n, err
}

// fail names what a failed read of the stream means. A stream that stops short is the service's failure to read GitHub to the end,
// or, when it stopped at exactly the limit, an archive that is larger than the limit. Bytes that are not a gzip stream or a tar are
// an archive that cannot be read.
func (m *meter) fail(err error) error {
	switch {
	case m.ctx.Err() != nil:
		return m.ctx.Err()
	case m.err != nil || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF):
		if m.n >= m.limit {
			return fmt.Errorf("%w: the stream stopped at its limit of %d bytes before the tar ended", ErrTooLarge, m.limit)
		}
		return fmt.Errorf("%w: %w", ErrGitHubUnavailable, err)
	default:
		return fmt.Errorf("%w: %w", ErrArchiveUnreadable, err)
	}
}

// walk reads a commit archive, a tar.gz in GitHub's layout (a pax global header and one top-level directory), and calls visit for
// each file and symlink in the order of the archive, with its path below the top-level directory. visit reads the body of a file
// as it passes: the body is the stream itself, so nothing holds a whole entry. An error of visit is returned as it is, except an
// error of the stream, which walk classifies. Anything the tree of a commit cannot hold (a hard link, a device, a second top-level
// directory, an unsafe path) is a source mismatch. After the last entry walk reads the stream to its end, so that the gzip trailer
// is checked and a stream that broke is seen.
func walk(ctx context.Context, archive io.Reader, l Limits, visit func(entry) error) error {
	m := &meter{ctx: ctx, r: archive, limit: l.ArchiveBytes}
	gz, err := gzip.NewReader(m)
	if err != nil {
		return m.fail(err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	top := ""
	for {
		hdr, err := tr.Next()
		switch {
		case errors.Is(err, io.EOF):
			return drain(gz, m)
		case errors.Is(err, tar.ErrInsecurePath):
			return mismatch("an entry has a path that leaves the tree")
		case err != nil:
			return m.fail(err)
		}
		e, skip, err := classify(hdr, &top)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		e.body = markReads{tr}
		if err := visit(e); err != nil {
			var read *readError
			if errors.As(err, &read) {
				return m.fail(read.err)
			}
			return err
		}
	}
}

// drain reads what follows the tar: the padding to the end of its record, and the trailer that carries the gzip checksum.
func drain(gz io.Reader, m *meter) error {
	n, err := io.Copy(io.Discard, io.LimitReader(gz, maxTrailer+1))
	switch {
	case err != nil:
		return m.fail(err)
	case n > maxTrailer:
		return fmt.Errorf("%w: data follows the end of the tar", ErrArchiveUnreadable)
	}
	return nil
}

// classify turns a tar header into an entry, or says to skip it (the global header, a directory). The first entry that is not the
// global header names the top-level directory, which every other entry must share.
func classify(hdr *tar.Header, top *string) (e entry, skip bool, err error) {
	if hdr.Typeflag == tar.TypeXGlobalHeader {
		return entry{}, true, nil
	}
	first, rest, _ := strings.Cut(hdr.Name, "/")
	if first == "" || (*top != "" && first != *top) {
		return entry{}, false, mismatch("%q is not below the top-level directory of the archive", hdr.Name)
	}
	*top = first
	switch hdr.Typeflag {
	case tar.TypeDir:
		return entry{}, true, nil
	case tar.TypeReg:
		e = entry{kind: regular, size: hdr.Size, exec: hdr.Mode&0o100 != 0}
	case tar.TypeSymlink:
		e = entry{kind: symbolic, target: hdr.Linkname, size: int64(len(hdr.Linkname))}
	default:
		return entry{}, false, mismatch("%q is an entry of the type %q, which a tree does not hold", hdr.Name, hdr.Typeflag)
	}
	if rest == "" || len(rest) > maxPathBytes || len(e.target) > maxLinkBytes {
		return entry{}, false, mismatch("%q has no path of a tree, or a path or a link target that is too long", hdr.Name)
	}
	e.path = rest
	return e, false, nil
}
