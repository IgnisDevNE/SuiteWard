package installer

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// asDiffError fails the test unless err is a *DiffError, and returns it.
func asDiffError(t *testing.T, err error) *DiffError {
	t.Helper()
	var diff *DiffError
	if !errors.As(err, &diff) {
		t.Fatalf("got %v, want a *DiffError", err)
	}
	return diff
}

func diffMentions(e *DiffError, text string) bool {
	return slices.ContainsFunc(e.Diffs, func(d Diff) bool { return strings.Contains(d.Want+d.Got+d.Field+d.Source, text) })
}

func renderedUnits(t *testing.T, mutate func(*UnitParams)) map[string][]byte {
	t.Helper()
	p := isolatedParams()
	if mutate != nil {
		mutate(&p)
	}
	units, err := RenderUnits(p)
	if err != nil {
		t.Fatal(err)
	}
	return units
}

func TestAdoptUnitsCarriesTheKnownFields(t *testing.T) {
	installed := renderedUnits(t, func(p *UnitParams) {
		p.HTTPPort, p.MCPPort, p.JobTimeout, p.EnvFile = 18081, 18091, "3m", "/etc/suiteward/github.env"
		p.Digest = "sha256:" + strings.Repeat("b", 64)
	})
	got, err := AdoptUnits(installed, nil)
	if err != nil {
		t.Fatal(err)
	}
	s := got.Settings
	if s.Tier != config.TierIsolated || s.Runtime != RuntimePodman || s.HTTPPort != 18081 || s.MCPPort != 18091 || s.JobTimeout != "3m" {
		t.Errorf("settings: %+v", s)
	}
	if got.Image != releaseImagePrefix+"sha256:"+strings.Repeat("b", 64) || got.EnvFile != "/etc/suiteward/github.env" {
		t.Errorf("image %q, env file %q", got.Image, got.EnvFile)
	}
}

