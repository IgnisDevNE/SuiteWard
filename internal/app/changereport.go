package app

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// maxReportBytes bounds the file bytes one report build holds, both sides together. The service container has 256 MiB, and a
// build shares them with the blob cache of the GitHub reader (32 MiB) and with the hunks the report keeps (4 MiB at most).
const maxReportBytes = 32 << 20

// errReportTooLarge says that a revision changes more bytes than a report is built from here.
var errReportTooLarge = errors.New("the revision changes too many bytes to build a report")

// changeReports rebuilds the change report of a proposal revision, whenever it is asked to: the revision comment, the check summary and the
// report page all read it from here, so they state the same numbers. The proposed manifest of a revision is not in the database, only its
// digest: the code that records a revision keeps the manifest in the content store under that digest (storeManifest). One report is built at
// a time in the process, because a build holds the bytes of the files that differ.
type changeReports struct {
	suites   governance.UnitOfWork
	contents governance.ContentReader
	budget   int // the bytes one build may read, maxReportBytes
	gate     chan struct{}
}

func newChangeReports(suites governance.UnitOfWork, contents governance.ContentReader) *changeReports {
	return &changeReports{suites: suites, contents: contents, budget: maxReportBytes, gate: make(chan struct{}, 1)}
}

// revisionReport is the report of a revision and the canonical version it compares with.
type revisionReport struct {
	Base   contract.SuiteVersionID
	Report report.Report
}

// revisionReport builds the report of the revision at ref: the manifest of the revision against the manifest of the canonical it expected,
// and the bytes of the files that differ. It waits for its turn until ctx ends. A revision it cannot find, one without a base canonical (a
// baseline) and one whose manifest was never stored are governance.ErrNotFound. A file that is not in the store is an error that wraps
// fs.ErrNotExist, and a revision over the byte budget is errReportTooLarge.
func (c *changeReports) revisionReport(ctx context.Context, ref contract.ProposalReference) (revisionReport, error) {
	select {
	case c.gate <- struct{}{}:
		defer func() { <-c.gate }()
	case <-ctx.Done():
		return revisionReport{}, ctx.Err()
	}
	version, base, digest, err := c.bindingOf(ctx, ref)
	if err != nil {
		return revisionReport{}, err
	}
	raw, err := c.contents.Read(ctx, digest)
	if errors.Is(err, fs.ErrNotExist) {
		return revisionReport{}, fmt.Errorf("%w: the manifest %s of %s-%s is not stored", governance.ErrNotFound, digest, ref.ProposalID, ref.RevisionID)
	} else if err != nil {
		return revisionReport{}, fmt.Errorf("read the manifest %s: %w", digest, err)
	}
	proposed, err := parseManifest(raw, digest)
	if err != nil {
		return revisionReport{}, err
	}
	changes, err := c.fileChanges(ctx, base, proposed)
	if err != nil {
		return revisionReport{}, err
	}
	built, err := report.Build(changes)
	if err != nil {
		return revisionReport{}, fmt.Errorf("build the report: %w", err)
	}
	return revisionReport{Base: version, Report: built}, nil
}

// bindingOf reads what the revision at ref binds: the canonical version it expected, that version's manifest and the digest of its own.
func (c *changeReports) bindingOf(ctx context.Context, ref contract.ProposalReference) (version contract.SuiteVersionID, base artifact.Manifest, digest artifact.Digest, err error) {
	err = c.suites.Do(ctx, ref.ProjectID, ref.SuiteID, func(ctx context.Context, tx governance.Tx) error {
		proposal, _, err := tx.Proposal(ctx, ref.ProposalID)
		if err != nil {
			return err
		}
		revisions := proposal.Revisions()
		i := slices.IndexFunc(revisions, func(r contract.ProposalRevision) bool { return r.Binding().Reference().RevisionID == ref.RevisionID })
		if i < 0 {
			return fmt.Errorf("%w: %s has no revision %s", governance.ErrNotFound, ref.ProposalID, ref.RevisionID)
		}
		binding := revisions[i].Binding()
		if version = binding.ExpectedCanonical(); version == "" {
			return fmt.Errorf("%w: %s-%s expects no canonical, so there is nothing to compare it with", governance.ErrNotFound, ref.ProposalID, ref.RevisionID)
		}
		canonical, found, err := tx.Version(ctx, version)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: the canonical version %s that %s-%s expected", governance.ErrNotFound, version, ref.ProposalID, ref.RevisionID)
		}
		base, digest = canonical.Version().Manifest(), binding.ManifestDigest()
		return nil
	})
	return version, base, digest, err
}

