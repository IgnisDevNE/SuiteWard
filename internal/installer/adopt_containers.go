package installer

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// inspected is what the guard reads of `podman inspect` (the keys Docker uses too). The shape was checked against a rootless
// Podman 6 machine: --cap-drop all is recorded as the list of default capabilities, the network is under NetworkSettings
// and the secrets under Config.Secrets.
type inspected struct {
	Config struct {
		Image       string
		User        string
		Env         []string
		StopTimeout *int
		Healthcheck *struct {
			Test              []string
			Interval, Timeout int64
			Retries           int
		}
		Secrets []struct {
			Name string
			UID  int
			Mode int
		}
	}
	HostConfig struct {
		ReadonlyRootfs bool
		CapAdd         []string
		CapDrop        []string
		SecurityOpt    []string
		Memory         int64
		RestartPolicy  struct{ Name string }
		PortBindings   map[string][]struct{ HostIp, HostPort string }
		Tmpfs          map[string]string
	}
	Mounts []struct {
		Type, Name, Source, Destination string
	}
	NetworkSettings struct{ Networks map[string]json.RawMessage }
}

// Values of the image that `inspect` lists next to the ones the stack sets; they are not differences while they hold.
var imageEnvDefaults = map[string]string{
	"SUITEWARD_HTTP_ADDR":    "0.0.0.0:8080",
	"SUITEWARD_MCP_ADDR":     "0.0.0.0:8090",
	"SUITEWARD_ARTIFACT_DIR": "/data/artifacts",
}

// The capabilities Podman lists as dropped for --cap-drop all.
var defaultCapabilities = []string{
	"CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FOWNER", "CAP_FSETID", "CAP_KILL", "CAP_NET_BIND_SERVICE",
	"CAP_SETFCAP", "CAP_SETGID", "CAP_SETPCAP", "CAP_SETUID", "CAP_SYS_CHROOT",
}

func parseInspect(name string, raw []byte) (inspected, error) {
	var in inspected
	raw = []byte(strings.TrimSpace(string(raw)))
	if strings.HasPrefix(string(raw), "[") { // `inspect` prints an array of one
		var list []inspected
		if err := json.Unmarshal(raw, &list); err != nil {
			return in, fmt.Errorf("read the inspect output of %s: %w", name, err)
		}
		if len(list) != 1 {
			return in, fmt.Errorf("the inspect output of %s describes %d containers, want 1", name, len(list))
		}
		return list[0], nil
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return in, fmt.Errorf("read the inspect output of %s: %w", name, err)
	}
	return in, nil
}

// AdoptContainers does the same for a stack of containers made by deploy/local.ps1 (Podman), from the output of
// `podman inspect` of suiteward-db and suiteward (and suiteward-migrate when it exists). The ports, tier, job timeout,
// GitHub values and image are carried; any other difference from ColocatedStack is a *DiffError. Podman's secrets are
// compared; a stack that keeps its secrets in volumes (Docker) is not adopted from here.
func AdoptContainers(raw map[string][]byte) (Adopted, error) {
	parsed := map[string]inspected{}
	for _, name := range []string{ContainerDB, ContainerMigrate, ContainerServe} {
		data, ok := raw[name]
		if !ok && name == ContainerMigrate {
			continue // a stack that ran migrate with --rm has no such container
		}
		if !ok {
			return Adopted{}, fmt.Errorf("the inspect output of %s is missing", name)
		}
		in, err := parseInspect(name, data)
		if err != nil {
			return Adopted{}, err
		}
		parsed[name] = in
	}
	settings, diffs := knownContainerFields(parsed[ContainerServe])
	image := parsed[ContainerServe].Config.Image
	stack, err := ColocatedStack(settings, image)
	if err != nil {
		// The expected stack cannot be built: say what could not be read, or why the carried values are unusable.
		if len(diffs) == 0 {
			diffs = []Diff{{Source: ContainerServe, Field: "known fields", Got: err.Error()}}
		}
		return Adopted{}, &DiffError{diffs}
	}
	specs := []RunSpec{stack.DB, stack.Migrate}
	if len(diffs) == 0 { // a service whose known fields could not be read is already reported, and its defaults would only repeat it
		specs = append(specs, stack.Serve)
	}
	for _, spec := range specs {
		in, ok := parsed[spec.Name]
		if !ok {
			continue
		}
		found, err := diffContainer(spec, in)
		if err != nil {
			return Adopted{}, err
		}
		diffs = append(diffs, found...)
	}
	if len(diffs) > 0 {
		return Adopted{}, &DiffError{diffs}
	}
	return Adopted{Settings: settings, Image: image}, nil
}

