package installer

import "errors"

// rulesetJSON is the ruleset the user imports: the contract check pinned to the App and the test check from any source.
func rulesetJSON(appID int64, testCheck string) ([]byte, error) { return nil, errors.New("not implemented") }

// writeRuleset writes the ruleset of repo (owner/name) into dir and returns its path.
func writeRuleset(dir, repo string, data []byte) (string, error) {
	return "", errors.New("not implemented")
}
