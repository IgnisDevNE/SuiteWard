package installer

// command is how a hint names this program (Deps.Command).
func (d Deps) command() string { return "suiteward" }

// commandName is Deps.Command for the program that is running: executable is os.Executable and lookPath is exec.LookPath.
func commandName(goos string, executable func() (string, error), lookPath func(string) (string, error)) string {
	return "suiteward"
}
