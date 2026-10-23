package archive

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// Reader reads one archive. The archive is untrusted: whoever holds the public recipient can write an archive that decrypts.
type Reader struct {
	plain io.Reader
	used  bool
}

// Open decrypts r with identity. A wrong identity is ErrWrongKey; bytes that are not an age file are ErrInvalid. Nothing of the
// archive has been verified yet: ReadManifest or Extract does it.
func Open(r io.Reader, identity *age.X25519Identity) (*Reader, error) {
	if identity == nil {
		return nil, errors.New("archive: no identity to open the backup with")
	}
	plain, err := age.Decrypt(r, identity)
	var noMatch *age.NoIdentityMatchError
	switch {
	case errors.As(err, &noMatch):
		return nil, fmt.Errorf("open the backup: %w", ErrWrongKey)
	case err != nil:
		return nil, fmt.Errorf("%w: open the backup: %w", ErrInvalid, err)
	}
	return &Reader{plain: plain}, nil
}

// ReadManifest streams the whole archive, checks every rule Extract checks, writes nothing, and returns the manifest. A Reader
// reads its stream once: open the file again to Extract after a preview.
func (r *Reader) ReadManifest() (Manifest, error) {
	return r.scan(func(string) (io.WriteCloser, error) { return discard{io.Discard}, nil })
}

// Extract writes the members into dir, which the caller made (mode 0700) and which holds none of them yet. Each member is a new
// file of mode 0600, and units go in a new units directory. On any failure Extract removes what it made and nothing else.
func (r *Reader) Extract(dir string) (Manifest, error) {
	x := &extraction{dir: dir}
	m, err := r.scan(x.create)
	if err != nil {
		return Manifest{}, errors.Join(err, x.cleanup())
	}
	return m, nil
}

type discard struct{ io.Writer }

func (discard) Close() error { return nil }

// damagedReader marks a failure to read the stream as ErrInvalid, and leaves a failure of the destination as it is.
type damagedReader struct{ r io.Reader }

func (d damagedReader) Read(p []byte) (int, error) {
	n, err := d.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		err = fmt.Errorf("%w: read the backup: %w", ErrInvalid, err)
	}
	return n, err
}

// scan checks the archive as a stream: member by member, each rejected at its header before a byte of it is written, then the
// manifest against what was read. open gives the destination of a member.
func (r *Reader) scan(open func(name string) (io.WriteCloser, error)) (Manifest, error) {
	if r.used {
		return Manifest{}, errors.New("archive: the backup was already read, open it again to read it again")
	}
	r.used = true
	s := scanner{tr: tar.NewReader(r.plain), open: open, seen: map[string]bool{}}
	var manifest *Manifest
	for {
		hdr, err := s.tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: read the backup: %w", ErrInvalid, err)
		}
		if manifest != nil {
			return Manifest{}, invalid("member %q comes after the manifest", hdr.Name)
		}
		if hdr.Typeflag == tar.TypeReg && hdr.Name == manifestName {
			if manifest, err = readManifest(s.tr); err != nil {
				return Manifest{}, err
			}
			continue
		}
		if err := s.member(hdr); err != nil {
			return Manifest{}, err
		}
	}
	// The tar can end before the encryption does: reading to the end makes age authenticate the last chunk.
	if _, err := io.Copy(io.Discard, damagedReader{r.plain}); err != nil {
		return Manifest{}, err
	}
	if manifest == nil {
		return Manifest{}, invalid("the backup has no %s", manifestName)
	}
	if err := compare(s.members, manifest.Members); err != nil {
		return Manifest{}, err
	}
	return *manifest, nil
}

type scanner struct {
	tr      *tar.Reader
	open    func(name string) (io.WriteCloser, error)
	members []Member
	seen    map[string]bool
}

// member checks the header of a member, then streams it to its destination while it hashes it.
func (s *scanner) member(hdr *tar.Header) error {
	switch {
	case hdr.Typeflag != tar.TypeReg:
		return invalid("member %q is not a regular file", hdr.Name)
	case !allowed(hdr.Name):
		return invalid("member name %q is not allowed", hdr.Name)
	case s.seen[hdr.Name]:
		return invalid("member %q appears twice", hdr.Name)
	}
	name := hdr.Name
	s.seen[name] = true
	dst, err := s.open(name)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), damagedReader{s.tr})
	// The destination is closed before an error returns: a file that is still open cannot be removed on Windows.
	if cerr := dst.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}
	s.members = append(s.members, Member{Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))})
	return nil
}

func readManifest(r io.Reader) (*Manifest, error) {
	body, err := io.ReadAll(io.LimitReader(damagedReader{r}, maxManifest+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxManifest {
		return nil, invalid("%s is too large", manifestName)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, invalid("%s is not valid: %v", manifestName, err)
	}
	if m.Format != formatVersion {
		return nil, invalid("format %d is not supported, this release reads format %d", m.Format, formatVersion)
	}
	return &m, nil
}

// compare requires the members read to be the members the manifest lists: the same names, in the same order, with the same sizes
// and hashes. That covers a member that is missing, extra, reordered or altered.
func compare(got, want []Member) error {
	for i := 0; i < len(got) || i < len(want); i++ {
		switch {
		case i >= len(want):
			return invalid("member %q is not in the manifest", got[i].Name)
		case i >= len(got):
			return invalid("the manifest lists %q, which the backup does not hold", want[i].Name)
		case got[i].Name != want[i].Name:
			return invalid("member %d is %q but the manifest lists %q there: the members and the manifest differ", i, got[i].Name, want[i].Name)
		case got[i].Size != want[i].Size:
			return invalid("member %q has size %d but the manifest records %d", got[i].Name, got[i].Size, want[i].Size)
		case got[i].SHA256 != want[i].SHA256:
			return invalid("member %q has sha256 %s but the manifest records %s", got[i].Name, got[i].SHA256, want[i].SHA256)
		}
	}
	return nil
}

// extraction creates the files of Extract and remembers them, so a failure removes exactly what this run made.
type extraction struct {
	dir     string
	created []string
	units   bool
}

// create opens a new file for name, which allowed has already accepted: the path is the allowed name under dir.
func (x *extraction) create(name string) (io.WriteCloser, error) {
	if strings.HasPrefix(name, unitsPrefix) && !x.units {
		// Mkdir, not MkdirAll: an existing directory, or a link in its place, is never reused.
		units := filepath.Join(x.dir, "units")
		if err := os.Mkdir(units, 0o700); err != nil {
			return nil, fmt.Errorf("create the units directory: %w", err)
		}
		x.units = true
		x.created = append(x.created, units)
	}
	path := filepath.Join(x.dir, filepath.FromSlash(name))
	f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", name, err)
	}
	x.created = append(x.created, path)
	return f, nil
}

func (x *extraction) cleanup() error {
	var errs []error
	for i := len(x.created) - 1; i >= 0; i-- {
		if err := os.Remove(x.created[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
