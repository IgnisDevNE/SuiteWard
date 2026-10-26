package app

import (
	"maps"
	"testing"
)

// The archive token reads a commit's source: it asks for the contents and nothing a pull request or a check could use.
func TestArchivePermissionsAreMetadataAndContentsReadOnly(t *testing.T) {
	want := map[string]string{"metadata": "read", "contents": "read"}
	if !maps.Equal(archivePermissions, want) {
		t.Errorf("archivePermissions = %v, want exactly %v", archivePermissions, want)
	}
}
