//go:build windows

package installer

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/sys/windows/registry"
)

// registryPath reads and writes the Path value of a key of HKEY_CURRENT_USER. These tests give it a key of their own under
// Software\SuiteWardTest, which each test removes: the Environment of the user that runs them is never touched.

// aUserKey is an empty key of HKEY_CURRENT_USER for the test, and its path.
func aUserKey(t *testing.T) (registry.Key, string) {
	t.Helper()
	path := `Software\SuiteWardTest\` + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.ALL_ACCESS)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = k.Close()
		_ = registry.DeleteKey(registry.CURRENT_USER, path)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\SuiteWardTest`) // gone when no other test holds a key under it
	})
	return k, path
}

func rawPath(t *testing.T, k registry.Key) (string, uint32) {
	t.Helper()
	value, kind, err := k.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "(no Path)", 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return value, kind
}

func TestTheRegistryPathIsReadAsItIsStoredAndNotExpanded(t *testing.T) {
	k, path := aUserKey(t)
	if _, _, found, err := (registryPath{key: path}).Read(); err != nil || found {
		t.Fatalf("Read of a key with no Path = found %v, %v; want not found and no error", found, err)
	}
	if err := k.SetExpandStringValue("Path", `%SystemRoot%\system32;C:\A`); err != nil {
		t.Fatal(err)
	}
	value, expand, found, err := (registryPath{key: path}).Read()
	if err != nil || !found || !expand || value != `%SystemRoot%\system32;C:\A` {
		t.Errorf("Read = %q, expand %v, found %v, %v; want the value with %%SystemRoot%% as stored", value, expand, found, err)
	}
	if err := k.SetStringValue("Path", `C:\B`); err != nil {
		t.Fatal(err)
	}
	if value, expand, found, err = (registryPath{key: path}).Read(); err != nil || !found || expand || value != `C:\B` {
		t.Errorf("Read of a REG_SZ = %q, expand %v, found %v, %v", value, expand, found, err)
	}
	if _, _, _, err := (registryPath{key: path + `\missing`}).Read(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("Read of a key that is not there = %v, want an error that names it", err)
	}
}

func TestTheRegistryPathKeepsItsKindWhenTheFolderIsAddedOrRemoved(t *testing.T) {
	for _, c := range []struct {
		name    string
		set     func(k registry.Key, value string) error
		want    uint32
		initial string
	}{
		{"expandable", func(k registry.Key, v string) error { return k.SetExpandStringValue("Path", v) }, registry.EXPAND_SZ, `%SystemRoot%\system32;C:\A`},
		{"plain", func(k registry.Key, v string) error { return k.SetStringValue("Path", v) }, registry.SZ, `C:\Windows\system32;C:\A`},
	} {
		t.Run(c.name, func(t *testing.T) {
			k, path := aUserKey(t)
			if err := c.set(k, c.initial); err != nil {
				t.Fatal(err)
			}
			self := SelfInstall{Path: registryPath{key: path}}
			if changed, err := self.addToUserPath(folderOnPath); err != nil || !changed {
				t.Fatalf("addToUserPath = %v, %v", changed, err)
			}
			if value, kind := rawPath(t, k); value != c.initial+";"+folderOnPath || kind != c.want {
				t.Errorf("after the add the Path is %q of kind %d, want %q of kind %d", value, kind, c.initial+";"+folderOnPath, c.want)
			}
			if changed, err := self.removeFromUserPath(folderOnPath); err != nil || !changed {
				t.Fatalf("removeFromUserPath = %v, %v", changed, err)
			}
			if value, kind := rawPath(t, k); value != c.initial || kind != c.want {
				t.Errorf("after the removal the Path is %q of kind %d, want %q of kind %d", value, kind, c.initial, c.want)
			}
		})
	}
}

func TestAUserWithoutAPathInTheRegistryGetsOneOfTheExpandableKind(t *testing.T) {
	k, path := aUserKey(t)
	if changed, err := (SelfInstall{Path: registryPath{key: path}}).addToUserPath(folderOnPath); err != nil || !changed {
		t.Fatalf("addToUserPath = %v, %v", changed, err)
	}
	if value, kind := rawPath(t, k); value != folderOnPath || kind != registry.EXPAND_SZ {
		t.Errorf("the new Path is %q of kind %d, want the folder as REG_EXPAND_SZ", value, kind)
	}
}

// The area is one that nothing listens for: the call is made as the real one is, to every top-level window, and changes no environment.
func TestTheSettingChangeIsBroadcastToEveryWindow(t *testing.T) {
	if err := broadcastSettingChange("SuiteWardTestNothingChanged"); err != nil {
		t.Errorf("broadcastSettingChange = %v, want the call to succeed", err)
	}
}
