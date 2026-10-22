package installer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// githubAliases are the flags and answers-file keys of the existing App route (the ones local.ps1 had), mapped to the
// keys the GitHub flow reads. Both spellings work in a flag and in the answers file.
var githubAliases = map[string]string{
	"github-app-id":          "app-id",
	"github-installation-id": "installation-id",
	"github-owner":           "owner",
	"github-repositories":    "repositories",
	"github-app-key-file":    "app-key-file",
}

// aliasGitHubKeys copies each alias to the key the GitHub flow reads, unless that key is set.
func aliasGitHubKeys(m map[string]string) {
	for old, key := range githubAliases {
		if v, ok := m[old]; ok {
			if _, set := m[key]; !set {
				m[key] = v
			}
		}
	}
}

// githubImplying are the answers that only make sense with a GitHub App: given without one, they are a mistake that
// local.ps1 refused too.
var githubImplying = []string{"installation-id", "repositories", "owner", "app-key-file", "app-key", "github-account", "manifest-code", "test-check", "ruleset-imported"}

func (e Env) githubFlow() func(context.Context, githubEnv) (GitHubResult, error) {
	if e.ResolveGitHub != nil {
		return e.ResolveGitHub
	}
	return resolveGitHub
}

// wantsGitHub decides whether the GitHub flow runs. It does when an App id is given, when GitHub is asked for (--github
// yes, or a yes at the prompt of a terminal), or when the saved settings have GitHub but the key secret is gone. A
// re-run with settings that are complete does not ask GitHub again. GitHub values without an App are an error.
func (c *colocatedInstall) wantsGitHub(ctx context.Context, r *Resolver) (bool, error) {
	if id, ok := r.given("app-id"); ok {
		if strings.EqualFold(strings.TrimSpace(id), "none") {
			c.s.GitHub = GitHub{}
			return false, nil
		}
		return true, nil
	}
	existing := c.s.GitHub.AppID != 0
	if v, ok := r.given("github"); ok {
		yes, err := parseYesNo("github", v)
		return yes, err
	}
	for _, key := range githubImplying {
		if _, ok := r.given(key); ok && !existing {
			return false, fmt.Errorf("GitHub values were given without a GitHub App (%s): pass --github-app-id with --github-installation-id and the App key for an App that exists, or --github yes to create one, or --github-app-id none", key)
		}
	}
	for _, key := range githubImplying {
		if _, ok := r.given(key); ok {
			return true, nil // values for the GitHub App of the saved settings
		}
	}
	if existing {
		have, err := c.rt.Secrets().Exists(ctx, SecretGitHubAppKey)
		return !have, err
	}
	if !r.term.IsTerminal {
		return false, nil
	}
	return r.confirm("github", "Set up GitHub now (creates a GitHub App in your browser)?")
}

// githubFlow runs the GitHub flow when it is wanted and applies its result: the settings, the App key and the owner login
// that setup enrolls.
func (c *colocatedInstall) githubFlow(ctx context.Context, r *Resolver, settingsDir string) error {
	run, err := c.wantsGitHub(ctx, r)
	if err != nil || !run {
		return err
	}
	port := 0
	if v, ok := r.given("manifest-port"); ok {
		if port, err = strconv.Atoi(strings.TrimSpace(v)); err != nil {
			return fmt.Errorf("manifest-port %q is not a number", v)
		}
	}
	secrets := c.rt.Secrets()
	stored := false
	existing := c.s.GitHub
	res, err := c.env.githubFlow()(ctx, githubEnv{
		R: r, Out: c.out, Tier: c.s.Tier, SettingsDir: settingsDir, ManifestPort: port, Existing: &existing,
		KeyExists: func(ctx context.Context) (bool, error) { return secrets.Exists(ctx, SecretGitHubAppKey) },
		ReadKey:   func(ctx context.Context) ([]byte, error) { return secrets.Read(ctx, SecretGitHubAppKey) },
		StoreKey: func(ctx context.Context, key []byte) error {
			if err := secrets.Write(ctx, SecretGitHubAppKey, key, false); err != nil {
				return err
			}
			stored = true
			return nil
		},
	})
	if err != nil {
		return err
	}
	if res.AppID == 0 || res.InstallationID == 0 || len(res.Repositories) == 0 {
		return errors.New("the GitHub flow returned no App, installation or repositories")
	}
	c.s.GitHub = GitHub{
		AppID: res.AppID, InstallationID: res.InstallationID, Owner: res.Account, Repositories: res.Repositories, TestChecks: res.TestChecks,
	}
	if err := c.s.Validate(); err != nil {
		return err
	}
	c.setupOwner, c.setupChecks = res.OwnerLogin, res.TestChecks
	if !stored && len(res.PrivateKey) > 0 { // an App that exists: its key came from a file or the answers, and is stored now
		have, err := secrets.Exists(ctx, SecretGitHubAppKey)
		if err != nil {
			return err
		}
		if !have {
			c.githubKey = res.PrivateKey
		}
	}
	return nil
}
