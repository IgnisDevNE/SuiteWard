package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// resolveGitHub is the one entry the tier flows call (M1.12-I6):
//
//	func resolveGitHub(ctx context.Context, env githubEnv) (GitHubResult, error)
//
// It makes sure the instance has a GitHub App, an installation and the chosen repositories, and leaves the repositories
// protected or says why not. Order: the App (given, or created through the manifest flow), the installation, the
// repositories (1 to MaxRepositories, one owner), the test check per repository, the owner login, a ruleset JSON per
// repository, and a re-read of the protection. It prints to env.Out and asks through env.R.
//
// The private key is returned in GitHubResult.PrivateKey for the caller to put into the SecretStore. Nothing here writes it
// to disk, prints it or puts it in an error. An App given through answers needs "app-id", "installation-id" and the key:
// "app-key-file" (a path) or "app-key" (the answers file, a no-echo prompt, or stdin; when stdin carries the key, every
// other answer must come from flags or the answers file, because the key read consumes the rest of stdin).
func resolveGitHub(ctx context.Context, env githubEnv) (GitHubResult, error) {
	return GitHubResult{}, errors.New("not implemented")
}

// githubEnv is what resolveGitHub needs from the machine. The zero value of a duration, APIURL, WebURL, HTTP or Now takes
// the production default.
type githubEnv struct {
	R               *Resolver
	Out             io.Writer
	HTTP            *http.Client
	APIURL, WebURL  string // https://api.github.com and https://github.com
	Now             func() time.Time
	Tier            config.Tier
	SettingsDir     string // where the ruleset files go
	ManifestPort    int    // 0: any free port; --manifest-port for the isolated SSM port forward
	SkipRulesetWait bool
	ManifestWait    time.Duration // the manifest code, 15 minutes
	InstallWait     time.Duration // the installation, 10 minutes
	PollEvery       time.Duration // 5 seconds
}

// GitHubResult is what the install flow learned from GitHub.
type GitHubResult struct {
	AppID          int64
	Slug           string
	InstallationID int64
	Account        string // the account the App is installed on, and the owner of every repository
	AccountType    string // User or Organization
	OwnerLogin     string // the instance owner, for setup
	Repositories   []string
	TestChecks     map[string]string            // repository -> test check
	Protection     map[string]protection.Result // repository -> the result of the re-read
	PrivateKey     []byte                       // in memory only, for the SecretStore
}

// Format never renders the private key, whatever the verb.
func (r GitHubResult) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "GitHubResult{app %d, installation %d, %s, %v}", r.AppID, r.InstallationID, r.Account, r.Repositories)
}

// runSetup runs `suiteward setup` in the serving container with the real terminal; nothing is captured or logged.
func runSetup(ctx context.Context, rt Runtime, owner string, checks map[string]string, out io.Writer) error {
	return errors.New("not implemented")
}
