package installer

import (
	"fmt"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/deploy"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

var postgresImageLine = regexp.MustCompile(`(?m)^Image=(docker\.io/library/postgres:[^@\s]+@sha256:[0-9a-f]{64})$`)

// unitFile reads one embedded quadlet unit.
func unitFile(name string) ([]byte, error) {
	raw, err := fs.ReadFile(deploy.Quadlet, "quadlet/"+name)
	if err != nil {
		return nil, fmt.Errorf("embedded unit %s: %w", name, err)
	}
	return raw, nil
}

// PostgresImage is the digest-pinned database image of the embedded quadlet: the one copy of the pin the installer has.
func PostgresImage() (string, error) {
	raw, err := unitFile("suiteward-db.container")
	if err != nil {
		return "", err
	}
	m := postgresImageLine.FindSubmatch(raw)
	if m == nil {
		return "", fmt.Errorf("suiteward-db.container has no digest-pinned PostgreSQL Image= line")
	}
	return string(m[1]), nil
}

// Stack is the three containers of one instance, in start order.
type Stack struct{ DB, Migrate, Serve RunSpec }

// ColocatedStack is the co-located stack: the database, the one-shot migrate and the service, with the flag sets of
// the quadlets. image is the release image (pinned by digest for an install).
func ColocatedStack(s Settings, image string) (Stack, error) {
	if err := s.Validate(); err != nil {
		return Stack{}, err
	}
	if s.Tier != config.TierCoLocated {
		return Stack{}, fmt.Errorf("an installation made of containers is for the co-located type (on this computer), not %s", s.Tier)
	}
	if image == "" {
		return Stack{}, fmt.Errorf("the release image is required")
	}
	postgres, err := PostgresImage()
	if err != nil {
		return Stack{}, err
	}
	serve := RunSpec{
		Name: ContainerServe, Image: image, Network: NetworkName, Detach: true, Restart: "always", Memory: "256m", StopTimeout: 45,
		Env: []string{
			"SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url", "SUITEWARD_MCP_TOKEN_FILE=/run/secrets/mcp-token", executorAuthFileEnv + "=/run/secrets/executor-token",
			"SUITEWARD_INSTALLATION_TIER=" + string(s.Tier), reportBaseURLEnv + "=" + reportBaseURL(s.HTTPPort),
		},
		Secrets: []string{SecretDatabaseURL, SecretMCPAuth, SecretExecutorAuth},
		Volumes: []string{VolumeData + ":/data"},
		Publish: []string{
			"127.0.0.1:" + strconv.Itoa(s.HTTPPort) + ":8080",
			"127.0.0.1:" + strconv.Itoa(s.MCPPort) + ":8090",
		},
	}
	if g := s.GitHub; g.AppID != 0 {
		serve.Env = append(serve.Env,
			"SUITEWARD_GITHUB_APP_ID="+strconv.FormatInt(g.AppID, 10),
			"SUITEWARD_GITHUB_INSTALLATION_ID="+strconv.FormatInt(g.InstallationID, 10),
			"SUITEWARD_GITHUB_REPOSITORIES="+strings.Join(g.Repositories, ","),
			"SUITEWARD_GITHUB_APP_KEY_FILE=/run/secrets/github-app-key")
		serve.Secrets = append(serve.Secrets, SecretGitHubAppKey)
	}
	if s.JobTimeout != "" { // serve only: migrate does not read the job time limit
		serve.Env = append(serve.Env, "SUITEWARD_JOB_TIMEOUT="+s.JobTimeout)
	}
	return Stack{
		DB: RunSpec{
			Name: ContainerDB, Image: postgres, Network: NetworkName, Detach: true, Restart: "always", User: "999:999",
			Tmpfs: []string{"/run/postgresql", "/tmp"}, Memory: "384m", StopTimeout: 30,
			Env:     []string{"POSTGRES_USER=suiteward", "POSTGRES_DB=suiteward", "POSTGRES_PASSWORD_FILE=/run/secrets/postgres-password"},
			Secrets: []string{SecretDBAuth}, Volumes: []string{VolumeDBData + ":/var/lib/postgresql"},
			Health: &Health{Cmd: "pg_isready -U suiteward -d suiteward", Interval: "5s", Timeout: "3s", Retries: 12},
		},
		Migrate: RunSpec{
			Name: ContainerMigrate, Image: image, Network: NetworkName, Memory: "256m",
			Env:     []string{"SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url", "SUITEWARD_MIGRATION_DATABASE_URL_FILE=/run/secrets/database-owner-url"},
			Secrets: []string{SecretDatabaseURL, SecretDatabaseOwnerURL}, Args: []string{"migrate"},
		},
		Serve: serve,
	}, nil
}
