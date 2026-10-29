package installer

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
)

const (
	secretValue = "s3cr3t-value-0123456789abcdef"
	pemValue    = "-----BEGIN PRIVATE KEY-----\nMIIEvQ\n-----END PRIVATE KEY-----\n"
)

// errPodman49Replace is what Podman 4.9 (the one of Ubuntu 24.04 LTS) answers to `secret create --replace` for a secret that does
// not exist yet. Podman 5 creates it; plain `secret create`, and `--replace` for an existing secret, work on both.
var errPodman49Replace = errors.New("exit status 125: Error: deleting secret : : no secret data with ID")

func TestPodmanSecretsAreWrittenThroughStdinWithoutANewline(t *testing.T) {
	ctx := context.Background()
	f := newFake("linux")
	secrets := newRootlessPodman(t, f, "").Secrets()
	f.on("secret ls", SecretGitHubAppKey+"\n") // the key is there, so writing it replaces it

	if err := secrets.Write(ctx, SecretMCPAuth, []byte(secretValue), false); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Write(ctx, SecretGitHubAppKey, []byte(pemValue), true); err != nil {
		t.Fatal(err)
	}
	creates := f.find("secret create")
	if len(creates) != 2 {
		t.Fatalf("calls: %v", f.lines())
	}
	if creates[0].line() != "secret create suiteward-mcp-token -" || creates[0].stdin != secretValue {
		t.Fatalf("unexpected first create: %+v", creates[0])
	}
	if creates[1].line() != "secret create --replace suiteward-github-app-key -" || creates[1].stdin != pemValue {
		t.Fatalf("a replacement must pass --replace and the value untouched: %+v", creates[1])
	}
	f.assertNoSecret(t, secretValue, "MIIEvQ")
}

func TestPodmanSecretLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newFake("linux")
	secrets := newRootlessPodman(t, f, "").Secrets()

	f.on("secret ls", SecretMCPAuth+"\n"+SecretDatabaseURL+"-old\n")
	if ok, err := secrets.Exists(ctx, SecretMCPAuth); err != nil || !ok {
		t.Fatalf("exists: %v %v", ok, err)
	}
	if ok, err := secrets.Exists(ctx, SecretDatabaseURL); err != nil || ok {
		t.Fatalf("a longer name was taken for the secret: %v %v", ok, err)
	}

	f.on("secret inspect", secretValue+"\n")
	got, err := secrets.Read(ctx, SecretMCPAuth)
	if err != nil || string(got) != secretValue {
		t.Fatalf("read: %q %v", got, err)
	}
	if len(f.find("secret inspect --showsecret --format {{.SecretData}} suiteward-mcp-token")) != 1 {
		t.Fatalf("calls: %v", f.lines())
	}

	if err := secrets.Remove(ctx, SecretMCPAuth); err != nil {
		t.Fatal(err)
	}
	if len(f.find("secret rm --ignore suiteward-mcp-token")) != 1 {
		t.Fatalf("calls: %v", f.lines())
	}
}

func TestSecretsRefuseANameOutsideTheCatalog(t *testing.T) {
	ctx := context.Background()
	for name, mk := range map[string]func(*fakeEngine) SecretStore{
		"podman": func(f *fakeEngine) SecretStore { return newRootlessPodman(t, f, "").Secrets() },
		"docker": func(f *fakeEngine) SecretStore {
			return newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{}).Secrets()
		},
	} {
		f := newFake("linux")
		secrets := mk(f)
		before := len(f.calls)
		if err := secrets.Write(ctx, "../etc; rm -rf /", []byte("x"), false); err == nil {
			t.Errorf("%s: wrote an uncatalogued secret", name)
		}
		if _, err := secrets.Read(ctx, "nope"); err == nil {
			t.Errorf("%s: read an uncatalogued secret", name)
		}
		if len(f.calls) != before {
			t.Errorf("%s: a command ran for an uncatalogued secret", name)
		}
	}
}

func writerRuns(f *fakeEngine) []call { return f.find("--entrypoint sh") }

