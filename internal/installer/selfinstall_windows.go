//go:build windows

package installer

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"runtime"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// osSelfInstall is this PC: the Path of the user's own environment, which a new terminal reads.
func osSelfInstall() *SelfInstall {
	return &SelfInstall{Executable: os.Executable, LookPath: exec.LookPath, Rename: os.Rename, Path: registryPath{key: `Environment`}}
}

// registryPath is the value Path under HKEY_CURRENT_USER\key, read and written as it is stored: the kind of the value, expandable or not,
// is kept, because a Path of the expandable kind holds entries such as %SystemRoot%\system32 that must not be expanded into it.
type registryPath struct{ key string }

func (p registryPath) Read() (value string, expand, found bool, err error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, p.key, registry.QUERY_VALUE)
	if err != nil {
		return "", false, false, fmt.Errorf(`open HKEY_CURRENT_USER\%s: %w`, p.key, err)
	}
	defer func() { _ = k.Close() }()
	value, kind, err := k.GetStringValue("Path")
	switch {
	case errors.Is(err, registry.ErrNotExist):
		return "", false, false, nil
	case err != nil:
		return "", false, false, fmt.Errorf(`read the Path of HKEY_CURRENT_USER\%s: %w`, p.key, err)
	}
	return value, kind == registry.EXPAND_SZ, true, nil
}

func (p registryPath) Write(value string, expand bool) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, p.key, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf(`open HKEY_CURRENT_USER\%s: %w`, p.key, err)
	}
	defer func() { _ = k.Close() }()
	if expand {
		err = k.SetExpandStringValue("Path", value)
	} else {
		err = k.SetStringValue("Path", value)
	}
	if err != nil {
		return fmt.Errorf(`write the Path of HKEY_CURRENT_USER\%s: %w`, p.key, err)
	}
	return nil
}

// Announce broadcasts WM_SETTINGCHANGE for "Environment", as the system's own dialog does: Explorer reads the variables again, and a terminal
// that is opened from it afterwards has the new Path.
func (registryPath) Announce() error { return broadcastSettingChange("Environment") }

var sendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

const (
	hwndBroadcast   = 0xffff
	wmSettingChange = 0x001a
	smtoAbortIfHung = 0x0002
	broadcastWaitMs = 5000 // for each window: one that does not answer is skipped
)

// broadcastSettingChange sends WM_SETTINGCHANGE with the name of the area that changed to every top-level window.
func broadcastSettingChange(area string) error {
	text, err := windows.UTF16PtrFromString(area)
	if err != nil {
		return err
	}
	// The pointer is handed over as a number, from the value that Go keeps alive until the call returns.
	r, _, callErr := sendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0, reflect.ValueOf(text).Pointer(), smtoAbortIfHung, broadcastWaitMs, 0)
	runtime.KeepAlive(text)
	if r == 0 {
		return fmt.Errorf("SendMessageTimeout: %w", callErr)
	}
	return nil
}
