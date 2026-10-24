//go:build windows

package installer

import "errors"

// osSelfInstall is this PC: the Path of the user's own environment, which a new terminal reads.
func osSelfInstall() *SelfInstall { return nil }

// registryPath is the value Path under HKEY_CURRENT_USER\key, read and written as it is stored.
type registryPath struct{ key string }

func (p registryPath) Read() (value string, expand, found bool, err error) { return "", false, false, nil }

func (p registryPath) Write(value string, expand bool) error { return nil }

func (registryPath) Announce() error { return nil }

// broadcastSettingChange sends WM_SETTINGCHANGE with the name of the area that changed to every top-level window.
func broadcastSettingChange(area string) error { return errNotDone }

var errNotDone = errors.New("not done")
