package installer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func linuxDocker(t *testing.T) (Runtime, *fakeEngine) {
	t.Helper()
	f := newFake("linux")
	return newDocker(t, f, dockerDesktopServer, `["name=seccomp,profile=builtin"]`, DockerOptions{}), f
}

func linuxPodman(t *testing.T) (Runtime, *fakeEngine) {
	t.Helper()
	f := newFake("linux")
	return newRootlessPodman(t, f, ""), f
}

// ---- the engine is identified before anything is created ----

func TestNewPodmanRefusesWhatItCannotUse(t *testing.T) {
	ctx := context.Background()
	t.Run("deps without an Exec", func(t *testing.T) {
		if _, err := NewPodman(ctx, Deps{GOOS: "linux", Getenv: func(string) string { return "" }}, ""); err == nil {
			t.Fatal("a runtime was made without a way to run podman")
		}
	})
	t.Run("the connection does not answer", func(t *testing.T) {
		f := newFake("linux").fail("info --format", errInjected)
		_, err := NewPodman(ctx, f.deps(), "pm")
		if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "the Podman connection is unavailable") {
			t.Fatalf("NewPodman = %v", err)
		}
	})
	t.Run("the machine connections cannot be listed", func(t *testing.T) {
		f := newFake("windows").fail("system connection list", errInjected)
		_, err := NewPodman(ctx, f.deps(), "")
		if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "list the Podman connections") {
			t.Fatalf("NewPodman = %v", err)
		}
	})
	t.Run("the machine connections are not JSON", func(t *testing.T) {
		f := newFake("windows").on("system connection list", "not json")
		if _, err := NewPodman(ctx, f.deps(), ""); err == nil || !strings.Contains(err.Error(), "read the Podman connections") {
			t.Fatalf("NewPodman = %v", err)
		}
	})
	t.Run("no rootless machine connection", func(t *testing.T) {
		f := newFake("windows").on("system connection list", `[{"Name":"root","URI":"ssh://root@127.0.0.1:1/run/podman/podman.sock","IsMachine":true}]`)
		if _, err := NewPodman(ctx, f.deps(), ""); !errors.Is(err, ErrNoConnection) {
			t.Fatalf("NewPodman = %v, want ErrNoConnection", err)
		}
	})
}

func TestNewDockerRefusesWhatItCannotIdentify(t *testing.T) {
	ctx := context.Background()
	if _, err := NewDocker(ctx, Deps{GOOS: "linux", Getenv: func(string) string { return "" }}, DockerOptions{}); err == nil {
		t.Fatal("a runtime was made without a way to run docker")
	}
	cases := []struct {
		name  string
		rules func(f *fakeEngine)
		want  string
	}{
		{"the engine does not answer", func(f *fakeEngine) { f.fail("version --format", errInjected) }, "the container program (Docker) does not answer"},
		{"the version is not JSON", func(f *fakeEngine) { f.on("version --format", "not json") }, "read the Docker server version"},
		{"no server is reported", func(f *fakeEngine) { f.on("version --format", "{}") }, "reported no server"},
		{"the engine is not Docker", func(f *fakeEngine) { f.on("version --format", `{"Platform":{"Name":"Podman Engine"},"Version":"5"}`) }, ErrNotDocker.Error()},
		{"the security options cannot be read", func(f *fakeEngine) {
			f.on("version --format", dockerDesktopServer).fail("info --format", errInjected)
		}, "read the Docker security options"},
		{"the security options are not JSON", func(f *fakeEngine) {
			f.on("version --format", dockerDesktopServer).on("info --format", "nope")
		}, "read the Docker security options"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake("linux")
			tc.rules(f)
			rt, err := NewDocker(ctx, f.deps(), DockerOptions{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewDocker = %v, %v; want an error containing %q", rt, err, tc.want)
			}
		})
	}
}

// ---- objects of the engine ----

