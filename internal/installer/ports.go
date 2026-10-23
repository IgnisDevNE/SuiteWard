package installer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// listeningPorts reads the output of `ss -ltn` as the set of local TCP ports something listens on, whatever the address.
// shortcut: colocatedInstall.machineListening parses the same output on its own; another change owns colocated.go, so the two
// merge once both can be edited together.
func listeningPorts(out []byte) map[int]bool {
	listening := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "LISTEN" {
			continue
		}
		local := fields[3] // Local Address:Port
		if i := strings.LastIndex(local, ":"); i >= 0 {
			if port, err := strconv.Atoi(local[i+1:]); err == nil {
				listening[port] = true
			}
		}
	}
	return listening
}

// checkPortsFree refuses an HTTP or MCP port of s that something else listens on: the service would not start, and every WSL2
// distribution and the Podman machine share one network, so another instance can hold it. A host where ss cannot run is told so
// and goes on: the readiness wait of the install still finds a port in use.
func checkPortsFree(ctx context.Context, d Deps, s Settings, say func(string, ...any)) error {
	out, err := d.Exec(ctx, "ss", []string{"-ltn"}, nil)
	if err != nil {
		say("Note: the ports could not be checked (ss -ltn: %v); a port that is in use shows up when the service starts.\n", err)
		return nil
	}
	listening := listeningPorts(out)
	for _, p := range []struct {
		flag string
		port int
	}{{"--http-port", s.HTTPPort}, {"--mcp-port", s.MCPPort}} {
		if listening[p.port] {
			return fmt.Errorf("port %d is in use on this machine (something listens on it; WSL2 distributions and the Podman machine share one network): choose another with %s", p.port, p.flag)
		}
	}
	return nil
}
