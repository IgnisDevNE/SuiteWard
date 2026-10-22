package app

import (
	"context"
	"io"

	"github.com/IgnisDevNE/SuiteWard/internal/application/owner"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// selfCheckResult is one line of `suiteward check`.
type selfCheckResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

func parseSelfCheck(args []string, stderr io.Writer) (asJSON bool, err error) { return false, nil }

func selfCheckGitHubResults(ctx context.Context, g config.GitHub, projects []owner.ProjectPolicy) []selfCheckResult {
	return nil
}

func selfCheckDatabaseResults(ctx context.Context, cfg config.Config) (schema, role, enrolled selfCheckResult, projects []owner.ProjectPolicy) {
	return
}

func writeSelfCheck(w io.Writer, results []selfCheckResult, asJSON bool) error { return nil }

func runSelfCheck(ctx context.Context, cfg config.Config, asJSON bool, stdout, stderr io.Writer) int {
	return 0
}
