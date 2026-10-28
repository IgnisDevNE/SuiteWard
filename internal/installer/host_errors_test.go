package installer

import (
	"context"
	"io"
	"strings"
	"testing"
)

// Each step of the host preparation that fails stops it with an error that names the step; no later step changes the host.
func TestPrepareHostStopsAtTheStepThatFails(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(h *hostFake)
		want     string
		notAfter [2]string // a command (name, text of its arguments) that must not have run
	}{
		{"the user id", func(h *hostFake) { h.failOn["id -u"] = errInjected }, "find out which user is running the installer", [2]string{"podman", ""}},
		{"the installation of podman", func(h *hostFake) {
			h.podman = ""
			h.failOn["dnf install -y podman"] = errInjected
		}, "dnf install -y podman", [2]string{"useradd", ""}},
		{"a podman that does not run after its installation", func(h *hostFake) {
			h.podman = ""
			h.failOn["podman --version"] = errInjected
		}, "Podman still does not run after it was installed", [2]string{"useradd", ""}},
		{"the upgrade of an old podman", func(h *hostFake) {
			h.podman = "podman version 5.4.0"
			h.failOn["dnf upgrade -y podman"] = errInjected
		}, "dnf upgrade -y podman", [2]string{"useradd", ""}},
		{"an upgrade that leaves podman old", func(h *hostFake) { h.podman = "podman version 5.4.0" }, "needs Podman 5.5 or later", [2]string{"useradd", ""}},
		{"the changelog of podman", func(h *hostFake) { h.failOn["rpm -q"] = errInjected }, "read the changelog of the Podman package", [2]string{"useradd", ""}},
		{"the upgrade for the CVE", func(h *hostFake) {
			h.changelog = ""
			h.failOn["dnf upgrade -y podman"] = errInjected
		}, "dnf upgrade -y podman", [2]string{"useradd", ""}},
		{"the creation of the account", func(h *hostFake) {
			h.account = false
			h.failOn["useradd"] = errInjected
		}, "create the account suiteward", [2]string{"loginctl", ""}},
		{"the subuid file", func(h *hostFake) { h.failOn["cat /etc/subuid"] = errInjected }, "read /etc/subuid", [2]string{"loginctl", ""}},
		{"a subgid file that is not understood", func(h *hostFake) { h.subgid = "garbage\n" }, "/etc/subgid: cannot read the line", [2]string{"loginctl", ""}},
		{"the subordinate ranges", func(h *hostFake) {
			h.subuid, h.subgid = "", ""
			h.failOn["usermod"] = errInjected
		}, "add the id ranges (subuid and subgid)", [2]string{"loginctl", ""}},
		{"linger", func(h *hostFake) { h.failOn["loginctl enable-linger"] = errInjected }, "turn on linger for suiteward", [2]string{"curl", ""}},
		{"curl", func(h *hostFake) { h.failOn["curl --version"] = errInjected }, "curl is needed", [2]string{"tee", "imds-block"}},
		{"the installation of nft", func(h *hostFake) {
			h.metadata = "200"
			h.failOn["command -v nft"] = errInjected
			h.failOn["dnf install -y nftables"] = errInjected
		}, "dnf install -y nftables", [2]string{"tee", "imds-block"}},
		{"an nft that is still missing", func(h *hostFake) {
			h.metadata = "200"
			h.failOn["command -v nft"] = errInjected
		}, "is still missing after the installation", [2]string{"tee", "imds-block"}},
		{"the directory of the rule", func(h *hostFake) {
			h.metadata = "200"
			h.failOn["mkdir -p"] = errInjected
		}, errInjected.Error(), [2]string{"tee", ""}},
		{"the write of the rule", func(h *hostFake) {
			h.metadata = "200"
			h.failOn["tee"] = errInjected
		}, "write /etc/", [2]string{"systemctl", "daemon-reload"}},
		{"the enable of the rule unit", func(h *hostFake) {
			h.metadata = "200"
			h.failOn["systemctl enable --now"] = errInjected
		}, "systemctl enable --now", [2]string{"getent", ""}},
		{"a rule that is not loaded", func(h *hostFake) {
			h.metadata = "200"
			h.failOn["nft list table"] = errInjected
		}, "the metadata rule (the firewall rule) is not loaded", [2]string{"getent", ""}},
		{"a table without the reject rule", func(h *hostFake) {
			h.metadata = "200"
			h.nftOutput = "table inet suiteward_imds { }"
		}, "has no reject rule", [2]string{"getent", ""}},
		{"the account entry", func(h *hostFake) { h.failOn["getent"] = errInjected }, "look up the account suiteward", [2]string{"install", "-d"}},
		{"the creation of the configuration directory", func(h *hostFake) { h.failOn["install -d -o suiteward"] = errInjected }, "create /home/suiteward/.config/suiteward", [2]string{"cmp", ""}},
		{"the copy of the env file", func(h *hostFake) {
			h.failOn["install -o suiteward -g suiteward -m 0600 /etc/suiteward/github.env"] = errInjected
		}, "copy /etc/suiteward/github.env", [2]string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHostFake()
			h.failOn = map[string]error{}
			tc.setup(h)
			_, err := runHost(t, h, map[string]string{"yes": "true", "metadata-rule": "yes"}, false, "")
			if err == nil {
				t.Fatal("PrepareHost succeeded although a step failed")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("PrepareHost = %v, want an error containing %q", err, tc.want)
			}
			if tc.notAfter[0] != "" && h.has(tc.notAfter[0], tc.notAfter[1]) {
				t.Errorf("%s %s ran after the failure: %v", tc.notAfter[0], tc.notAfter[1], h.lines())
			}
		})
	}
}