func TestDockerSecretsGoToOneVolumePerConsumer(t *testing.T) {
	ctx := context.Background()
	f := newFake("linux")
	secrets := newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{}).Secrets()
	postgres, err := PostgresImage()
	if err != nil {
		t.Fatal(err)
	}

	// The application URL is read by migrate and by serve: a copy in each consumer's own volume.
	if err := secrets.Write(ctx, SecretDatabaseURL, []byte(secretValue), true); err != nil {
		t.Fatal(err)
	}
	runs := writerRuns(f)
	if len(runs) != 2 {
		t.Fatalf("want a write per consumer, got %v", f.lines())
	}
	wantVolumes := []string{"suiteward-secrets-" + ContainerMigrate, "suiteward-secrets-" + ContainerServe}
	for i, run := range runs {
		line := run.line()
		for _, want := range []string{
			"run --rm -i --network none --user 0 --entrypoint sh --volume " + wantVolumes[i] + ":/s " + postgres + " -c ",
			"cat > /s/database-url", "chown 65532:65532 /s/database-url", "chmod 0400 /s/database-url", "umask 077",
		} {
			if !strings.Contains(line, want) {
				t.Errorf("write %d lacks %q:\n%s", i, want, line)
			}
		}
		if run.stdin != secretValue {
			t.Errorf("write %d did not pass the value through stdin: %q", i, run.stdin)
		}
	}

	// The owner URL is migrate's alone and the database password belongs to the database user.
	f.calls = nil
	if err := secrets.Write(ctx, SecretDatabaseOwnerURL, []byte(secretValue), true); err != nil {
		t.Fatal(err)
	}
	if runs := writerRuns(f); len(runs) != 1 || !strings.Contains(runs[0].line(), "suiteward-secrets-"+ContainerMigrate+":/s") ||
		strings.Contains(runs[0].line(), "suiteward-secrets-"+ContainerServe+":/s") {
		t.Fatalf("the owner URL must reach the migrate volume only: %v", f.lines())
	}
	f.calls = nil
	if err := secrets.Write(ctx, SecretDBAuth, []byte(secretValue), true); err != nil {
		t.Fatal(err)
	}
	if runs := writerRuns(f); len(runs) != 1 || !strings.Contains(runs[0].line(), "chown 999:999 /s/postgres-password") ||
		!strings.Contains(runs[0].line(), "suiteward-secrets-"+ContainerDB+":/s") {
		t.Fatalf("the database password must be 999-owned in the database volume: %v", f.lines())
	}
	f.assertNoSecret(t, secretValue)
}

func TestDockerWriteWithoutReplaceKeepsAnExistingSecret(t *testing.T) {
	ctx := context.Background()
	f := newFake("linux")
	secrets := newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{}).Secrets()
	f.on("volume ls", "suiteward-secrets-"+ContainerServe+"\n").on("test -f", "yes\n")
	if err := secrets.Write(ctx, SecretMCPAuth, []byte(secretValue), false); !errors.Is(err, ErrSecretExists) {
		t.Fatalf("got %v, want ErrSecretExists", err)
	}
	if len(f.find("cat >")) != 0 {
		t.Fatal("the secret was written")
	}
}

func TestDockerSecretExistsOnlyWhenEveryConsumerHasIt(t *testing.T) {
	ctx := context.Background()
	volumes := "suiteward-secrets-" + ContainerMigrate + "\nsuiteward-secrets-" + ContainerServe + "\n"

	f := newFake("linux")
	secrets := newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{}).Secrets()
	if ok, err := secrets.Exists(ctx, SecretDatabaseURL); err != nil || ok {
		t.Fatalf("without volumes: %v %v", ok, err)
	}
	if len(writerRuns(f)) != 0 {
		t.Fatal("a container ran although no volume exists")
	}

	f = newFake("linux")
	secrets = newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{}).Secrets()
	f.on("volume ls", volumes).on("test -f", "yes\n")
	if ok, err := secrets.Exists(ctx, SecretDatabaseURL); err != nil || !ok {
		t.Fatalf("both copies exist: %v %v", ok, err)
	}

	f = newFake("linux")
	secrets = newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{}).Secrets()
	f.on("volume ls", volumes).on("suiteward-secrets-"+ContainerServe+":/s", "no\n").on("test -f", "yes\n")
	if ok, err := secrets.Exists(ctx, SecretDatabaseURL); err != nil || ok {
		t.Fatalf("one copy is missing: %v %v", ok, err)
	}
}

