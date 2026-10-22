package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPendingAppValidateNamesTheBadField(t *testing.T) {
	good := PendingApp{AppID: 7, Slug: "suiteward-abcd", Account: "acme", AccountType: "Organization"}
	if err := good.validate(); err != nil {
		t.Fatalf("a valid record was refused: %v", err)
	}
	cases := []struct {
		name string
		edit func(*PendingApp)
		want string
	}{
		{"app id", func(p *PendingApp) { p.AppID = 0 }, "app_id"},
		{"slug", func(p *PendingApp) { p.Slug = "Not A Slug!" }, "slug"},
		{"account", func(p *PendingApp) { p.Account = "bad login!" }, "account is not"},
		{"account type", func(p *PendingApp) { p.AccountType = "Bot" }, "account_type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := good
			tc.edit(&p)
			err := p.validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate = %v, want an error naming %q", err, tc.want)
			}
		})
	}
}

// An invalid App is never recorded: a record nothing can load again would wedge every re-run.
func TestSavePendingAppRefusesAnInvalidAppAndWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.yaml.pending-app")
	err := savePendingApp(path, PendingApp{AppID: 0})
	if err == nil || !strings.Contains(err.Error(), "refusing to record an invalid App") {
		t.Fatalf("save = %v, want the refusal", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("a record was written for an invalid App: %v", statErr)
	}
}

// A record whose directory cannot be made is an error, not a silent loss of the only link to the created App.
func TestSavePendingAppReportsAnUnwritableRecord(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := PendingApp{AppID: 7, Slug: "suiteward-abcd", Account: "acme", AccountType: "User"}
	if err := savePendingApp(filepath.Join(blocker, "sub", "settings.yaml.pending-app"), p); err == nil {
		t.Fatal("a record under a regular file was reported as saved")
	}
}

func TestLoadPendingAppRefusesAnUnreadableOrDamagedRecord(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name    string
		content string // "" with isDir true means the path is a directory
		isDir   bool
		want    string
	}{
		{"a directory", "", true, "read the record of the App"},
		{"unknown field", "app_id: 7\nsurprise: 1\n", false, "delete it if its App is gone"},
		{"not yaml", "app_id: [\n", false, "parse "},
		{"invalid values", "app_id: 0\nslug: x\n", false, "invalid record of an App"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.Repeat("p", i+1))
			if tc.isDir {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			p, found, err := loadPendingApp(path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("load = %v, want an error containing %q", err, tc.want)
			}
			if found || p != (PendingApp{}) {
				t.Errorf("a refused record came back as found: %+v %v", p, found)
			}
		})
	}
}
