package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
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
	run := func(own []int, out string, err error) (string, error) {
		var said strings.Builder
		d := Deps{Exec: func(_ context.Context, name string, args []string, _ io.Reader) ([]byte, error) {
			if name != "ss" || strings.Join(args, " ") != "-ltn" {
				t.Fatalf("unexpected command %s %v", name, args)
			}
			return []byte(out), err
		}}
		checkErr := checkPortsFree(context.Background(), d, s, own, func(format string, a ...any) { said.WriteString(format) })
		return said.String(), checkErr
	}

	if _, err := run(nil, "LISTEN 0 4096 127.0.0.1:8081 0.0.0.0:*\n", nil); err != nil {
		t.Errorf("ports the instance does not use are no conflict: %v", err)
	}
	for port, flag := range map[string]string{"18081": "--http-port", "18091": "--mcp-port"} {
		_, err := run(nil, "LISTEN 0 4096 0.0.0.0:"+port+" 0.0.0.0:*\n", nil)
		if err == nil || !strings.Contains(err.Error(), port) || !strings.Contains(err.Error(), flag) {
			t.Errorf("port %s: want an error naming it and %s, got %v", port, flag, err)
		}
		if _, err := run([]int{18081, 18091}, "LISTEN 0 4096 0.0.0.0:"+port+" 0.0.0.0:*\n", nil); err != nil {
			t.Errorf("port %s is the stack's own, so it is no conflict: %v", port, err)
		}
	}
}

func TestCheckPortsFreeTellsAMissingSSAndReturnsEveryOtherFailure(t *testing.T) {
	s := isolatedSettings()
	said := ""
	check := func(ctx context.Context, err error) error {
		d := Deps{Exec: func(context.Context, string, []string, io.Reader) ([]byte, error) { return nil, err }}
		return checkPortsFree(ctx, d, s, nil, func(format string, a ...any) { said += format })
	}

	if err := check(context.Background(), fmt.Errorf("ss -ltn: %w: ", exec.ErrNotFound)); err != nil || !strings.Contains(said, "could not be checked") {
		t.Errorf("a host without ss is a note, not a refusal: %q, %v", said, err)
	}
	failed := errors.New("ss -ltn: exit status 1: Cannot open netlink socket")
	for name, tc := range map[string]struct {
		ctx context.Context
		err error
	}{
		"a non-zero exit":       {context.Background(), failed},
		"a cancelled context":   {cancelled(), context.Canceled},
		"a deadline that ended": {context.Background(), context.DeadlineExceeded},
	} {
		if err := check(tc.ctx, tc.err); !errors.Is(err, tc.err) {
			t.Errorf("%s: want the error returned, got %v", name, err)
		}
	}
}

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
