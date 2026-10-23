// Package archive is the file format of a SuiteWard backup (ADR 0019): one age file encrypted to the recovery recipient of the
// instance, whose plaintext is a tar of a few allowed members and a manifest.json that comes last. The manifest records the
// size and SHA-256 of every member, computed while the member streams, so a backup is neither buffered nor written in plaintext.
package archive

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"filippo.io/age"
)

const (
	// formatVersion is the only manifest format this package writes or reads.
	formatVersion = 1
	manifestName  = "manifest.json"
	unitsPrefix   = "units/"
	// maxManifest bounds what a reader holds in memory for manifest.json: the manifest of a real backup is a few hundred bytes.
	maxManifest = 1 << 20
)

var (
	// ErrInvalid wraps every way the bytes are not an intact backup of this format: damaged or truncated, a member the
	// format does not allow, or a member that differs from its manifest. A caller also gets it for a member it must not add.
	ErrInvalid = errors.New("invalid backup")
	// ErrWrongKey is Open's answer when the identity is not the one this backup was encrypted to.
	ErrWrongKey = errors.New("this identity does not open the backup: use the recovery kit of the instance that made it")
)

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

// unitName is the name of a unit file of an isolated install, in the units directory of the archive.
var unitName = regexp.MustCompile(`^units/suiteward[a-z-]*\.(container|volume|network)$`)

// allowed is the allowlist of member names: nothing else is written to an archive or accepted from one. manifest.json is not on
// it, because the writer adds it and the reader reads it apart.
func allowed(name string) bool {
	switch name {
	case "suiteward.sql", "suiteward-artifacts.tar", "status.json", "settings.yaml":
		return true
	}
	return unitName.MatchString(name)
}

// Info is what the manifest records about the backup besides its members.
type Info struct {
	SuitewardVersion string    `json:"suitewardVersion"`
	ImageDigest      string    `json:"imageDigest"`
	Instance         string    `json:"instance"`
	Tier             string    `json:"tier"`
	CreatedAt        time.Time `json:"createdAt"`
}

// Member is the manifest's record of one member.
type Member struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest is manifest.json: the members in archive order.
type Manifest struct {
	Format int `json:"format"`
	Info
	Members []Member `json:"members"`
}

// Writer streams members into an encrypted archive. Create it with NewWriter, Add each member, then Close.
type Writer struct {
	enc     io.WriteCloser
	tw      *tar.Writer
	info    Info
	members []Member
	seen    map[string]bool
	err     error // the first failure of the stream: after it no manifest is written, so the output can never read as complete
	closed  bool
}

// NewWriter starts an archive on dst, encrypted to recipient. The caller opens dst (a file only this user can read) and removes
// it when the archive fails: a failed Writer leaves a truncated file that no reader accepts, but it still holds part of a
// database.
func NewWriter(dst io.Writer, recipient *age.HybridRecipient, info Info) (*Writer, error) {
	if recipient == nil {
		return nil, errors.New("archive: no recipient to encrypt the backup to")
	}
	if info.CreatedAt.IsZero() {
		return nil, invalid("the manifest needs the time of the backup")
	}
	enc, err := age.Encrypt(dst, recipient)
	if err != nil {
		return nil, fmt.Errorf("start the encrypted backup: %w", err)
	}
	info.CreatedAt = info.CreatedAt.UTC().Truncate(time.Second)
	return &Writer{enc: enc, tw: tar.NewWriter(enc), info: info, members: []Member{}, seen: map[string]bool{}}, nil
}

// Add streams the member name from r, which must hold exactly size bytes: the tar header carries the size before the data, so
// a reader that disagrees fails the writer. A caller that does not know the size, such as the output of pg_dump, spools it to a
// private temporary file first and passes the file. A name outside the allowlist, and a name added twice, are refused without
// harming the archive; any other failure ends it.
func (w *Writer) Add(name string, size int64, r io.Reader) error {
	if w.closed {
		return errors.New("archive: the writer is closed")
	}
	if w.err != nil {
		return w.err
	}
	if !allowed(name) {
		return invalid("member name %q is not allowed", name)
	}
	if w.seen[name] {
		return invalid("member %q is added twice", name)
	}
	m, err := w.stream(name, size, r)
	if err != nil {
		w.err = err
		return err
	}
	w.seen[name] = true
	w.members = append(w.members, m)
	return nil
}

func (w *Writer) stream(name string, size int64, r io.Reader) (Member, error) {
	if err := w.tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o600, Size: size}); err != nil {
		return Member{}, fmt.Errorf("write %s: %w", name, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w.tw, h), io.LimitReader(r, size))
	if err != nil {
		return Member{}, fmt.Errorf("write %s: %w", name, err)
	}
	if n != size {
		return Member{}, invalid("member %q: the reader yielded %d bytes, but %d were announced", name, n, size)
	}
	var more [1]byte
	switch _, err := io.ReadFull(r, more[:]); {
	case err == nil:
		return Member{}, invalid("member %q: the reader has more than the %d bytes announced", name, size)
	case !errors.Is(err, io.EOF):
		return Member{}, fmt.Errorf("read %s: %w", name, err)
	}
	return Member{Name: name, Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// Close writes the manifest, then ends the tar and the encryption. It reports the first failure of the stream instead, and
// finishes nothing then.
func (w *Writer) Close() error {
	if w.closed {
		return errors.New("archive: the writer is closed")
	}
	w.closed = true
	if w.err != nil {
		return w.err
	}
	body, err := json.MarshalIndent(Manifest{Format: formatVersion, Info: w.info, Members: w.members}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the manifest: %w", err)
	}
	if err := w.tw.WriteHeader(&tar.Header{Name: manifestName, Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))}); err != nil {
		return fmt.Errorf("write %s: %w", manifestName, err)
	}
	if _, err := w.tw.Write(body); err != nil {
		return fmt.Errorf("write %s: %w", manifestName, err)
	}
	if err := w.tw.Close(); err != nil {
		return fmt.Errorf("finish the tar of the backup: %w", err)
	}
	if err := w.enc.Close(); err != nil {
		return fmt.Errorf("finish the encrypted backup: %w", err)
	}
	return nil
}