func TestRuntimesRefuseAKindTheyDoNotHandle(t *testing.T) {
	ctx := context.Background()
	for name, make := range map[string]func(*testing.T) (Runtime, *fakeEngine){"podman": linuxPodman, "docker": linuxDocker} {
		t.Run(name, func(t *testing.T) {
			rt, f := make(t)
			if _, err := rt.Exists(ctx, Kind("image"), "x"); err == nil || !strings.Contains(err.Error(), "cannot look for a image") {
				t.Errorf("Exists = %v", err)
			}
			if err := rt.Create(ctx, KindContainer, "x"); err == nil || !strings.Contains(err.Error(), "cannot create a container") {
				t.Errorf("Create = %v", err)
			}
			if err := rt.Delete(ctx, KindContainer, "x"); err == nil || !strings.Contains(err.Error(), "cannot delete a container") {
				t.Errorf("Delete = %v", err)
			}
			if err := rt.Pull(ctx, "docker.io/library/postgres:18"); !errors.Is(err, ErrNotDigestImage) {
				t.Errorf("Pull of a tag = %v", err)
			}
			if len(f.find("pull")) != 0 {
				t.Errorf("a tag was pulled: %v", f.lines())
			}
		})
	}
}

func TestRuntimesReportAFailingObjectCommandWithItsName(t *testing.T) {
	ctx := context.Background()
	for name, make := range map[string]func(*testing.T) (Runtime, *fakeEngine){"podman": linuxPodman, "docker": linuxDocker} {
		t.Run(name, func(t *testing.T) {
			rt, f := make(t)
			f.fail("network create", errInjected).fail("network rm", errInjected).fail("pull ", errInjected).fail("--filter name=gone", errInjected)
			f.on("network ls --filter name=net", "net\n")
			if err := rt.Create(ctx, KindNetwork, "net"); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "create the network net") {
				t.Errorf("Create = %v", err)
			}
			if err := rt.Delete(ctx, KindNetwork, "net"); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "remove the network net") {
				t.Errorf("Delete = %v", err)
			}
			if _, err := rt.Exists(ctx, KindContainer, "gone"); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "look for the container gone") {
				t.Errorf("Exists = %v", err)
			}
			if err := rt.Delete(ctx, KindVolume, "gone"); !errors.Is(err, errInjected) {
				t.Errorf("Delete of a volume that cannot be looked for = %v", err)
			}
			if err := rt.Pull(ctx, testImage); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "pull "+testImage) {
				t.Errorf("Pull = %v", err)
			}
		})
	}
}

func TestRuntimesReportAStateTheyCannotRead(t *testing.T) {
	ctx := context.Background()
	for name, make := range map[string]func(*testing.T) (Runtime, *fakeEngine){"podman": linuxPodman, "docker": linuxDocker} {
		t.Run(name, func(t *testing.T) {
			rt, f := make(t)
			f.fail("suiteward-bad", errInjected).on("suiteward-json", "not json")
			if _, err := rt.State(ctx, "suiteward-bad"); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "inspect suiteward-bad") {
				t.Errorf("State = %v", err)
			}
			if _, err := rt.State(ctx, "suiteward-json"); err == nil || !strings.Contains(err.Error(), "read the state of suiteward-json") {
				t.Errorf("State of a reply that is not JSON = %v", err)
			}
			if _, err := rt.Inspect(ctx, "suiteward-bad"); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "inspect suiteward-bad") {
				t.Errorf("Inspect = %v", err)
			}
		})
	}
}

// ---- run ----

func TestRuntimesRefuseABadRunSpecBeforeRunningAnything(t *testing.T) {
	ctx := context.Background()
	for name, make := range map[string]func(*testing.T) (Runtime, *fakeEngine){"podman": linuxPodman, "docker": linuxDocker} {
		t.Run(name, func(t *testing.T) {
			rt, f := make(t)
			ok := RunSpec{Name: ContainerServe, Image: testImage, Network: NetworkName}
			if err := rt.Run(ctx, RunSpec{Image: testImage, Network: NetworkName}); err == nil || !strings.Contains(err.Error(), "needs a name, an image and a network") {
				t.Errorf("a spec without a name: %v", err)
			}
			withSecret := ok
			withSecret.Secrets = []string{SecretDBAuth} // the service is not a consumer of the database password
			if err := rt.Run(ctx, withSecret); err == nil || !strings.Contains(err.Error(), "is not for the container") {
				t.Errorf("a secret that is not for the container: %v", err)
			}
			unknown := ok
			unknown.Secrets = []string{"suiteward-nothing"}
			if err := rt.Run(ctx, unknown); err == nil || !strings.Contains(err.Error(), "is not a secret of the installation") {
				t.Errorf("an unknown secret: %v", err)
			}
			if len(f.find("run ")) != 0 {
				t.Errorf("a container was run for a refused spec: %v", f.lines())
			}
		})
	}
}