// fileChanges lists the files that differ between two manifests with the bytes of both sides, reading nothing else, and no more than the budget.
func (c *changeReports) fileChanges(ctx context.Context, base, proposed artifact.Manifest) ([]report.FileChange, error) {
	read := 0
	load := func(d artifact.Digest) ([]byte, error) {
		data, err := c.contents.Read(ctx, d)
		if err != nil {
			return nil, fmt.Errorf("read the file %s: %w", d, err)
		}
		if read += len(data); read > c.budget {
			return nil, errReportTooLarge
		}
		return data, nil
	}
	var changes []report.FileChange
	before, after := base.Entries(), proposed.Entries() // in path order
	for len(before) > 0 || len(after) > 0 {
		var change report.FileChange
		var err error
		switch {
		case len(after) == 0 || len(before) > 0 && before[0].Path < after[0].Path:
			change = report.FileChange{Path: before[0].Path, Status: report.Removed}
			change.Old, err = load(before[0].Content)
			before = before[1:]
		case len(before) == 0 || after[0].Path < before[0].Path:
			change = report.FileChange{Path: after[0].Path, Status: report.Added}
			change.New, err = load(after[0].Content)
			after = after[1:]
		default:
			old, now := before[0], after[0]
			before, after = before[1:], after[1:]
			if old.Content == now.Content {
				continue
			}
			change = report.FileChange{Path: now.Path, Status: report.Modified}
			if change.Old, err = load(old.Content); err == nil {
				change.New, err = load(now.Content)
			}
		}
		if err != nil {
			return nil, err
		}
		changes = append(changes, change)
	}
	return changes, nil
}

// manifestMagic starts the canonical encoding of a manifest, as artifact.NewManifest hashes it: a big-endian uint64 entry count, then each
// entry in path order as a big-endian uint64 path length, the path and the raw 32 bytes of its digest. The digest of a manifest is the SHA-256
// of that encoding, so the encoding can sit in the content store under the digest of the manifest it encodes: Put checks the bytes against
// it, and parseManifest checks that they are the manifest. shortcut: this mirrors the encoding of artifact.NewManifest, which is outside this
// package; move the encoder and the parser next to it if the encoding ever changes.
const manifestMagic = "suiteward.manifest.v1\x00"

// minEntryBytes is the least an entry takes in the encoding: the length of its path and its digest.
const minEntryBytes = 8 + 32

// encodeManifest is the canonical encoding of m: the bytes whose SHA-256 is m.Digest().
func encodeManifest(m artifact.Manifest) []byte {
	entries := m.Entries()
	raw := binary.BigEndian.AppendUint64([]byte(manifestMagic), uint64(len(entries)))
	for _, e := range entries {
		sum, err := hex.DecodeString(strings.TrimPrefix(e.Content.String(), "sha256:"))
		if err != nil || len(sum) != 32 { // NewManifest refuses an absent digest, and a digest is always 32 bytes
			panic("app: a manifest entry without a digest of 32 bytes")
		}
		raw = binary.BigEndian.AppendUint64(raw, uint64(len(e.Path)))
		raw = append(append(raw, e.Path...), sum...)
	}
	return raw
}

// errBadManifest says that bytes are not the encoding of the manifest they were stored under.
var errBadManifest = errors.New("not the canonical encoding of the manifest")

// parseManifest reads the canonical encoding raw of the manifest named digest. It refuses anything that is not exactly that encoding: another
// format, a count or a length beyond the bytes, trailing bytes, an invalid or repeated path, and bytes of another manifest.
func parseManifest(raw []byte, digest artifact.Digest) (artifact.Manifest, error) {
	rest, ok := bytes.CutPrefix(raw, []byte(manifestMagic))
	if !ok || len(rest) < 8 {
		return artifact.Manifest{}, fmt.Errorf("manifest %s: %w", digest, errBadManifest)
	}
	count := binary.BigEndian.Uint64(rest)
	rest = rest[8:]
	if count > uint64(len(rest))/minEntryBytes {
		return artifact.Manifest{}, fmt.Errorf("manifest %s: a count of entries beyond the bytes: %w", digest, errBadManifest)
	}
	var entries []artifact.Entry
	for range count {
		if len(rest) < 8 {
			return artifact.Manifest{}, fmt.Errorf("manifest %s: %w", digest, errBadManifest)
		}
		size := binary.BigEndian.Uint64(rest)
		rest = rest[8:]
		if size > uint64(len(rest)) || uint64(len(rest))-size < 32 {
			return artifact.Manifest{}, fmt.Errorf("manifest %s: a path beyond the bytes: %w", digest, errBadManifest)
		}
		path, sum := string(rest[:size]), rest[size:size+32]
		rest = rest[size+32:]
		content, err := artifact.ParseDigest("sha256:" + hex.EncodeToString(sum))
		if err != nil {
			return artifact.Manifest{}, fmt.Errorf("manifest %s: %w: %w", digest, errBadManifest, err)
		}
		entries = append(entries, artifact.Entry{Path: path, Content: content})
	}
	if len(rest) != 0 {
		return artifact.Manifest{}, fmt.Errorf("manifest %s: trailing bytes: %w", digest, errBadManifest)
	}
	m, err := artifact.NewManifest(entries)
	if err != nil {
		return artifact.Manifest{}, fmt.Errorf("manifest %s: %w: %w", digest, errBadManifest, err)
	}
	if m.Digest() != digest {
		return artifact.Manifest{}, fmt.Errorf("manifest %s: the entries are those of %s: %w", digest, m.Digest(), errBadManifest)
	}
	return m, nil
}

// storeManifest keeps the canonical encoding of m in the content store, under the digest of m. It is idempotent, like every Put.
func storeManifest(ctx context.Context, content governance.ContentWriter, m artifact.Manifest) error {
	if err := content.Put(ctx, m.Digest(), bytes.NewReader(encodeManifest(m))); err != nil {
		return fmt.Errorf("store the manifest %s: %w", m.Digest(), err)
	}
	return nil
}