func TestNextNeedsTheIDOfTheServiceAccount(t *testing.T) {
	h := newHostFake()
	h.failOn = map[string]error{"id -u suiteward": errInjected}
	hst := &host{d: h.deps(), out: io.Discard}
	if err := hst.next(context.Background()); err == nil || !strings.Contains(err.Error(), "look up the user id of suiteward") {
		t.Fatalf("next = %v", err)
	}
}

func TestPrepareHostAsksOrRefusesTheMetadataQuestion(t *testing.T) {
	t.Run("not a terminal and no answer", func(t *testing.T) {
		h := newHostFake()
		h.metadata = "200"
		_, err := runHost(t, h, nil, false, "")
		if err == nil || !strings.Contains(err.Error(), "--metadata-rule") {
			t.Fatalf("PrepareHost = %v", err)
		}
		if writeOf(h, imdsRuleFile) != "" || h.has("mkdir", "-p /etc/suiteward") {
			t.Errorf("the rule was written without an answer: %v", h.lines())
		}
	})
	t.Run("a terminal that is closed", func(t *testing.T) {
		h := newHostFake()
		h.metadata = "200"
		_, err := runHost(t, h, nil, true, "")
		if err == nil || !strings.Contains(err.Error(), "read your answer for metadata-rule") {
			t.Fatalf("PrepareHost = %v", err)
		}
	})
	t.Run("an answer that is neither yes nor no", func(t *testing.T) {
		h := newHostFake()
		h.metadata = "200"
		_, err := runHost(t, h, map[string]string{"metadata-rule": "perhaps"}, false, "")
		if err == nil || !strings.Contains(err.Error(), `"perhaps" is not yes or no`) {
			t.Fatalf("PrepareHost = %v", err)
		}
	})
	t.Run("an answer typed that is not understood", func(t *testing.T) {
		h := newHostFake()
		h.metadata = "200"
		_, err := runHost(t, h, nil, true, "maybe\n")
		if err == nil || !strings.Contains(err.Error(), "is not yes or no") {
			t.Fatalf("PrepareHost = %v", err)
		}
	})
}

func TestFreeSubidRangeReadsOnlyWhatItUnderstands(t *testing.T) {
	cases := []struct {
		name, content string
		start         int
		has           bool
		err           string
	}{
		{"empty", "", subidFloor, false, ""},
		{"comments and blank lines", "# a comment\n\n  \nsuiteward:100000:65536\n", 165536, true, ""},
		{"another account's range comes first", "alice:165536:65536\n", 231072, false, ""},
		{"a range below the floor", "root:0:65536\n", subidFloor, false, ""},
		{"too few fields", "alice:1\n", 0, false, `cannot read the line "alice:1"`},
		{"too many fields", "alice:1:2:3\n", 0, false, "cannot read the line"},
		{"a first id that is not a number", "alice:x:65536\n", 0, false, "cannot read the line"},
		{"a count that is not a number", "alice:100000:many\n", 0, false, "cannot read the line"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, has, err := freeSubidRange(tc.content, "suiteward")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("freeSubidRange = %v, want an error containing %q", err, tc.err)
				}
				return
			}
			if err != nil || start != tc.start || has != tc.has {
				t.Fatalf("freeSubidRange = %d, %v, %v; want %d, %v", start, has, err, tc.start, tc.has)
			}
		})
	}
}

func TestAccountHomeNeedsTheAccountEntry(t *testing.T) {
	h := newHostFake()
	h.failOn = map[string]error{}
	hst := &host{d: h.deps()}
	if home, err := hst.accountHome(context.Background()); err != nil || home != "/home/suiteward" {
		t.Fatalf("accountHome = %q, %v", home, err)
	}
	h.failOn["getent"] = errInjected
	if _, err := hst.accountHome(context.Background()); err == nil || !strings.Contains(err.Error(), "look up the account suiteward") {
		t.Errorf("accountHome of an account that cannot be looked up = %v", err)
	}
}

func TestPodmanAtLeastReadsVersionText(t *testing.T) {
	for text, want := range map[string]bool{
		"podman version 5.5.0":  true,
		"podman version 5.4.9":  false,
		"podman version 6.0":    true,
		"podman version 4.9.4":  false,
		"podman version 5.10.1": true,
		"podman":                false,
		"podman version 5":      false,
	} {
		if got := podmanAtLeast(text, 5, 5); got != want {
			t.Errorf("podmanAtLeast(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestPrepareHostRefusesDepsWithoutAnExec(t *testing.T) {
	r := newTestResolver(t, nil, nil, Terminal{In: strings.NewReader(""), Out: io.Discard})
	if err := PrepareHost(context.Background(), r, Deps{GOOS: "linux", Getenv: func(string) string { return "" }}, io.Discard); err == nil {
		t.Fatal("the host was prepared without a way to run commands")
	}
}
