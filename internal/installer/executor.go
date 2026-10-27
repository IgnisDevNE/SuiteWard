package installer

import "path/filepath"

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
