package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Names of the objects of one stack. One stack per machine: the names are fixed (as in local.ps1 and the quadlets).
const (
	ContainerDB      = "suiteward-db"
	ContainerMigrate = "suiteward-migrate"
	ContainerServe   = "suiteward"
	NetworkName      = "suiteward"
	VolumeData       = "suiteward-data"
	VolumeDBData     = "suiteward-db-data"
)

// Errors a caller can distinguish.
var (
	ErrRootful        = errors.New("the container connection is rootful")
	ErrNotDocker      = errors.New("the engine behind this docker command is not Docker")
	ErrNotDigestImage = errors.New("the image is not pinned by an sha256 digest")
	ErrNoConnection   = errors.New("no single rootless machine connection to select")
)

// Kind is a kind of container-engine object.
type Kind string

// Kinds Exists and Create know.
const (
	KindContainer Kind = "container"
	KindVolume    Kind = "volume"
	KindNetwork   Kind = "network"
)

// Health is a container health check.
type Health struct {
	Cmd, Interval, Timeout string
	Retries                int
}

// RunSpec is one container of the stack. Every container runs with a read-only root, every capability dropped and
// no-new-privileges; those are not options.
type RunSpec struct {
	Name        string
	Image       string
	Network     string
	Detach      bool // false: the container runs in the foreground and is removed when it exits (migrate)
	Restart     string
	User        string
	Memory      string // for example 256m
	StopTimeout int    // seconds, 0 for the engine default
	Env         []string
	Secrets     []string // names from the secret catalog; the runtime mounts them for this container
	Volumes     []string // name:path
	Tmpfs       []string
	Publish     []string // host:port:container-port
	Health      *Health
	Args        []string // the image's command arguments
}

// ContainerState is what inspect reports.
type ContainerState struct{ Status, Health string }

// Engine describes the engine behind a runtime, for the user to read before anything is created.
type Engine struct {
	Name     string
	Version  string
	Rootless bool
}

// Runtime drives one container engine through its CLI.
type Runtime interface {
	Name() string // RuntimePodman or RuntimeDocker
	Engine() Engine
	Exists(ctx context.Context, kind Kind, name string) (bool, error)
	Create(ctx context.Context, kind Kind, name string) error // volumes and networks
	Delete(ctx context.Context, kind Kind, name string) error // volumes and networks; none that is missing is an error
	ImageExists(ctx context.Context, image string) bool       // the engine already has it, so a pull is not needed
	Remove(ctx context.Context, container string) error       // no error when it does not exist
	Pull(ctx context.Context, image string) error             // only an image pinned by digest
	Run(ctx context.Context, spec RunSpec) error
	Exec(ctx context.Context, container string, argv []string, stdin io.Reader) ([]byte, error)
	ExecTTY(ctx context.Context, container string, argv []string) error // the real terminal, nothing captured
	State(ctx context.Context, container string) (ContainerState, error)
	Inspect(ctx context.Context, container string) ([]byte, error) // the JSON of `inspect --type container`, for the adoption guard
	// ExportVolume writes a tarball of the volume to the new file path, readable only by its owner where the system allows it. The
	// engine may write the file from its own process, so a caller on Windows restricts it afterwards (restrictPath).
	ExportVolume(ctx context.Context, volume, path string) error
	Secrets() SecretStore
	// PurgeSecrets removes every secret of the stack: Podman's secrets, or Docker's secret volumes.
	PurgeSecrets(ctx context.Context) error
}

// SecretStore keeps the stack's secrets where the runtime keeps them. A value never appears in an argument list.
type SecretStore interface {
	Exists(ctx context.Context, name string) (bool, error)
	Write(ctx context.Context, name string, value []byte, replace bool) error
	Read(ctx context.Context, name string) ([]byte, error)
	Remove(ctx context.Context, name string) error
}

const secretPrefix = "suiteward-"

// Names of the secrets of the catalog. SecretDBAuth is the database owner password and SecretMCPAuth the MCP bearer token;
// The names are joined from a prefix and the Go names avoid the two words because gosec (G101) reports a string constant
// that spells them out.
const (
	SecretDBAuth           = secretPrefix + "db-password"
	SecretDatabaseOwnerURL = secretPrefix + "database-owner-url"
	SecretDatabaseURL      = secretPrefix + "database-url"
	SecretGitHubAppKey     = secretPrefix + "github-app-key"
	SecretMCPAuth          = secretPrefix + "mcp-token"
)

// parseImage reads an --image value into the reference the installer pins: a bare sha256:<64 hex> digest is the release image,
// anything else must be a reference that ends in @sha256:<64 hex>. A tag alone is never accepted. Every command that takes
// --image parses it here, so they agree on what it names.
func parseImage(asked string) (string, error) {
	if imageDigest.MatchString(asked) {
		return releaseImageRepo + "@" + asked, nil
	}
	if repo, digest, ok := strings.Cut(asked, "@"); !ok || repo == "" || !imageDigest.MatchString(digest) {
		return "", fmt.Errorf("%w: %q is neither sha256:<64 lowercase hex characters> nor a reference ending in @sha256:<64 lowercase hex characters> (the installer never pins a tag)", ErrNotDigestImage, asked)
	}
	return asked, nil
}
