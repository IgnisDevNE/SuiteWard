// Package profiles is the list of the sandbox profiles: one constant of the release that the service, the test runner and
// the installer read. It reads no file, runs no command and asks no registry.
package profiles

import "errors"

// Profile is one entry of the list.
type Profile struct {
	ID         string
	Name       string
	Image      string
	ImageBytes int64
	BuildFiles []string
}

// All returns the list in order.
func All() []Profile { return nil }

// ByID returns the profile of an id.
func ByID(id string) (Profile, error) { return Profile{}, errors.New("not implemented") }

// Suggest returns the ids of the profiles that the paths suggest.
func Suggest(paths []string) []string { return nil }
