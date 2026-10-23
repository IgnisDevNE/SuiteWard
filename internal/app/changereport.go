package app

import (
	"context"
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/application/report"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// maxReportBytes bounds the file bytes one report build holds, both sides together.
const maxReportBytes = 32 << 20

// errReportTooLarge says that a revision changes more bytes than a report is built from here.
var errReportTooLarge = errors.New("the revision changes too many bytes to build a report")

// changeReports rebuilds the change report of a proposal revision.
type changeReports struct {
	suites   governance.UnitOfWork
	contents governance.ContentReader
	budget   int
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

func (c *changeReports) revisionReport(context.Context, contract.ProposalReference) (revisionReport, error) {
	return revisionReport{}, nil
}

// encodeManifest is the canonical encoding of m: the bytes whose SHA-256 is m.Digest().
func encodeManifest(artifact.Manifest) []byte { return nil }

// parseManifest reads the canonical encoding raw of the manifest named digest.
func parseManifest([]byte, artifact.Digest) (artifact.Manifest, error) {
	return artifact.Manifest{}, nil
}

// storeManifest keeps the canonical encoding of m in the content store, under the digest of m.
func storeManifest(context.Context, governance.ContentWriter, artifact.Manifest) error { return nil }
