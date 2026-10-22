package installer

import (
	"context"
	"errors"
	"io"
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
	Remove(ctx context.Context, container string) error       // no error when it does not exist
	Pull(ctx context.Context, image string) error             // only an image pinned by digest
	Run(ctx context.Context, spec RunSpec) error
	Exec(ctx context.Context, container string, argv []string, stdin io.Reader) ([]byte, error)
	ExecTTY(ctx context.Context, container string, argv []string) error // the real terminal, nothing captured
	State(ctx context.Context, container string) (ContainerState, error)
	Secrets() SecretStore
}

// SecretStore keeps the stack's secrets where the runtime keeps them. A value never appears in an argument list.
type SecretStore interface {
	Exists(ctx context.Context, name string) (bool, error)
	Write(ctx context.Context, name string, value []byte, replace bool) error
	Read(ctx context.Context, name string) ([]byte, error)
	Remove(ctx context.Context, name string) error
}

// Names of the secrets of the catalog.
const (
	SecretDBPassword       = "suiteward-db-password"
	SecretDatabaseOwnerURL = "suiteward-database-owner-url"
	SecretDatabaseURL      = "suiteward-database-url"
	SecretGitHubAppKey     = "suiteward-github-app-key"
	SecretMCPToken         = "suiteward-mcp-token"
)

// NewPodman selects the rootless connection and refuses a rootful one.
func NewPodman(ctx context.Context, d Deps, connection string) (Runtime, error) {
	return nil, errNotImplemented
}

// DockerOptions choose the Docker CLI and context; zero values pick Docker Desktop's on Windows and macOS.
type DockerOptions struct{ Binary, Context string }

// NewDocker identifies the engine behind the CLI and refuses Podman's docker shim.
func NewDocker(ctx context.Context, d Deps, o DockerOptions) (Runtime, error) {
	return nil, errNotImplemented
}

// PostgresImage is the digest-pinned database image of the embedded quadlet.
func PostgresImage() (string, error) { return "", errNotImplemented }
