package installer

import (
	"errors"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

var errNotImplemented = errors.New("not implemented")

// DefaultEnvFile is where the isolated service account keeps the non-secret GitHub values (D-INSTALLER).
const DefaultEnvFile = "%h/.config/suiteward/github.env"

// UnitParams are the per-install values of the rendered quadlet units.
type UnitParams struct {
	Digest     string // the signed index digest of the release image, sha256:<64 hex>
	Tier       config.Tier
	HTTPPort   int
	MCPPort    int
	JobTimeout string // optional, a Go duration; serve only
	EnvFile    string // EnvironmentFile of the service, DefaultEnvFile when empty
}

// UnitParamsFrom takes the ports, tier and job timeout from the settings.
func UnitParamsFrom(s Settings, digest string) UnitParams {
	return UnitParams{Digest: digest, Tier: s.Tier, HTTPPort: s.HTTPPort, MCPPort: s.MCPPort, JobTimeout: s.JobTimeout}
}

// RenderUnits fills the embedded quadlet units: both release Image= lines with one digest, the tier, the published ports,
// the job timeout and the GitHub env file. The PostgreSQL pin stays as written. It returns the unit files by name.
func RenderUnits(p UnitParams) (map[string][]byte, error) { return nil, errNotImplemented }

// render fills templates; every anchor line it replaces must be there exactly once.
func render(templates map[string][]byte, p UnitParams) (map[string][]byte, error) {
	return nil, errNotImplemented
}

// DaemonReload is the command that makes systemd read changed units; the isolated installer runs it after WriteUnits.
var DaemonReload = []string{"systemctl", "--user", "daemon-reload"}

// WriteResult lists the unit files WriteUnits created or changed.
type WriteResult struct{ Changed []string }

// WriteUnits writes the units into dir (created when missing) and reports which files changed; it needs a daemon reload
// when any did.
func WriteUnits(dir string, units map[string][]byte) (WriteResult, error) {
	return WriteResult{}, errNotImplemented
}
