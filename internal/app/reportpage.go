package app

import (
	"context"
	"net/http"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/contract"
)

// reportSource is the part of changeReports the report page reads.
type reportSource interface {
	revisionReport(ctx context.Context, ref contract.ProposalReference) (revisionReport, error)
}

func (a *api) report(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
