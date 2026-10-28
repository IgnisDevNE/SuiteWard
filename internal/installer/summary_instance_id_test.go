package installer

import (
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// The summaries of the backup family name the row of the instance's 8-character id "Instance ID", as the install summary does, so that the
// word instance is not left to mean the id in one place and the installation in another.
func TestTheSummariesOfTheBackupFamilyNameTheInstanceRowInstanceID(t *testing.T) {
	doc := statusDoc{Instance: "1a2b3c4d", Version: "v0.2.0"}
	for name, rows := range map[string][]ui.KV{
		"backup":      backupSummary(t.TempDir(), doc, "age1pq1x"),
		"backup init": (&keyInit{inst: &instance{}, instance: "1a2b3c4d", recipient: "age1pq1x"}).summary(),
		"restore":     (&restoreRun{doc: doc, after: doc}).summary(),
	} {
		var id string
		for _, kv := range rows {
			if kv.Key == "Instance" {
				t.Errorf("the %s summary still names the row Instance", name)
			}
			if kv.Key == "Instance ID" {
				id = kv.Value
			}
		}
		if id != "1a2b3c4d" {
			t.Errorf("the %s summary has the Instance ID %q, want 1a2b3c4d: %+v", name, id, rows)
		}
	}
}
