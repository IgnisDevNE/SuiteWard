package installer

import (
	"strings"
	"testing"
)

// psql echoes a failing statement, and the statement of the role holds its new password: neither tier may show it.
func TestInstallIsolatedRoleFailureNeverShowsThePassword(t *testing.T) {
	f := newIsoFake(t.TempDir())
	f.roleFails = true
	out, err := runIsolated(t, f, nil, "")
	if err == nil {
		t.Fatal("the role statement failed, so the install must fail")
	}
	var passwords []string
	for _, c := range f.calls {
		if m := passwordInSQL.FindStringSubmatch(c.stdin); m != nil {
			passwords = append(passwords, m[1])
		}
	}
	if len(passwords) == 0 {
		t.Fatal("no role statement ran, so this proves nothing")
	}
	for _, password := range passwords {
		if strings.Contains(err.Error(), password) || strings.Contains(out, password) {
			t.Errorf("the role password is in the error or the output:\n%v\n%s", err, out)
		}
	}
	if strings.Contains(err.Error(), "LINE 1") {
		t.Errorf("the statement psql echoed is in the error: %v", err)
	}
}