// A foreground container logs why it failed on stdout, so the end of it comes with the error; a detached one prints only its ID.
func TestRuntimesRunErrorsCarryTheTailOfAForegroundOutput(t *testing.T) {
	ctx := context.Background()
	for name, make := range map[string]func(*testing.T) (Runtime, *fakeEngine){"podman": linuxPodman, "docker": linuxDocker} {
		t.Run(name, func(t *testing.T) {
			rt, f := make(t)
			f.rules = append(f.rules, rule{match: "--name suiteward-migrate", out: "migration 20 failed: duplicate column\n", err: errInjected})
			err := rt.Run(ctx, RunSpec{Name: ContainerMigrate, Image: testImage, Network: NetworkName})
			if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "migration 20 failed: duplicate column") {
				t.Errorf("foreground: %v", err)
			}
			f.rules = append([]rule{{match: "--name suiteward ", out: "containerid", err: errInjected}}, f.rules...)
			err = rt.Run(ctx, RunSpec{Name: ContainerServe, Image: testImage, Network: NetworkName, Detach: true})
			if !errors.Is(err, errInjected) || strings.Contains(err.Error(), "containerid") {
				t.Errorf("detached: %v", err)
			}
		})
	}
}

func TestRuntimesRemoveErrorsNameTheContainer(t *testing.T) {
	ctx := context.Background()
	t.Run("podman", func(t *testing.T) {
		rt, f := linuxPodman(t)
		f.fail("rm --force", errInjected)
		if err := rt.Remove(ctx, ContainerServe); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "remove suiteward") {
			t.Errorf("Remove = %v", err)
		}
	})
	t.Run("docker", func(t *testing.T) {
		rt, f := linuxDocker(t)
		f.fail("container ls --all --filter name=suiteward ", errInjected)
		if err := rt.Remove(ctx, ContainerServe); !errors.Is(err, errInjected) {
			t.Errorf("Remove with a failing lookup = %v", err)
		}
		rt, f = linuxDocker(t)
		f.on("container ls --all --filter name=suiteward ", "suiteward\n").fail("stop --time 45", errInjected)
		if err := rt.Remove(ctx, ContainerServe); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "stop suiteward") {
			t.Errorf("Remove with a failing stop = %v", err)
		}
		if len(f.find("rm --force")) != 0 {
			t.Error("a container that did not stop was removed")
		}
		rt, f = linuxDocker(t)
		f.on("container ls --all --filter name=suiteward ", "suiteward\n").fail("rm --force", errInjected)
		if err := rt.Remove(ctx, ContainerServe); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "remove suiteward") {
			t.Errorf("Remove with a failing rm = %v", err)
		}
	})
	t.Run("docker run stops when the old container cannot be removed", func(t *testing.T) {
		rt, f := linuxDocker(t)
		f.fail("container ls --all", errInjected)
		if err := rt.Run(ctx, RunSpec{Name: ContainerServe, Image: testImage, Network: NetworkName}); !errors.Is(err, errInjected) {
			t.Errorf("Run = %v", err)
		}
		if len(f.find("run ")) != 0 {
			t.Error("a container was started over one that could not be removed")
		}
	})
}

// ---- export ----

