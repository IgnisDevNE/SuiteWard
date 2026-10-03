package dbgen

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

type CASAuthorityParams struct {
	ProjectID         string
	SuiteID           string
	ExpectedRevision  string
	NewRevision       string
	ExpectedCurrent   pgtype.Text
	NewCurrent        pgtype.Text
	GovernancePayload []byte
}

func (*Queries) CASAuthority(context.Context, CASAuthorityParams) (int64, error) { return 0, nil }