// sameImage says whether an installed container runs the image the stack expects. Podman records an image created from
// name:tag@digest as name@digest, so when both sides carry a digest the repository and the digest decide and the tag does not.
func sameImage(want, got string) bool {
	wantRepo, wantDigest, wantPinned := strings.Cut(want, "@")
	gotRepo, gotDigest, gotPinned := strings.Cut(got, "@")
	if !wantPinned || !gotPinned {
		return want == got
	}
	return wantDigest == gotDigest && withoutTag(wantRepo) == withoutTag(gotRepo)
}

func withoutTag(repo string) string {
	if i := strings.LastIndex(repo, ":"); i > strings.LastIndex(repo, "/") {
		return repo[:i]
	}
	return repo
}

func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// knownContainerFields reads the carried fields of the installed service container.
func knownContainerFields(in inspected) (Settings, []Diff) {
	s := DefaultSettings()
	var diffs []Diff
	bad := func(field, want, got string) {
		diffs = append(diffs, Diff{Source: ContainerServe, Field: field, Want: want, Got: got})
	}
	if in.Config.Image == "" {
		bad("image", "an image reference", "")
	}
	for _, port := range []struct {
		container string
		into      *int
	}{{"8080/tcp", &s.HTTPPort}, {"8090/tcp", &s.MCPPort}} {
		bindings := in.HostConfig.PortBindings[port.container]
		n, err := 0, error(nil)
		if len(bindings) == 1 && bindings[0].HostIp == "127.0.0.1" {
			n, err = strconv.Atoi(bindings[0].HostPort)
		}
		if len(bindings) != 1 || bindings[0].HostIp != "127.0.0.1" || err != nil {
			bad("ports", "127.0.0.1:<port> for "+port.container, fmt.Sprint(bindings))
			continue
		}
		*port.into = n
	}
	env := envMap(in.Config.Env)
	if tier := env["SUITEWARD_INSTALLATION_TIER"]; tier != string(config.TierCoLocated) {
		bad("environment", "SUITEWARD_INSTALLATION_TIER=co-located", "SUITEWARD_INSTALLATION_TIER="+tier)
	}
	s.JobTimeout = env["SUITEWARD_JOB_TIMEOUT"]
	if g, err := githubFromEnv(env); err != nil {
		bad("environment", "all or none of the GitHub values", err.Error())
	} else {
		s.GitHub = g
	}
	return s, diffs
}

// githubFromEnv reads the GitHub values of the service: all three or none.
func githubFromEnv(env map[string]string) (GitHub, error) {
	app, installation, repos := env["SUITEWARD_GITHUB_APP_ID"], env["SUITEWARD_GITHUB_INSTALLATION_ID"], env["SUITEWARD_GITHUB_REPOSITORIES"]
	if app == "" && installation == "" && repos == "" {
		return GitHub{}, nil
	}
	appID, err1 := strconv.ParseInt(app, 10, 64)
	installationID, err2 := strconv.ParseInt(installation, 10, 64)
	if err1 != nil || err2 != nil || repos == "" {
		return GitHub{}, fmt.Errorf("app id %q, installation id %q, repositories %q", app, installation, repos)
	}
	list := strings.Split(repos, ",")
	owner, _, _ := strings.Cut(list[0], "/")
	return GitHub{AppID: appID, InstallationID: installationID, Owner: owner, Repositories: list}, nil
}

type field struct{ name, value string }

func joined(items []string) string {
	items = slices.Clone(items)
	slices.Sort(items)
	return strings.Join(items, ", ")
}

func healthLine(cmd string, interval, timeout time.Duration, retries int) string {
	return fmt.Sprintf("%s every %s, timeout %s, %d retries", cmd, interval, timeout, retries)
}

// expectedFields are the properties of spec that an installed container must have.
func expectedFields(spec RunSpec) ([]field, error) {
	f := []field{{"image", spec.Image}, {"read-only root", "true"}, {"capabilities", "all dropped"}, {"no-new-privileges", "true"}, {"network", spec.Network}}
	if spec.User != "" {
		f = append(f, field{"user", spec.User})
	}
	if spec.Memory != "" {
		memory, err := parseMemory(spec.Memory)
		if err != nil {
			return nil, err
		}
		f = append(f, field{"memory", strconv.FormatInt(memory, 10) + " bytes"})
	}
	if spec.StopTimeout > 0 {
		f = append(f, field{"stop timeout", strconv.Itoa(spec.StopTimeout)})
	}
	if spec.Restart != "" {
		f = append(f, field{"restart", spec.Restart})
	}
	var ports, secrets []string
	for _, p := range spec.Publish {
		parts := strings.Split(p, ":") // host-ip:host-port:container-port
		ports = append(ports, parts[2]+"/tcp="+parts[0]+":"+parts[1])
	}
	for _, name := range spec.Secrets {
		_, uid, err := secretFor(name, spec.Name)
		if err != nil {
			return nil, err
		}
		secrets = append(secrets, fmt.Sprintf("%s uid=%d mode=0400", name, uid))
	}
	health := ""
	if h := spec.Health; h != nil {
		interval, err1 := time.ParseDuration(h.Interval)
		timeout, err2 := time.ParseDuration(h.Timeout)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("the health check of %s has an unreadable interval or timeout", spec.Name)
		}
		health = healthLine(h.Cmd, interval, timeout, h.Retries)
	}
	return append(f,
		field{"ports", joined(ports)}, field{"environment", joined(spec.Env)}, field{"tmpfs", joined(spec.Tmpfs)},
		field{"mounts", joined(spec.Volumes)}, field{"secrets", joined(secrets)}, field{"health check", health}), nil
}

