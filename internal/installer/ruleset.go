package installer

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
)

type requiredCheck struct {
	Context       string `json:"context"`
	IntegrationID int64  `json:"integration_id,omitempty"` // 0 leaves the key out: any source
}

type rulesetRule struct {
	Type       string `json:"type"`
	Parameters struct {
		Strict bool            `json:"strict_required_status_checks_policy"`
		Checks []requiredCheck `json:"required_status_checks"`
	} `json:"parameters"`
}

type rulesetFile struct {
	Name        string `json:"name"`
	Target      string `json:"target"`
	Enforcement string `json:"enforcement"`
	Conditions  struct {
		RefName struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"ref_name"`
	} `json:"conditions"`
	Rules        []rulesetRule `json:"rules"`
	BypassActors []struct{}    `json:"bypass_actors"`
}

// rulesetJSON is the ruleset the user imports, in the REST shape that spike S2 proved: SuiteWard / Contract pinned to the
// App, the test check from any source, the default branch, strict, active, an empty bypass list.
func rulesetJSON(appID int64, testCheck string) ([]byte, error) {
	testCheck = strings.TrimSpace(testCheck)
	switch {
	case appID <= 0:
		return nil, errors.New("the ruleset needs the App id")
	case testCheck == "":
		return nil, errors.New("the ruleset needs a test check")
	case testCheck == protection.ContractCheck:
		return nil, fmt.Errorf("%s cannot be the test check", protection.ContractCheck)
	}
	rs := rulesetFile{Name: "SuiteWard contract", Target: "branch", Enforcement: "active", Rules: []rulesetRule{{Type: "required_status_checks"}}, BypassActors: []struct{}{}}
	rs.Conditions.RefName.Include = []string{"~DEFAULT_BRANCH"}
	rs.Conditions.RefName.Exclude = []string{}
	rs.Rules[0].Parameters.Strict = true
	rs.Rules[0].Parameters.Checks = []requiredCheck{{protection.ContractCheck, appID}, {testCheck, 0}}
	raw, err := json.MarshalIndent(rs, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode the ruleset: %w", err)
	}
	return append(raw, '\n'), nil
}

// writeRuleset writes the ruleset of repo (owner/name) into dir as suiteward-ruleset-<owner>-<name>.json (0600) and returns its path.
func writeRuleset(dir, repo string, data []byte) (string, error) {
	owner, name, _ := strings.Cut(repo, "/")
	if !repositoryName.MatchString(repo) || owner == "." || owner == ".." || name == "." || name == ".." {
		return "", fmt.Errorf("repository %q is not an owner/name pair", repo)
	}
	path := filepath.Join(dir, "suiteward-ruleset-"+owner+"-"+name+".json")
	if err := writeFileAtomic(path, data, 0o700); err != nil {
		return "", err
	}
	return path, nil
}
