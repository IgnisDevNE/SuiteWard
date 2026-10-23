package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/application/commandtext"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
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
	proposed, err := artifact.ParseManifest(raw)
	if err == nil && proposed.Digest() != digest { // bytes that are the encoding of another manifest than the one the revision names
		err = fmt.Errorf("%w: the entries are those of %s", artifact.ErrInvalidEncoding, proposed.Digest())
	}
	if err != nil {
		return revisionReport{}, fmt.Errorf("the manifest %s: %w", digest, err)
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

// revisionReference is the reference of revision R<revision> of proposal P<proposal> in the default Suite of a repository.
func revisionReference(repositoryID int64, proposal, revision int) contract.ProposalReference {
	return contract.ProposalReference{ProjectID: owner.ProjectID(repositoryID), SuiteID: defaultSuite, ProposalID: proposalID(proposal), RevisionID: contract.ProposalRevisionID("R" + strconv.Itoa(revision))}
}

// summarize states the numbers of the revision at ref for a comment or a check summary. A report that cannot be built is no reason to withhold what the
// comment or the check says otherwise: found is then false and the cause is logged under what, so the caller posts without the numbers. A delivery
// whose context ended is the only error, because it is delivered again and the numbers are then counted.
func summarize(ctx context.Context, reports reportSource, logger *slog.Logger, what string, ref contract.ProposalReference) (changes commandtext.Changes, found bool, err error) {
	built, err := reports.revisionReport(ctx, ref)
	if cause := ctx.Err(); cause != nil {
		return commandtext.Changes{}, false, cause
	}
	if err != nil {
		logger.WarnContext(ctx, what+": the change report is unavailable, so the numbers are left out", "reference", referenceText(ref), "error", err)
		return commandtext.Changes{}, false, nil
	}
	return commandtext.ChangesOf(built.Report), true, nil
}

// reportURL is the link to the report of a revision of repo (owner/name) on the instance at base, or "" without a base.
func reportURL(base, repo, reference string) string {
	if base == "" {
		return ""
	}
	owner, name, _ := strings.Cut(repo, "/")
	return base + "/reports/" + url.PathEscape(owner) + "/" + url.PathEscape(name) + "/" + reference
}

// storeManifest keeps the canonical encoding of m in the content store, under the digest of m. It is idempotent, like every Put.
func storeManifest(ctx context.Context, content governance.ContentWriter, m artifact.Manifest) error {
	if err := content.Put(ctx, m.Digest(), bytes.NewReader(m.Encoding())); err != nil {
		return fmt.Errorf("store the manifest %s: %w", m.Digest(), err)
	}
	return nil
}