// actualFields are the same properties of an installed container.
func actualFields(in inspected) map[string]string {
	h := in.HostConfig
	got := map[string]string{
		"image": in.Config.Image, "read-only root": strconv.FormatBool(h.ReadonlyRootfs), "user": in.Config.User,
		"memory": strconv.FormatInt(h.Memory, 10) + " bytes", "restart": h.RestartPolicy.Name,
		"capabilities": capabilityState(h.CapAdd, h.CapDrop), "stop timeout": "0",
		"no-new-privileges": strconv.FormatBool(slices.ContainsFunc(h.SecurityOpt, func(o string) bool { return strings.HasPrefix(o, "no-new-privileges") })),
	}
	if t := in.Config.StopTimeout; t != nil {
		got["stop timeout"] = strconv.Itoa(*t)
	}
	var networks, ports, env, tmpfs, mounts, secrets []string
	for name := range in.NetworkSettings.Networks {
		networks = append(networks, name)
	}
	for container, bindings := range h.PortBindings {
		for _, b := range bindings {
			ports = append(ports, container+"="+b.HostIp+":"+b.HostPort)
		}
	}
	for k, v := range envMap(in.Config.Env) {
		if !strings.HasPrefix(k, "SUITEWARD_") && !strings.HasPrefix(k, "POSTGRES_") {
			continue
		}
		if def, isDefault := imageEnvDefaults[k]; isDefault && def == v {
			continue
		}
		env = append(env, k+"="+v)
	}
	for path := range h.Tmpfs {
		tmpfs = append(tmpfs, path)
	}
	for _, m := range in.Mounts {
		if m.Type == "volume" {
			mounts = append(mounts, m.Name+":"+m.Destination)
		} else {
			mounts = append(mounts, m.Type+":"+m.Source+":"+m.Destination)
		}
	}
	for _, s := range in.Config.Secrets {
		secrets = append(secrets, fmt.Sprintf("%s uid=%d mode=%04o", s.Name, s.UID, s.Mode))
	}
	got["network"], got["ports"], got["environment"] = joined(networks), joined(ports), joined(env)
	got["tmpfs"], got["mounts"], got["secrets"], got["health check"] = joined(tmpfs), joined(mounts), joined(secrets), ""
	if hc := in.Config.Healthcheck; hc != nil {
		cmd := strings.Join(hc.Test, " ")
		if len(hc.Test) == 2 && hc.Test[0] == "CMD-SHELL" {
			cmd = hc.Test[1]
		}
		got["health check"] = healthLine(cmd, time.Duration(hc.Interval), time.Duration(hc.Timeout), hc.Retries)
	}
	return got
}

// capabilityState says whether every capability is dropped and none added. Docker records "ALL"; Podman lists the default set.
func capabilityState(add, drop []string) string {
	if len(add) > 0 {
		return "added " + joined(add)
	}
	dropped := map[string]bool{}
	for _, c := range drop {
		dropped[strings.ToUpper(c)] = true
	}
	if dropped["ALL"] || !slices.ContainsFunc(defaultCapabilities, func(c string) bool { return !dropped[c] }) {
		return "all dropped"
	}
	return "kept; dropped only " + joined(drop)
}

// diffContainer lists the properties of an installed container that differ from spec.
func diffContainer(spec RunSpec, in inspected) ([]Diff, error) {
	want, err := expectedFields(spec)
	if err != nil {
		return nil, err
	}
	got := actualFields(in)
	var diffs []Diff
	for _, f := range want {
		if f.name == "image" && sameImage(f.value, got[f.name]) {
			continue
		}
		if got[f.name] != f.value {
			diffs = append(diffs, Diff{Source: spec.Name, Field: f.name, Want: f.value, Got: got[f.name]})
		}
	}
	return diffs, nil
}
