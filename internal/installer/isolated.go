package installer

import (
	"context"
	"errors"
	"io"
	"time"
)

// Health wait of the database, variables so tests do not sleep.
var (
	dbHealthTimeout  = 90 * time.Second
	dbHealthInterval = 2 * time.Second
)

// generatorPath is the Podman quadlet generator, whose dry run lists every unit it reads.
const generatorPath = "/usr/lib/systemd/system-generators/podman-system-generator"

func installIsolated(ctx context.Context, s Settings, r *Resolver, d Deps, out io.Writer) error {
	return errors.New("not implemented")
}