func TestExportVolumeErrors(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/volume.tar"
	t.Run("podman", func(t *testing.T) {
		rt, f := linuxPodman(t)
		f.fail("volume export", errInjected)
		if err := rt.ExportVolume(ctx, VolumeData, path); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "export the volume suiteward-data") {
			t.Errorf("ExportVolume = %v", err)
		}
	})
	cases := []struct {
		name  string
		rules func(f *fakeEngine)
		want  string
	}{
		{"the database cannot be inspected", func(f *fakeEngine) { f.fail("inspect --type container suiteward-db", errInjected) }, "inspect suiteward-db"},
		{"the inspection is not JSON", func(f *fakeEngine) { f.on("inspect --type container suiteward-db", "not json") }, "suiteward-db"},
		{"the database reports no image", func(f *fakeEngine) { f.on("inspect --type container suiteward-db", `[{"Name":"/suiteward-db"}]`) }, "reports no image"},
		{"the export fails", func(f *fakeEngine) {
			f.fail("tar -cf", errInjected).on("inspect --type container suiteward-db", `[{"Name":"/suiteward-db","Config":{"Image":"`+testImage+`"}}]`)
		}, "export the volume suiteward-data"},
	}
	for _, tc := range cases {
		t.Run("docker: "+tc.name, func(t *testing.T) {
			rt, f := linuxDocker(t)
			tc.rules(f)
			err := rt.ExportVolume(ctx, VolumeData, path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ExportVolume = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// ---- secrets ----

func TestPodmanSecretsRefuseWhatIsNotInTheCatalogOrCannotBeDone(t *testing.T) {
	ctx := context.Background()
	rt, f := linuxPodman(t)
	secrets := rt.Secrets()
	identified := len(f.calls) // asking the engine who it is is not asking about the secret
	const unknown = "suiteward-nothing"
	if _, err := secrets.Exists(ctx, unknown); err == nil || !strings.Contains(err.Error(), "is not a secret of the installation") {
		t.Errorf("Exists of an unknown secret = %v", err)
	}
	if err := secrets.Write(ctx, unknown, []byte("v"), false); err == nil || !strings.Contains(err.Error(), "is not a secret of the installation") {
		t.Errorf("Write of an unknown secret = %v", err)
	}
	if _, err := secrets.Read(ctx, unknown); err == nil || !strings.Contains(err.Error(), "is not a secret of the installation") {
		t.Errorf("Read of an unknown secret = %v", err)
	}
	if err := secrets.Remove(ctx, unknown); err == nil || !strings.Contains(err.Error(), "is not a secret of the installation") {
		t.Errorf("Remove of an unknown secret = %v", err)
	}
	if len(f.calls) != identified {
		t.Errorf("the engine was asked about an unknown secret: %v", f.lines()[identified:])
	}

	// A secret that is there is not overwritten, and the value is not run.
	f.on("secret ls --filter name="+SecretMCPAuth, SecretMCPAuth+"\n")
	if err := secrets.Write(ctx, SecretMCPAuth, []byte(secretValue), false); !errors.Is(err, ErrSecretExists) {
		t.Errorf("Write over an existing secret = %v, want ErrSecretExists", err)
	}
	if len(f.find("secret create")) != 0 {
		t.Error("a secret that exists was written over")
	}

	// A failing engine is reported by name, and a failing create does not carry the engine's text of the value.
	f.rules = append([]rule{{match: "secret ls --filter name=" + SecretDatabaseURL, err: errInjected}}, f.rules...)
	if err := secrets.Write(ctx, SecretDatabaseURL, []byte(secretValue), false); !errors.Is(err, errInjected) {
		t.Errorf("Write with a failing lookup = %v", err)
	}
	f.rules = append([]rule{{match: "secret create", err: errInjected}}, f.rules...)
	if err := secrets.Write(ctx, SecretGitHubAppKey, []byte(secretValue), true); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "create the secret "+SecretGitHubAppKey) {
		t.Errorf("Write with a failing create = %v", err)
	}
	f.rules = append([]rule{{match: "secret rm", err: errInjected}}, f.rules...)
	if err := secrets.Remove(ctx, SecretMCPAuth); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "remove the secret "+SecretMCPAuth) {
		t.Errorf("Remove = %v", err)
	}
	if err := rt.PurgeSecrets(ctx); !errors.Is(err, errInjected) {
		t.Errorf("PurgeSecrets = %v", err)
	}
	f.assertNoSecret(t, secretValue)
}

func TestDockerSecretsRefuseWhatIsNotInTheCatalogOrCannotBeDone(t *testing.T) {
	ctx := context.Background()
	rt, f := linuxDocker(t)
	secrets := rt.Secrets()
	identified := len(f.calls) // asking the engine who it is is not asking about the secret
	const unknown = "suiteward-nothing"
	if _, err := secrets.Exists(ctx, unknown); err == nil || !strings.Contains(err.Error(), "is not a secret of the installation") {
		t.Errorf("Exists of an unknown secret = %v", err)
	}
	if err := secrets.Write(ctx, unknown, []byte("v"), true); err == nil || !strings.Contains(err.Error(), "is not a secret of the installation") {
		t.Errorf("Write of an unknown secret = %v", err)
	}
	if _, err := secrets.Read(ctx, unknown); err == nil || !strings.Contains(err.Error(), "is not a secret of the installation") {
		t.Errorf("Read of an unknown secret = %v", err)
	}
	if err := secrets.Remove(ctx, unknown); err == nil || !strings.Contains(err.Error(), "is not a secret of the installation") {
		t.Errorf("Remove of an unknown secret = %v", err)
	}
	if ok, err := secrets.(interface {
		Consistent(context.Context, string) (bool, error)
	}).Consistent(ctx, unknown); err == nil || ok {
		t.Errorf("Consistent of an unknown secret = %v, %v", ok, err)
	}
	if len(f.calls) != identified {
		t.Errorf("the engine was asked about an unknown secret: %v", f.lines()[identified:])
	}
}

func TestDockerSecretsReportAnEngineThatFails(t *testing.T) {
	ctx := context.Background()
	t.Run("a volume probe fails", func(t *testing.T) {
		rt, f := linuxDocker(t)
		f.on("volume ls --filter name=suiteward-secrets-suiteward-migrate", "suiteward-secrets-suiteward-migrate\n")
		f.fail("test -f /s/database-owner-url", errInjected)
		_, err := rt.Secrets().Exists(ctx, SecretDatabaseOwnerURL)
		if err == nil || !strings.Contains(err.Error(), "look for the secret "+SecretDatabaseOwnerURL) {
			t.Errorf("Exists = %v", err)
		}
	})
	t.Run("a volume lookup fails", func(t *testing.T) {
		rt, f := linuxDocker(t)
		f.fail("volume ls --filter name=suiteward-secrets-suiteward-db", errInjected)
		if _, err := rt.Secrets().Exists(ctx, SecretDBAuth); !errors.Is(err, errInjected) {
			t.Errorf("Exists = %v", err)
		}
	})
	t.Run("a write fails and the error does not carry the value", func(t *testing.T) {
		rt, f := linuxDocker(t)
		f.fail("cat > /s/database-url", errors.New("sh: "+secretValue))
		err := rt.Secrets().Write(ctx, SecretDatabaseURL, []byte(secretValue), true)
		if err == nil || !strings.Contains(err.Error(), "write the secret "+SecretDatabaseURL+" for suiteward-migrate") {
			t.Errorf("Write = %v", err)
		}
		f.assertNoSecret(t, secretValue)
	})
	t.Run("a write over an existing secret is refused", func(t *testing.T) {
		rt, f := linuxDocker(t)
		f.on("volume ls --filter name=suiteward-secrets-suiteward-db", "suiteward-secrets-suiteward-db\n").on("test -f /s/postgres-password", "yes\n")
		if err := rt.Secrets().Write(ctx, SecretDBAuth, []byte(secretValue), false); !errors.Is(err, ErrSecretExists) {
			t.Errorf("Write = %v, want ErrSecretExists", err)
		}
		if len(f.find("cat > /s/")) != 0 {
			t.Error("a secret that exists was written over")
		}
	})
	t.Run("a write whose lookup fails", func(t *testing.T) {
		rt, f := linuxDocker(t)
		f.fail("volume ls --filter name=suiteward-secrets-suiteward-db", errInjected)
		if err := rt.Secrets().Write(ctx, SecretDBAuth, []byte(secretValue), false); !errors.Is(err, errInjected) {
			t.Errorf("Write = %v", err)
		}
	})
}

func TestDockerSecretsReportAReadOrARemoveThatFails(t *testing.T) {
	ctx := context.Background()
	consistent := func(s SecretStore, name string) (bool, error) {
		return s.(interface {
			Consistent(context.Context, string) (bool, error)
		}).Consistent(ctx, name)
	}
	t.Run("a read fails and says nothing of why", func(t *testing.T) {
		rt, f := linuxDocker(t)
		f.fail("cat /s/mcp-token", errors.New("sh: "+secretValue))
		_, err := rt.Secrets().Read(ctx, SecretMCPAuth)
		if err == nil || strings.Contains(err.Error(), secretValue) || !strings.Contains(err.Error(), "read the secret "+SecretMCPAuth+" failed") {
			t.Errorf("Read = %v", err)
		}
	})
	t.Run("copies that differ", func(t *testing.T) {
		rt, f := linuxDocker(t)
		f.on("suiteward-secrets-suiteward-migrate:/s:ro", "postgres://a").on("suiteward-secrets-suiteward:/s:ro", "postgres://b")
		same, err := consistent(rt.Secrets(), SecretDatabaseURL)
		if err != nil || same {
			t.Errorf("Consistent = %v, %v; want a difference", same, err)
		}
	})
	t.Run("a copy that cannot be read", func(t *testing.T) {
		rt, f := linuxDocker(t)
		f.fail("suiteward-secrets-suiteward:/s:ro", errInjected)
		if same, err := consistent(rt.Secrets(), SecretDatabaseURL); err == nil || same {
			t.Errorf("Consistent = %v, %v", same, err)
		}
	})
	t.Run("a remove", func(t *testing.T) {
		rt, f := linuxDocker(t)
		f.fail("volume ls --filter name=suiteward-secrets-suiteward-db", errInjected)
		if err := rt.Secrets().Remove(ctx, SecretDBAuth); !errors.Is(err, errInjected) {
			t.Errorf("Remove with a failing lookup = %v", err)
		}
		rt, f = linuxDocker(t)
		f.on("volume ls --filter name=suiteward-secrets-suiteward-db", "suiteward-secrets-suiteward-db\n").fail("rm -f /s/postgres-password", errInjected)
		if err := rt.Secrets().Remove(ctx, SecretDBAuth); err == nil || !strings.Contains(err.Error(), "remove the secret "+SecretDBAuth+" for suiteward-db") {
			t.Errorf("Remove with a failing rm = %v", err)
		}
		// A volume that is not there has no secret to remove.
		rt, f = linuxDocker(t)
		if err := rt.Secrets().Remove(ctx, SecretDBAuth); err != nil {
			t.Errorf("Remove with no volume = %v", err)
		}
		if len(f.find("rm -f")) != 0 {
			t.Error("a volume that does not exist was run")
		}
	})
	t.Run("a purge stops at the first volume it cannot delete", func(t *testing.T) {
		rt, f := linuxDocker(t)
		// All three secret volumes exist, so a purge that went on would try the other two.
		f.on("volume ls --filter name=suiteward-secrets-suiteward-migrate", "suiteward-secrets-suiteward-migrate\n").
			on("volume ls --filter name=suiteward-secrets-suiteward-db", "suiteward-secrets-suiteward-db\n").
			on("volume ls --filter name=suiteward-secrets-suiteward --format", "suiteward-secrets-suiteward\n").
			fail("volume rm", errInjected)
		if err := rt.PurgeSecrets(ctx); !errors.Is(err, errInjected) {
			t.Errorf("PurgeSecrets = %v", err)
		}
		if removals := f.find("volume rm"); len(removals) != 1 || removals[0].line() != "volume rm suiteward-secrets-suiteward" {
			t.Errorf("the purge removed %v; want it to try the first volume only and stop at its failure", removals)
		}
	})
}

// ---- the database role ----

func TestOwnerPSQLErrorsKeepAPasswordOutOfTheMessage(t *testing.T) {
	ctx := context.Background()
	rt, f := linuxPodman(t)
	f.fail("psql", errors.New("ERROR: "+secretValue))
	if _, err := ownerPSQL(ctx, rt, "SELECT 1", false); err == nil || !strings.Contains(err.Error(), secretValue) {
		t.Errorf("a plain statement that fails does not say why: %v", err)
	}
	_, err := setAppRolePassword(ctx, rt, false)
	if err == nil || strings.Contains(err.Error(), secretValue) || !strings.Contains(err.Error(), "the statement is not shown") {
		t.Errorf("setAppRolePassword = %v", err)
	}
	if _, err := appRoleExists(ctx, rt); err == nil {
		t.Error("a role that could not be looked up was reported as absent")
	}
}
