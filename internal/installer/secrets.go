package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrSecretExists means a secret is already stored and Write was not asked to replace it.
var ErrSecretExists = errors.New("the secret already exists")

type consumer struct {
	container string
	uid       int
}

// secretSpec is one secret of the stack: its Podman name, the file it becomes under /run/secrets, and the containers
// that read it with the uid each reads it as. Only these containers get it, so serve never sees the owner URL.
type secretSpec struct {
	name, file string
	consumers  []consumer
}

const (
	uidService  = 65532 // the distroless nonroot user of the SuiteWard image
	uidPostgres = 999   // the postgres user of the database image
)

var secretCatalog = []secretSpec{
	{SecretDBAuth, "postgres-password", []consumer{{ContainerDB, uidPostgres}}},
	{SecretDatabaseOwnerURL, "database-owner-url", []consumer{{ContainerMigrate, uidService}}},
	{SecretDatabaseURL, "database-url", []consumer{{ContainerMigrate, uidService}, {ContainerServe, uidService}}},
	{SecretGitHubAppKey, "github-app-key", []consumer{{ContainerServe, uidService}}},
	{SecretMCPAuth, "mcp-token", []consumer{{ContainerServe, uidService}}},
}

func lookupSecret(name string) (secretSpec, error) {
	for _, s := range secretCatalog {
		if s.name == name {
			return s, nil
		}
	}
	return secretSpec{}, fmt.Errorf("%q is not a secret of the stack", name)
}

// secretFor finds a secret and the uid container reads it as; a container that is not one of its consumers is refused.
func secretFor(name, container string) (secretSpec, int, error) {
	s, err := lookupSecret(name)
	if err != nil {
		return secretSpec{}, 0, err
	}
	for _, c := range s.consumers {
		if c.container == container {
			return s, c.uid, nil
		}
	}
	return secretSpec{}, 0, fmt.Errorf("the secret %s is not for the container %s", name, container)
}

type podmanSecrets struct{ p *Podman }

func (s podmanSecrets) Exists(ctx context.Context, name string) (bool, error) {
	if _, err := lookupSecret(name); err != nil {
		return false, err
	}
	return s.p.Exists(ctx, kindSecret, name)
}

// Write passes the value on stdin exactly as it is (no trailing newline is added), never as an argument.
func (s podmanSecrets) Write(ctx context.Context, name string, value []byte, replace bool) error {
	if _, err := lookupSecret(name); err != nil {
		return err
	}
	if !replace {
		if ok, err := s.Exists(ctx, name); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%w: %s", ErrSecretExists, name)
		}
	}
	args := []string{"secret", "create"}
	if replace {
		args = append(args, "--replace")
	}
	if _, err := s.p.run(ctx, bytes.NewReader(value), append(args, name, "-")...); err != nil {
		return fmt.Errorf("create the secret %s: %w", name, err)
	}
	return nil
}

func (s podmanSecrets) Read(ctx context.Context, name string) ([]byte, error) {
	if _, err := lookupSecret(name); err != nil {
		return nil, err
	}
	out, err := s.p.run(ctx, nil, "secret", "inspect", "--showsecret", "--format", "{{.SecretData}}", name)
	if err != nil {
		return nil, readFailed(name) // the engine's message may quote the value
	}
	return bytes.TrimSuffix(bytes.TrimSuffix(out, []byte("\n")), []byte("\r")), nil
}

func (s podmanSecrets) Remove(ctx context.Context, name string) error {
	if _, err := lookupSecret(name); err != nil {
		return err
	}
	if _, err := s.p.run(ctx, nil, "secret", "rm", "--ignore", name); err != nil {
		return fmt.Errorf("remove the secret %s: %w", name, err)
	}
	return nil
}

// secretVolume is the named volume that holds the secrets of one consumer container.
func secretVolume(container string) string { return "suiteward-secrets-" + container }

// dockerSecrets keeps each secret as a 0400 file in one named volume per consumer container (spike S2): Docker has no
// secrets outside Swarm and a file bind-mounted from Windows shows up as mode 777 inside Docker's VM. Values go in through
// the stdin of a one-off container that runs the database image's shell, so they never touch the host disk.
type dockerSecrets struct{ d *Docker }

