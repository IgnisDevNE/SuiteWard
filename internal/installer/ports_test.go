package installer

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestListeningPortsReadsTheLocalPortOfEveryListener(t *testing.T) {
	out := `State   Recv-Q  Send-Q   Local Address:Port    Peer Address:Port  Process
LISTEN  0       4096         127.0.0.1:8081         0.0.0.0:*
LISTEN  0       4096           0.0.0.0:22           0.0.0.0:*
LISTEN  0       4096              [::]:8091            [::]:*
LISTEN  0       511                  *:3000               *:*
LISTEN  0       128       [fe80::1]%lo:9000            [::]:*
ESTAB   0       0        10.0.0.5:44312      10.0.0.9:5432
UNCONN  0       0            0.0.0.0:68           0.0.0.0:*
LISTEN  0       4096    garbage
`
	got := listeningPorts([]byte(out))
	for _, port := range []int{8081, 22, 8091, 3000, 9000} {
		if !got[port] {
			t.Errorf("port %d is listened on and was not found: %v", port, got)
		}
	}
	for _, port := range []int{44312, 5432, 68} {
		if got[port] {
			t.Errorf("port %d is no listener but was found", port)
		}
	}
}

func TestCheckPortsFreeNamesThePortAndTheFlag(t *testing.T) {
	s := isolatedSettings()
	s.HTTPPort, s.MCPPort = 18081, 18091
	run := func(out string, err error) (string, error) {
		var said strings.Builder
		d := Deps{Exec: func(_ context.Context, name string, args []string, _ io.Reader) ([]byte, error) {
			if name != "ss" || strings.Join(args, " ") != "-ltn" {
				t.Fatalf("unexpected command %s %v", name, args)
			}
			return []byte(out), err
		}}
		checkErr := checkPortsFree(context.Background(), d, s, func(format string, a ...any) { said.WriteString(format) })
		return said.String(), checkErr
	}

	if _, err := run("LISTEN 0 4096 127.0.0.1:8081 0.0.0.0:*\n", nil); err != nil {
		t.Errorf("ports the instance does not use are no conflict: %v", err)
	}
	for port, flag := range map[string]string{"18081": "--http-port", "18091": "--mcp-port"} {
		_, err := run("LISTEN 0 4096 0.0.0.0:"+port+" 0.0.0.0:*\n", nil)
		if err == nil || !strings.Contains(err.Error(), port) || !strings.Contains(err.Error(), flag) {
			t.Errorf("port %s: want an error naming it and %s, got %v", port, flag, err)
		}
	}
	said, err := run("", errors.New("ss: not found"))
	if err != nil || !strings.Contains(said, "could not be checked") {
		t.Errorf("a check that cannot run is a note, not a refusal: %q, %v", said, err)
	}
}
