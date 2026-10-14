package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
)

// blockingStatus blocks until its context is done and then fails the way pgx
// does when the deadline passes while it writes: with an error that wraps
// os.ErrDeadlineExceeded, not context.DeadlineExceeded.
type blockingStatus struct{}

func (blockingStatus) SystemStatus(ctx context.Context, _ int64, _ string) (postgres.SystemStatus, error) {
	<-ctx.Done()
	return postgres.SystemStatus{}, fmt.Errorf("read job state: %w", &net.OpError{Op: "write", Net: "tcp", Err: os.ErrDeadlineExceeded})
}

func TestProbeReportsItsOwnExpiredDeadlineAsATimeoutWhateverTheDriverReturns(t *testing.T) {
	var stdout, stderr bytes.Buffer
	o := probeOptions{timeout: 10 * time.Millisecond, jobID: 7, key: "probe-k"}
	code := waitForProbe(context.Background(), blockingStatus{}, o, time.Now(), &lines{encoder: json.NewEncoder(&stdout)}, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "timed out") {
		t.Fatalf("waitForProbe exited %d, stderr %q; want 2 and a timeout message", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"event":"result"`) || !strings.Contains(stdout.String(), `"probeId":"7:probe-k"`) {
		t.Fatalf("waitForProbe printed %q; want a final line with the probe id and the states it saw", stdout.String())
	}
}