func TestDockerSecretReadAndRemove(t *testing.T) {
	ctx := context.Background()
	f := newFake("linux")
	secrets := newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{}).Secrets()

	f.on("cat /s/mcp-token", secretValue)
	got, err := secrets.Read(ctx, SecretMCPAuth)
	if err != nil || string(got) != secretValue {
		t.Fatalf("read: %q %v", got, err)
	}
	last := writerRuns(f)[0]
	if !strings.Contains(last.line(), "--volume suiteward-secrets-"+ContainerServe+":/s:ro ") {
		t.Fatalf("a read must mount the volume read-only: %s", last.line())
	}

	f.calls = nil
	f.on("volume ls", "suiteward-secrets-"+ContainerMigrate+"\nsuiteward-secrets-"+ContainerServe+"\n")
	if err := secrets.Remove(ctx, SecretDatabaseURL); err != nil {
		t.Fatal(err)
	}
	if got := f.find("rm -f /s/database-url"); len(got) != 2 {
		t.Fatalf("want the file removed from both consumers: %v", f.lines())
	}
}

func TestNoSecretValueEverReachesAnArgumentOrAnError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("engine unavailable")
	for name, mk := range map[string]func(*fakeEngine) Runtime{
		"podman": func(f *fakeEngine) Runtime { return newRootlessPodman(t, f, "") },
		"docker": func(f *fakeEngine) Runtime { return newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{}) },
	} {
		f := newFake("linux")
		rt := mk(f)
		f.fail("secret create", boom).fail("--entrypoint sh", boom)
		var errs []error
		for _, s := range []string{SecretDBAuth, SecretDatabaseOwnerURL, SecretDatabaseURL, SecretGitHubAppKey, SecretMCPAuth} {
			errs = append(errs, rt.Secrets().Write(ctx, s, []byte(secretValue+pemValue), true))
		}
		for _, err := range errs {
			if err == nil {
				t.Errorf("%s: a failing engine did not fail the write", name)
			} else if strings.Contains(err.Error(), secretValue) {
				t.Errorf("%s: the error repeats the secret: %v", name, err)
			}
		}
		f.rules = nil
		_ = rt.Run(ctx, servingSpec())
		_ = rt.ExecTTY(ctx, ContainerServe, []string{"/suiteward", "setup"})
		f.assertNoSecret(t, secretValue, "MIIEvQ")
	}
}

func TestPostgresImageComesFromTheEmbeddedDatabaseUnit(t *testing.T) {
	image, err := PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^docker\.io/library/postgres:[^@]+@sha256:[0-9a-f]{64}$`).MatchString(image) {
		t.Fatalf("got %q", image)
	}
}

func TestPodmanWriteWithoutReplaceKeepsAnExistingSecret(t *testing.T) {
	ctx := context.Background()
	f := newFake("linux")
	secrets := newRootlessPodman(t, f, "").Secrets()
	f.on("secret ls", SecretMCPAuth+"\n")
	if err := secrets.Write(ctx, SecretMCPAuth, []byte(secretValue), false); !errors.Is(err, ErrSecretExists) {
		t.Fatalf("got %v, want ErrSecretExists", err)
	}
	if len(f.find("secret create")) != 0 {
		t.Fatal("podman secret create ran for an existing secret")
	}
	if err := secrets.Write(ctx, SecretMCPAuth, []byte(secretValue), true); err != nil {
		t.Fatal(err)
	}
	if len(f.find("secret create --replace")) != 1 {
		t.Fatalf("calls: %v", f.lines())
	}
}

// Podman 4.9 refuses --replace for a secret that is not there yet, so a write that may replace creates a missing secret without it.
func TestPodmanReplacingAMissingSecretCreatesItWithoutReplace(t *testing.T) {
	ctx := context.Background()
	f := newFake("linux")
	secrets := newRootlessPodman(t, f, "").Secrets()
	f.fail("secret create --replace", errPodman49Replace)

	if err := secrets.Write(ctx, SecretDatabaseURL, []byte(secretValue), true); err != nil {
		t.Fatalf("a missing secret was not created: %v", err)
	}
	creates := f.find("secret create")
	if len(creates) != 1 || creates[0].line() != "secret create suiteward-database-url -" || creates[0].stdin != secretValue {
		t.Fatalf("want one plain create with the value on stdin: %v", f.lines())
	}
	f.assertNoSecret(t, secretValue)
}
