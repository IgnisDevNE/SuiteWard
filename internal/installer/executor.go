package installer

// The names of the executor's settings (runtime contract, "Execution (M2.1)"): every setting is an environment variable named
// SUITEWARD_<NAME>, and a secret can be given as a file with _FILE. They are in one place so that a change of a name is one edit.
const (
	executorURLEnv       = "SUITEWARD_EXECUTOR_URL"
	executorTokenFileEnv = "SUITEWARD_EXECUTOR_TOKEN_FILE"
	executorRuntimeEnv   = "SUITEWARD_EXECUTOR_RUNTIME"
)