func TestAdoptUnitsOfTheDefaultRender(t *testing.T) {
	got, err := AdoptUnits(renderedUnits(t, nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.HTTPPort != DefaultHTTPPort || got.Settings.JobTimeout != "" || got.EnvFile != DefaultEnvFile {
		t.Errorf("got %+v", got)
	}
	if err := got.Settings.Validate(); err != nil {
		t.Errorf("the carried settings are invalid: %v", err)
	}
}

func TestAdoptUnitsIgnoresCommentsAndBlankLines(t *testing.T) {
	installed := renderedUnits(t, nil)
	installed["suiteward.container"] = append([]byte("# edited by the owner\n\n"), installed["suiteward.container"]...)
	if _, err := AdoptUnits(installed, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptUnitsStopsOnAnythingUnknown(t *testing.T) {
	pgDigestLine := func(u map[string][]byte) []byte {
		pg, _ := PostgresImage()
		return bytes.Replace(u["suiteward-db.container"], []byte(pg), []byte(strings.Replace(pg, "postgres:", "postgres:99-", 1)), 1)
	}
	cases := map[string]struct {
		mutate func(map[string][]byte)
		want   string // what the diff must mention
	}{
		"an extra environment variable": {func(u map[string][]byte) {
			u["suiteward.container"] = bytes.Replace(u["suiteward.container"], []byte("[Service]"), []byte("Environment=OWNER_EDIT=1\n\n[Service]"), 1)
		}, "OWNER_EDIT"},
		"a weakened hardening flag": {func(u map[string][]byte) {
			u["suiteward.container"] = bytes.Replace(u["suiteward.container"], []byte("ReadOnly=true"), []byte("ReadOnly=false"), 1)
		}, "ReadOnly"},
		"the owner URL on the service": {func(u map[string][]byte) {
			u["suiteward.container"] = bytes.Replace(u["suiteward.container"], []byte("[Service]"),
				[]byte("Secret=suiteward-database-owner-url,type=mount,target=/run/secrets/owner\n\n[Service]"), 1)
		}, "owner"},
		"another PostgreSQL pin": {func(u map[string][]byte) { u["suiteward-db.container"] = pgDigestLine(u) }, "postgres:99-"},
		"a changed migrate unit": {func(u map[string][]byte) {
			u["suiteward-migrate.container"] = bytes.Replace(u["suiteward-migrate.container"], []byte("Type=oneshot"), []byte("Type=simple"), 1)
		}, "Type="},
		"a missing unit": {func(u map[string][]byte) { delete(u, "suiteward.network") }, "suiteward.network"},
		"an image without a digest": {func(u map[string][]byte) {
			for _, name := range []string{"suiteward.container", "suiteward-migrate.container"} {
				u[name] = bytes.Replace(u[name], []byte("@"+testDigest), []byte(":latest"), 1)
			}
		}, "Image"},
		"a port outside the loopback": {func(u map[string][]byte) {
			u["suiteward.container"] = bytes.Replace(u["suiteward.container"], []byte("127.0.0.1:8081:8080"), []byte("0.0.0.0:8081:8080"), 1)
		}, "0.0.0.0"},
		"an unusable job timeout": {func(u map[string][]byte) {
			u["suiteward.container"] = bytes.Replace(u["suiteward.container"], []byte("[Service]"), []byte("Environment=SUITEWARD_JOB_TIMEOUT=soon\n\n[Service]"), 1)
		}, "soon"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			installed := renderedUnits(t, nil)
			tc.mutate(installed)
			_, err := AdoptUnits(installed, nil)
			if diff := asDiffError(t, err); !diffMentions(diff, tc.want) {
				t.Fatalf("the diff does not mention %q: %v", tc.want, diff)
			}
		})
	}
}

func TestAdoptUnitsReportsEveryDifferenceAtOnce(t *testing.T) {
	installed := renderedUnits(t, nil)
	installed["suiteward.container"] = bytes.Replace(installed["suiteward.container"], []byte("ReadOnly=true"), []byte("ReadOnly=false"), 1)
	installed["suiteward-migrate.container"] = bytes.Replace(installed["suiteward-migrate.container"], []byte("Type=oneshot"), []byte("Type=simple"), 1)
	diff := asDiffError(t, func() error { _, err := AdoptUnits(installed, nil); return err }())
	if !diffMentions(diff, "ReadOnly") || !diffMentions(diff, "Type=") {
		t.Fatalf("not every difference was reported: %v", diff)
	}
	if !strings.Contains(diff.Error(), "nothing was changed") {
		t.Fatalf("the error does not say that nothing was overwritten: %v", diff)
	}
}

func TestAdoptUnitsComparesWithTheNamedRelease(t *testing.T) {
	templates, err := embeddedUnits()
	if err != nil {
		t.Fatal(err)
	}
	older := maps.Clone(templates)
	older["suiteward.container"] = bytes.Replace(templates["suiteward.container"], []byte("[Service]"), []byte("Environment=OLD_RELEASE=1\n\n[Service]"), 1)
	p := isolatedParams()
	installed, err := render(older, p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdoptUnits(installed, older); err != nil {
		t.Fatalf("a stack of the named release must be adopted: %v", err)
	}
	if _, err := AdoptUnits(installed, nil); !diffMentions(asDiffError(t, err), "OLD_RELEASE") {
		t.Fatal("against another release the extra line must stop the adoption")
	}
}

// podmanInspect builds the JSON `podman inspect` prints for a container of spec: the shape of a rootless Podman 6 machine,
// where --cap-drop all is recorded as the list of default capabilities, the network is under NetworkSettings and the
// secrets under Config.Secrets.
func podmanInspect(t *testing.T, spec RunSpec, edit func(map[string]any)) []byte {
	image := podmanImageName(spec.Image)
	t.Helper()
	env := []string{"PATH=/usr/bin", "SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt"}
	if strings.Contains(spec.Image, "suiteward@") {
		env = append(env, "SUITEWARD_HTTP_ADDR=0.0.0.0:8080", "SUITEWARD_MCP_ADDR=0.0.0.0:8090", "SUITEWARD_ARTIFACT_DIR=/data/artifacts")
	} else {
		env = append(env, "PG_MAJOR=18", "PGDATA=/var/lib/postgresql/18/docker")
	}
	env = append(env, spec.Env...)
	memory, err := parseMemory(spec.Memory)
	if err != nil {
		t.Fatal(err)
	}
	ports := map[string]any{}
	for _, p := range spec.Publish {
		parts := strings.Split(p, ":")
		ports[parts[2]+"/tcp"] = []map[string]string{{"HostIp": parts[0], "HostPort": parts[1]}}
	}
	tmpfs := map[string]string{}
	for _, m := range spec.Tmpfs {
		tmpfs[m] = "rprivate,nosuid,nodev,tmpcopyup"
	}
	var mounts []map[string]any
	for _, v := range spec.Volumes {
		name, dest, _ := strings.Cut(v, ":")
		mounts = append(mounts, map[string]any{"Type": "volume", "Name": name, "Destination": dest, "RW": true})
	}
	var secrets []map[string]any
	for _, name := range spec.Secrets {
		_, uid, err := secretFor(name, spec.Name)
		if err != nil {
			t.Fatal(err)
		}
		secrets = append(secrets, map[string]any{"Name": name, "UID": uid, "GID": 0, "Mode": 256})
	}
	config := map[string]any{"Image": image, "User": "65532:65532", "Env": env, "StopTimeout": spec.StopTimeout, "Secrets": secrets}
	if spec.User != "" {
		config["User"] = spec.User
	}
	if h := spec.Health; h != nil {
		interval, _ := time.ParseDuration(h.Interval)
		timeout, _ := time.ParseDuration(h.Timeout)
		config["Healthcheck"] = map[string]any{"Test": []string{"CMD-SHELL", h.Cmd}, "Interval": int64(interval), "Timeout": int64(timeout), "Retries": h.Retries}
	}
	obj := map[string]any{
		"Name": spec.Name, "ImageName": image, "Config": config, "Mounts": mounts,
		"NetworkSettings": map[string]any{"Networks": map[string]any{spec.Network: map[string]any{}}},
		"HostConfig": map[string]any{
			"ReadonlyRootfs": true, "SecurityOpt": []string{"no-new-privileges"}, "Memory": memory, "NetworkMode": "bridge",
			"CapAdd": []string{}, "RestartPolicy": map[string]any{"Name": spec.Restart, "MaximumRetryCount": 0},
			"CapDrop": []string{
				"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FOWNER", "CAP_FSETID", "CAP_KILL", "CAP_NET_BIND_SERVICE",
				"CAP_SETFCAP", "CAP_SETGID", "CAP_SETPCAP", "CAP_SETUID", "CAP_SYS_CHROOT",
			},
			"PortBindings": ports, "Tmpfs": tmpfs,
		},
	}
	if edit != nil {
		edit(obj)
	}
	raw, err := json.Marshal([]any{obj}) // podman inspect prints an array
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func localPS1Settings() Settings {
	s := colocatedSettings()
	s.JobTimeout = "3m"
	s.GitHub = GitHub{AppID: 1234, InstallationID: 5678, Owner: "Acme", Repositories: []string{"Acme/api", "Acme/web"}}
	return s
}

func inspectedStack(t *testing.T, s Settings, image string, edit func(name string, obj map[string]any)) map[string][]byte {
	t.Helper()
	stack, err := ColocatedStack(s, image)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, spec := range []RunSpec{stack.DB, stack.Serve} {
		out[spec.Name] = podmanInspect(t, spec, func(obj map[string]any) {
			if edit != nil {
				edit(spec.Name, obj)
			}
		})
	}
	return out
}

func TestAdoptContainersCarriesTheKnownFields(t *testing.T) {
	want := localPS1Settings()
	got, err := AdoptContainers(inspectedStack(t, want, testImage, nil))
	if err != nil {
		t.Fatal(err)
	}
	s := got.Settings
	if s.Tier != config.TierCoLocated || s.Runtime != RuntimePodman || s.HTTPPort != 18081 || s.MCPPort != 18091 || s.JobTimeout != "3m" {
		t.Errorf("settings: %+v", s)
	}
	if s.GitHub.AppID != 1234 || s.GitHub.InstallationID != 5678 || s.GitHub.Owner != "Acme" || !slices.Equal(s.GitHub.Repositories, want.GitHub.Repositories) {
		t.Errorf("github: %+v", s.GitHub)
	}
	if got.Image != testImage {
		t.Errorf("image %q", got.Image)
	}
	if err := s.Validate(); err != nil {
		t.Errorf("the carried settings are invalid: %v", err)
	}
}

func TestAdoptContainersWithoutGitHubAndWithAMigrateContainer(t *testing.T) {
	s := colocatedSettings()
	inspected := inspectedStack(t, s, "localhost/suiteward:local", nil)
	stack, _ := ColocatedStack(s, "localhost/suiteward:local")
	inspected[ContainerMigrate] = podmanInspect(t, stack.Migrate, nil)
	got, err := AdoptContainers(inspected)
	if err != nil {
		t.Fatal(err)
	}
	if got.Settings.GitHub.AppID != 0 || got.Image != "localhost/suiteward:local" {
		t.Errorf("got %+v", got)
	}
}

func TestAdoptContainersAcceptsDockersWayOfRecordingDroppedCapabilities(t *testing.T) {
	inspected := inspectedStack(t, localPS1Settings(), testImage, func(_ string, obj map[string]any) {
		obj["HostConfig"].(map[string]any)["CapDrop"] = []string{"ALL"}
	})
	if _, err := AdoptContainers(inspected); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptContainersStopsOnAnythingUnknown(t *testing.T) {
	host := func(obj map[string]any) map[string]any { return obj["HostConfig"].(map[string]any) }
	cfg := func(obj map[string]any) map[string]any { return obj["Config"].(map[string]any) }
	cases := map[string]struct {
		container string
		edit      func(map[string]any)
		want      string
	}{
		"an extra environment variable": {ContainerServe, func(o map[string]any) {
			cfg(o)["Env"] = append(cfg(o)["Env"].([]string), "SUITEWARD_OWNER_EDIT=1")
		}, "SUITEWARD_OWNER_EDIT"},
		"a changed image default": {ContainerServe, func(o map[string]any) {
			cfg(o)["Env"] = append(cfg(o)["Env"].([]string), "SUITEWARD_HTTP_ADDR=0.0.0.0:9999")
		}, "SUITEWARD_HTTP_ADDR"},
		"a writable root filesystem": {ContainerServe, func(o map[string]any) { host(o)["ReadonlyRootfs"] = false }, "read"},
		"capabilities kept":          {ContainerServe, func(o map[string]any) { host(o)["CapDrop"] = []string{} }, "cap"},
		"an added capability":        {ContainerServe, func(o map[string]any) { host(o)["CapAdd"] = []string{"CAP_SYS_ADMIN"} }, "cap"},
		"privileges allowed":         {ContainerServe, func(o map[string]any) { host(o)["SecurityOpt"] = []string{} }, "privileges"},
		"another memory limit":       {ContainerServe, func(o map[string]any) { host(o)["Memory"] = 1 << 30 }, "emory"},
		"another restart policy":     {ContainerServe, func(o map[string]any) { host(o)["RestartPolicy"] = map[string]any{"Name": "no"} }, "estart"},
		"a published extra port": {ContainerServe, func(o map[string]any) {
			host(o)["PortBindings"].(map[string]any)["22/tcp"] = []map[string]string{{"HostIp": "0.0.0.0", "HostPort": "2222"}}
		}, "22/tcp"},
		"a port outside the loopback": {ContainerServe, func(o map[string]any) {
			host(o)["PortBindings"].(map[string]any)["8080/tcp"] = []map[string]string{{"HostIp": "0.0.0.0", "HostPort": "18081"}}
		}, "0.0.0.0"},
		"an extra mount": {ContainerServe, func(o map[string]any) {
			o["Mounts"] = append(o["Mounts"].([]map[string]any), map[string]any{"Type": "bind", "Source": "/", "Destination": "/host"})
		}, "/host"},
		"the owner URL on the service": {ContainerServe, func(o map[string]any) {
			cfg(o)["Secrets"] = append(cfg(o)["Secrets"].([]map[string]any), map[string]any{"Name": SecretDatabaseOwnerURL, "UID": 65532, "GID": 0, "Mode": 256})
		}, SecretDatabaseOwnerURL},
		"a secret readable by others": {ContainerServe, func(o map[string]any) { cfg(o)["Secrets"].([]map[string]any)[0]["Mode"] = 420 }, "ecret"},
		"another network": {ContainerServe, func(o map[string]any) {
			o["NetworkSettings"] = map[string]any{"Networks": map[string]any{"host": map[string]any{}}}
		}, "etwork"},
		"another stop timeout":        {ContainerServe, func(o map[string]any) { cfg(o)["StopTimeout"] = 10 }, "top"},
		"another database user":       {ContainerDB, func(o map[string]any) { cfg(o)["User"] = "0:0" }, "ser"},
		"a changed health check":      {ContainerDB, func(o map[string]any) { cfg(o)["Healthcheck"].(map[string]any)["Retries"] = 3 }, "ealth"},
		"another database image":      {ContainerDB, func(o map[string]any) { cfg(o)["Image"] = "docker.io/library/postgres:17" }, "image"},
		"another service image shape": {ContainerServe, func(o map[string]any) { cfg(o)["Image"] = "" }, "image"},
		"another database image setting": {ContainerDB, func(o map[string]any) {
			cfg(o)["Env"] = append(cfg(o)["Env"].([]string), "POSTGRES_INITDB_ARGS=--data-checksums")
		}, "POSTGRES_INITDB_ARGS"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			inspected := inspectedStack(t, localPS1Settings(), testImage, func(c string, obj map[string]any) {
				if c == tc.container {
					tc.edit(obj)
				}
			})
			_, err := AdoptContainers(inspected)
			diff := asDiffError(t, err)
			if !diffMentions(diff, tc.want) {
				t.Fatalf("the diff does not mention %q: %v", tc.want, diff)
			}
			if !diffMentions(diff, tc.container) {
				t.Fatalf("the diff does not name the container %s: %v", tc.container, diff)
			}
		})
	}
}

func TestAdoptContainersNeedsTheServiceAndTheDatabase(t *testing.T) {
	inspected := inspectedStack(t, colocatedSettings(), testImage, nil)
	for _, missing := range []string{ContainerServe, ContainerDB} {
		partial := maps.Clone(inspected)
		delete(partial, missing)
		if _, err := AdoptContainers(partial); err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("without %s: %v", missing, err)
		}
	}
	bad := maps.Clone(inspected)
	bad[ContainerServe] = []byte("not json")
	if _, err := AdoptContainers(bad); err == nil {
		t.Error("unreadable inspect output was accepted")
	}
	bad[ContainerServe] = []byte("[]")
	if _, err := AdoptContainers(bad); err == nil {
		t.Error("an empty inspect array was accepted")
	}
}

func TestParseMemory(t *testing.T) {
	for in, want := range map[string]int64{"256m": 256 << 20, "384m": 384 << 20, "1g": 1 << 30, "64k": 64 << 10, "100": 100} {
		got, err := parseMemory(in)
		if err != nil || got != want {
			t.Errorf("%s: got %d, %v, want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "m", "12x", "-1m"} {
		if _, err := parseMemory(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestAdoptContainersStopsWhenMigrateRunsAnotherImage(t *testing.T) {
	s := colocatedSettings()
	inspected := inspectedStack(t, s, testImage, nil)
	stack, _ := ColocatedStack(s, testImage)
	inspected[ContainerMigrate] = podmanInspect(t, stack.Migrate, func(o map[string]any) {
		o["Config"].(map[string]any)["Image"] = releaseImagePrefix + "sha256:" + strings.Repeat("c", 64)
	})
	diff := asDiffError(t, func() error { _, err := AdoptContainers(inspected); return err }())
	if !diffMentions(diff, ContainerMigrate) || !diffMentions(diff, "image") {
		t.Fatalf("got %v", diff)
	}
}

func TestAdoptContainersReportsEveryDifferenceAtOnce(t *testing.T) {
	inspected := inspectedStack(t, localPS1Settings(), testImage, func(c string, obj map[string]any) {
		switch c {
		case ContainerServe: // a known field that cannot be carried: the tier
			cfg := obj["Config"].(map[string]any)
			cfg["Env"] = append(cfg["Env"].([]string), "SUITEWARD_INSTALLATION_TIER=isolated")
		case ContainerDB:
			obj["HostConfig"].(map[string]any)["ReadonlyRootfs"] = false
		}
	})
	diff := asDiffError(t, func() error { _, err := AdoptContainers(inspected); return err }())
	if !diffMentions(diff, "SUITEWARD_INSTALLATION_TIER") || !diffMentions(diff, ContainerDB) {
		t.Fatalf("not every difference was reported: %v", diff)
	}
	if !strings.Contains(diff.Error(), "run again") {
		t.Fatalf("the error gives no way forward: %v", diff)
	}
}

func TestSameImage(t *testing.T) {
	pg, _ := PostgresImage() // docker.io/library/postgres:18.6-trixie@sha256:<digest>
	repo, digest, _ := strings.Cut(pg, "@")
	untagged := strings.TrimSuffix(repo, ":18.6-trixie") + "@" + digest
	otherDigest := strings.TrimSuffix(repo, ":18.6-trixie") + "@sha256:" + strings.Repeat("e", 64)
	for _, tc := range []struct {
		name      string
		want, got string
		same      bool
	}{
		{"the same tagged reference", pg, pg, true},
		{"Podman's tagless form of a tagged reference", pg, untagged, true},
		{"another tag with the same digest", pg, repo[:strings.LastIndex(repo, ":")] + ":19@" + digest, true},
		{"another digest", pg, otherDigest, false},
		{"another repository, same digest", pg, "docker.io/library/other@" + digest, false},
		{"another registry, same digest", pg, "quay.io/library/postgres@" + digest, false},
		{"no digest on the installed one", pg, strings.TrimSuffix(repo, ":18.6-trixie") + ":18.6-trixie", false},
		{"a tag only, equal", "localhost/suiteward:local", "localhost/suiteward:local", true},
		{"a tag only, different", "localhost/suiteward:local", "localhost/suiteward:other", false},
		{"empty", pg, "", false},
	} {
		if got := sameImage(tc.want, tc.got); got != tc.same {
			t.Errorf("%s: sameImage(%q, %q) = %v, want %v", tc.name, tc.want, tc.got, got, tc.same)
		}
	}
}

func TestAdoptContainersAcceptsTheTaggedImageToo(t *testing.T) {
	pg, _ := PostgresImage()
	inspected := inspectedStack(t, localPS1Settings(), testImage, func(name string, obj map[string]any) {
		if name == ContainerDB {
			obj["Config"].(map[string]any)["Image"] = pg
		}
	})
	if _, err := AdoptContainers(inspected); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptContainersRefusesAnotherRepositoryOrDigestOfTheDatabaseImage(t *testing.T) {
	pg, _ := PostgresImage()
	repo, digest, _ := strings.Cut(pg, "@")
	base := strings.TrimSuffix(repo, ":18.6-trixie")
	for name, image := range map[string]string{
		"another digest":     base + "@sha256:" + strings.Repeat("e", 64),
		"another repository": "docker.io/library/other@" + digest,
	} {
		inspected := inspectedStack(t, localPS1Settings(), testImage, func(c string, obj map[string]any) {
			if c == ContainerDB {
				obj["Config"].(map[string]any)["Image"] = image
			}
		})
		_, err := AdoptContainers(inspected)
		if diff := asDiffError(t, err); !diffMentions(diff, "image") {
			t.Errorf("%s: %v", name, diff)
		}
	}
}

// podmanImageName is how Podman records an image created from name:tag@digest: the tag is dropped.
func podmanImageName(image string) string {
	repo, digest, pinned := strings.Cut(image, "@")
	if !pinned {
		return image
	}
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		repo = repo[:i]
	}
	return repo + "@" + digest
}

func TestAdoptUnitsRelaxesExactlyOneDatabaseImageLineOnAnUpgrade(t *testing.T) {
	postgres, err := PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	other := "docker.io/library/postgres:17-trixie@sha256:" + strings.Repeat("d", 64)
	installed := renderedUnits(t, nil)
	db := string(installed["suiteward-db.container"])

	installed["suiteward-db.container"] = []byte(strings.Replace(db, "Image="+postgres, "Image="+oldPin, 1))
	if _, err := adoptUnits(installed, nil, true); err != nil {
		t.Fatalf("one older database pin is what an upgrade refreshes: %v", err)
	}

	installed["suiteward-db.container"] = []byte(strings.Replace(db, "Image="+postgres, "Image="+oldPin+"\nImage="+other, 1))
	diff := asDiffError(t, errAfter(adoptUnits(installed, nil, true)))
	if !diffMentions(diff, "Image="+other) {
		t.Errorf("a second database image line is a difference, and the diff names it: %v", diff.Diffs)
	}
}

// errAfter keeps the error of a call that also returns a value.
func errAfter[T any](_ T, err error) error { return err }

// The two lines of the service unit that mount the App key and name it; a GitHub-off install of v0.3.0-alpha.2 has neither.
var githubKeyUnitLines = []string{
	"Secret=suiteward-github-app-key,type=mount,target=/run/secrets/github-app-key,uid=65532,mode=0400",
	"Environment=SUITEWARD_GITHUB_APP_KEY_FILE=/run/secrets/github-app-key",
}

// withoutLines is unit without the given lines; it fails the test when a line is not there.
func withoutLines(t *testing.T, unit []byte, lines ...string) []byte {
	t.Helper()
	for _, line := range lines {
		if !bytes.Contains(unit, []byte(line+"\n")) {
			t.Fatalf("the unit has no line %q", line)
		}
		unit = bytes.Replace(unit, []byte(line+"\n"), nil, 1)
	}
	return unit
}

func TestAdoptUnitsAcceptsAServiceUnitWithoutTheGitHubKeyLines(t *testing.T) {
	installed := renderedUnits(t, nil)
	installed["suiteward.container"] = withoutLines(t, installed["suiteward.container"], githubKeyUnitLines...)
	got, err := AdoptUnits(installed, nil)
	if err != nil {
		t.Fatalf("a service unit of a stack that runs without GitHub is a known shape: %v", err)
	}
	if !got.NoGitHub {
		t.Error("the adoption must report a stack without GitHub")
	}

	// The units of v0.3.0-alpha.1 always had the lines.
	got, err = AdoptUnits(renderedUnits(t, nil), nil)
	if err != nil {
		t.Fatalf("units with the lines are still known: %v", err)
	}
	if got.NoGitHub {
		t.Error("units with the lines belong to a stack with GitHub")
	}
}

func TestAdoptUnitsStopsOnAServiceUnitWithOnlyOneOfTheGitHubKeyLines(t *testing.T) {
	for _, line := range githubKeyUnitLines {
		installed := renderedUnits(t, nil)
		installed["suiteward.container"] = withoutLines(t, installed["suiteward.container"], line)
		if diff := asDiffError(t, func() error { _, err := AdoptUnits(installed, nil); return err }()); !diffMentions(diff, "github-app-key") {
			t.Errorf("the diff must name the key line that is missing: %+v", diff.Diffs)
		}
	}
}
