package installer

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"

	"github.com/IgnisDevNE/SuiteWard/deploy"
)

// The names of the executor's settings (runtime contract, "Execution (M2.1)"): every setting is an environment variable named
// SUITEWARD_<NAME>, and a secret can be given as a file with _FILE. They are in one place so that a change of a name is one edit.
const (
	executorURLEnv       = "SUITEWARD_EXECUTOR_URL"
	executorTokenFileEnv = "SUITEWARD_EXECUTOR_TOKEN_FILE"
	executorRuntimeEnv   = "SUITEWARD_EXECUTOR_RUNTIME"
)

// executorTokenFile is the copy of the executor token that the executor reads on the co-located tier: beside the copy of the MCP token, in the
// directory that only this user can read, because the executor runs as this user.
func executorTokenFile(tokenFile string) string {
	return filepath.Join(filepath.Dir(tokenFile), "executor-token")
}

// The files of the executor on the isolated host. The administrator's step (`install host`) owns them all: they are root's, or the
// executor account's, and the service account only reads what a doctor check needs.
const (
	ExecutorAccount  = "suiteward-exec"
	executorLibDir   = "/usr/local/libexec/suiteward"
	executorBinary   = executorLibDir + "/suiteward-executor"
	executorPin      = executorLibDir + "/executor.pin"
	executorUnitName = "suiteward-executor.service"
	executorUnitPath = "/etc/systemd/user/" + executorUnitName
	executorTokenEtc = "/etc/suiteward/executor-token"
	// executorTokenHandover is the file name of the copy that the service account owns, in its configuration directory.
	executorTokenHandover = "executor-token"
	executorDropIn        = "/etc/systemd/system/user@.service.d/delegate.conf"
	executorDropInText    = "[Service]\nDelegate=cpu cpuset io memory pids\n"
	executorRuleFile      = "/etc/suiteward/exec-block.nft"
	executorBlockUnit     = "suiteward-exec-block.service"
	executorTable         = "suiteward_exec"
	executorPinName       = "suiteward-executor" // what the pin calls the copy when no SHA256SUMS names it
)

// metadataAddress6 is the address that AWS Nitro hosts answer on over IPv6; metadataAddress (host.go) is the IPv4 one. The executor is refused
// both, by destination.
const metadataAddress6 = "fd00:ec2::254"

// executorUnitURLLine is the line of the executor's unit that names the service; RenderExecutorUnit changes its port.
func executorUnitURLLine(port int) string {
	return "Environment=" + executorURLEnv + "=http://127.0.0.1:" + strconv.Itoa(port)
}

// RenderExecutorUnit is the systemd user unit of the executor of an isolated host, for a service that publishes its HTTP listener on the loopback
// port httpPort. Everything else of the unit is as written in deploy/systemd.
func RenderExecutorUnit(httpPort int) ([]byte, error) {
	if httpPort < 1 || httpPort > 65535 {
		return nil, fmt.Errorf("http port %d is outside 1-65535", httpPort)
	}
	raw, err := fs.ReadFile(deploy.Systemd, "systemd/"+executorUnitName)
	if err != nil {
		return nil, fmt.Errorf("embedded unit %s: %w", executorUnitName, err)
	}
	text, err := replaceLine(string(raw), lineEdit{old: executorUnitURLLine(DefaultHTTPPort), new: executorUnitURLLine(httpPort)})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", executorUnitName, err)
	}
	return []byte(text), nil
}

// executorDirOf is the directory of the executor of the co-located tier: beside the copy of the MCP token.
func executorDirOf(tokenFile string) string {
	return filepath.Join(filepath.Dir(tokenFile), "executor")
}