// volumeRun runs script in a one-off container with volume at /s, optionally read-only, and returns its output.
func (s dockerSecrets) volumeRun(ctx context.Context, volume string, readOnly bool, stdin io.Reader, script string) ([]byte, error) {
	image, err := PostgresImage()
	if err != nil {
		return nil, err
	}
	mount := volume + ":/s"
	if readOnly {
		mount += ":ro"
	}
	// No network: the one-off container holds a secret in memory and has no reason to reach anything.
	args := []string{"run", "--rm", "-i", "--network", "none", "--user", "0", "--entrypoint", "sh", "--volume", mount, image, "-c", script}
	return s.d.run(ctx, stdin, args...)
}

func (s dockerSecrets) Exists(ctx context.Context, name string) (bool, error) {
	spec, err := lookupSecret(name)
	if err != nil {
		return false, err
	}
	for _, c := range spec.consumers {
		volume := secretVolume(c.container)
		if ok, err := s.d.Exists(ctx, KindVolume, volume); err != nil || !ok {
			return false, err
		}
		out, err := s.volumeRun(ctx, volume, true, nil, fmt.Sprintf("test -f /s/%s && echo yes || echo no", spec.file))
		if err != nil {
			return false, fmt.Errorf("look for the secret %s: %w", name, err)
		}
		if strings.TrimSpace(string(out)) != "yes" {
			return false, nil
		}
	}
	return true, nil
}

func (s dockerSecrets) Write(ctx context.Context, name string, value []byte, replace bool) error {
	spec, err := lookupSecret(name)
	if err != nil {
		return err
	}
	if !replace {
		if ok, err := s.Exists(ctx, name); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%w: %s", ErrSecretExists, name)
		}
	}
	for _, c := range spec.consumers {
		script := fmt.Sprintf("umask 077; cat > /s/%[1]s && chown %[2]d:%[2]d /s/%[1]s && chmod 0400 /s/%[1]s", spec.file, c.uid)
		if _, err := s.volumeRun(ctx, secretVolume(c.container), false, bytes.NewReader(value), script); err != nil {
			return fmt.Errorf("write the secret %s for %s: %w", name, c.container, err)
		}
	}
	return nil
}

func (s dockerSecrets) Read(ctx context.Context, name string) ([]byte, error) {
	spec, err := lookupSecret(name)
	if err != nil {
		return nil, err
	}
	out, err := s.volumeRun(ctx, secretVolume(spec.consumers[0].container), true, nil, "cat /s/"+spec.file)
	if err != nil {
		return nil, readFailed(name)
	}
	return out, nil
}

// readFailed is the error of a failed secret read: no cause, because whatever the command said may include the value.
func readFailed(name string) error {
	return fmt.Errorf("read the secret %s failed (the engine's message is not shown: it may quote the value)", name)
}

// Consistent says whether every consumer's volume holds the same value of a secret that more than one container reads. A
// write that was interrupted between two volumes leaves them different.
func (s dockerSecrets) Consistent(ctx context.Context, name string) (bool, error) {
	spec, err := lookupSecret(name)
	if err != nil {
		return false, err
	}
	var first []byte
	for i, c := range spec.consumers {
		out, err := s.volumeRun(ctx, secretVolume(c.container), true, nil, "cat /s/"+spec.file)
		if err != nil {
			return false, readFailed(name)
		}
		if i == 0 {
			first = out
		} else if !bytes.Equal(out, first) {
			return false, nil
		}
	}
	return true, nil
}

// PurgeSecrets deletes the secret volumes of the three containers; the containers are gone by then.
func (dk *Docker) PurgeSecrets(ctx context.Context) error {
	for _, container := range []string{ContainerServe, ContainerMigrate, ContainerDB} {
		if err := dk.Delete(ctx, KindVolume, secretVolume(container)); err != nil {
			return err
		}
	}
	return nil
}

// PurgeSecrets removes every secret of the catalog.
func (p *Podman) PurgeSecrets(ctx context.Context) error {
	for _, s := range secretCatalog {
		if err := p.secrets.Remove(ctx, s.name); err != nil {
			return err
		}
	}
	return nil
}

func (s dockerSecrets) Remove(ctx context.Context, name string) error {
	spec, err := lookupSecret(name)
	if err != nil {
		return err
	}
	for _, c := range spec.consumers {
		volume := secretVolume(c.container)
		if ok, err := s.d.Exists(ctx, KindVolume, volume); err != nil {
			return err
		} else if !ok {
			continue
		}
		if _, err := s.volumeRun(ctx, volume, false, nil, "rm -f /s/"+spec.file); err != nil {
			return fmt.Errorf("remove the secret %s for %s: %w", name, c.container, err)
		}
	}
	return nil
}
