package installer

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

// listeningPorts reads the output of `ss -ltn` as the set of local TCP ports something listens on, whatever the address. Both
// tiers use it: the isolated install on its own machine, the co-located install inside the Podman machine.
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
// distribution and the Podman machine share one network, so another instance can hold it. The ports in own are the stack's
// own service's, which is replaced or kept, so they are no conflict. A host without ss is told so and goes on: the readiness
// wait of the install still finds a port in use. Any other failure of ss, a context that was cancelled included, is returned:
// a check that failed has not passed.
func checkPortsFree(ctx context.Context, d Deps, s Settings, own []int, say func(string, ...any)) error {
	out, err := d.Exec(ctx, "ss", []string{"-ltn"}, nil)
	switch {
	case errors.Is(err, exec.ErrNotFound):
		say("The ports could not be checked, because ss is not installed. If a port is busy, the service will not start and the install will say so.\n")
		return nil
	case err != nil:
		return fmt.Errorf("list the ports something listens on (ss -ltn): %w", err)
	}
	listening := listeningPorts(out)
	for _, p := range []struct {
		flag string
		port int
	}{{"--http-port", s.HTTPPort}, {"--mcp-port", s.MCPPort}} {
		if listening[p.port] && !slices.Contains(own, p.port) {
			return fmt.Errorf("port %d is already in use on this machine, so nothing was changed (something listens on it; WSL2 distributions and the Podman machine share one network): choose another port with %s", p.port, p.flag)
		}
	}
	return nil
}
