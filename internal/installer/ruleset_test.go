package installer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const goldenRuleset = `{
  "name": "SuiteWard contract",
  "target": "branch",
  "enforcement": "active",
  "conditions": {
    "ref_name": {
      "include": [
        "~DEFAULT_BRANCH"
      ],
      "exclude": []
    }
  },
  "rules": [
    {
      "type": "required_status_checks",
      "parameters": {
        "strict_required_status_checks_policy": true,
        "required_status_checks": [
          {
            "context": "SuiteWard / Contract",
            "integration_id": 4242
          },
          {
            "context": "CI / test"
          }
        ]
      }
    }
  ],
  "bypass_actors": []
}
`

func TestRulesetJSONGolden(t *testing.T) {
	got, err := rulesetJSON(fakeAppID, "CI / test")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != goldenRuleset {
		t.Fatalf("ruleset JSON differs from the one S2 proved:\n%s", got)
	}
}

func TestRulesetJSONRefusesWhatCannotBeRequired(t *testing.T) {
	for _, tc := range []struct {
		appID int64
		check string
	}{{0, "CI / test"}, {fakeAppID, ""}, {fakeAppID, "  "}, {fakeAppID, "SuiteWard / Contract"}} {
		if _, err := rulesetJSON(tc.appID, tc.check); err == nil {
			t.Errorf("rulesetJSON(%d, %q) was accepted", tc.appID, tc.check)
		}
	}
}

func TestWriteRulesetIsPrivateAndNamedAfterTheRepository(t *testing.T) {
	dir := t.TempDir()
	path, err := writeRuleset(dir, "acme/app", []byte(goldenRuleset))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "suiteward-ruleset-acme-app.json"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil || string(raw) != goldenRuleset {
		t.Fatalf("file = %q, %v", raw, err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 600", info.Mode().Perm())
	}
	for _, repo := range []string{"../x/y", "acme", "acme/app/extra", "a b/c"} {
		if _, err := writeRuleset(dir, repo, nil); err == nil {
			t.Errorf("writeRuleset(%q) was accepted", repo)
		}
	}
}
