package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/postgres"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/river"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// probePoll is how often probe asks for the state of the job and the message.
const probePoll = 200 * time.Millisecond

type probeOptions struct {
	delay   time.Duration
	noWait  bool
	waitID  string
	timeout time.Duration
	// jobID and key are the parts of waitID.
	jobID int64
	key   string
}

// parseProbe reads the flags of `probe`. An error has been reported on stderr with the usage.
func parseProbe(args []string, stderr io.Writer) (probeOptions, error) {
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { _, _ = io.WriteString(stderr, usage) } // a failed write to stderr cannot be reported anywhere else
	var o probeOptions
	flags.DurationVar(&o.delay, "delay", 0, "schedule the probe job after this delay")
	flags.BoolVar(&o.noWait, "no-wait", false, "print the probe id and return")
	flags.StringVar(&o.waitID, "wait", "", "wait for the probe with this id instead of enqueueing one")
	flags.DurationVar(&o.timeout, "timeout", 60*time.Second, "how long to wait")
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	var err error
	switch {
	case flags.NArg() > 0:
		err = fmt.Errorf("unexpected argument %q", flags.Arg(0))
	case o.delay < 0 || o.timeout <= 0:
		err = errors.New("--delay must not be negative and --timeout must be positive")
	case o.waitID != "" && (o.delay != 0 || o.noWait):
		err = errors.New("--wait cannot be combined with --delay or --no-wait")
	case o.waitID != "":
		o.jobID, o.key, err = parseProbeID(o.waitID)
	}
	if err != nil {
		reportf(stderr, "%v\n", err)
		_, _ = io.WriteString(stderr, usage) // a failed write to stderr cannot be reported anywhere else
	}
	return o, err
}

// probeID is the opaque id that probe prints: the job id and the outbox key.
func probeID(jobID int64, key string) string { return strconv.FormatInt(jobID, 10) + ":" + key }

func parseProbeID(id string) (int64, string, error) {
	job, key, found := strings.Cut(id, ":")
	jobID, err := strconv.ParseInt(job, 10, 64)
	if !found || err != nil || key == "" {
		return 0, "", fmt.Errorf("%q is not a probe id", id)
	}
	return jobID, key, nil
}

// lines prints JSON lines and remembers the first failure to write them.
type lines struct {
	encoder *json.Encoder
	err     error
}

func (l *lines) print(v any) {
	if l.err == nil {
		l.err = l.encoder.Encode(v)
	}
}

// runProbe enqueues a probe (or takes an earlier one), prints its JSON lines
// and waits for it. Exit codes: 0 finished, 2 timed out or reached a failed
// terminal state, 1 any other error.
func runProbe(ctx context.Context, cfg config.Config, o probeOptions, stdout, stderr io.Writer) int {
	pool, store, err := connect(ctx, cfg)
	if err != nil {
		reportf(stderr, "%v\n", err)
		return 1
	}
	defer pool.Close()
	out := &lines{encoder: json.NewEncoder(stdout)}
	started := time.Now()
	if o.waitID == "" {
		o.key = "probe-" + rand.Text()
		job := governance.Job{Kind: river.ProbeKind, Args: []byte(`{}`)}
		if o.delay > 0 {
			job.ScheduledAt = time.Now().Add(o.delay)
		}
		if o.jobID, err = store.EnqueueSystem(ctx, job, governance.OutboxMessage{Key: o.key, Kind: river.ProbeKind, Payload: []byte(`{}`)}); err != nil {
			reportf(stderr, "%v\n", err)
			return 1
		}
		out.print(map[string]any{"event": "enqueued", "probeId": probeID(o.jobID, o.key)})
	}
	code := 0
	if !o.noWait {
		code = waitForProbe(ctx, store, o, started, out, stderr)
	}
	if out.err != nil {
		reportf(stderr, "write output: %v\n", out.err)
		return 1
	}
	return code
}

// waitForProbe polls until the job is completed and the message delivered,
// prints the final line with the states it saw, and returns the exit code.
func waitForProbe(ctx context.Context, store *postgres.Store, o probeOptions, started time.Time, out *lines, stderr io.Writer) int {
	waitCtx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	ticker := time.NewTicker(probePoll)
	defer ticker.Stop()
	var seen postgres.SystemStatus
	result := func() {
		out.print(map[string]any{"event": "result", "probeId": probeID(o.jobID, o.key), "job": seen.JobState, "outbox": seen.OutboxState, "elapsedMs": time.Since(started).Milliseconds()})
	}
	for {
		status, err := store.SystemStatus(waitCtx, o.jobID, o.key)
		switch {
		case err == nil:
			seen = status
			if seen.JobState == "completed" && seen.OutboxState == postgres.OutboxDelivered {
				result()
				return 0
			}
			if seen.JobState == "discarded" || seen.JobState == "cancelled" || seen.OutboxState == postgres.OutboxFailed {
				result()
				reportf(stderr, "the probe failed: job %s, message %s\n", seen.JobState, seen.OutboxState)
				return 2
			}
		case !errors.Is(err, context.DeadlineExceeded):
			reportf(stderr, "%v\n", err)
			return 1
		}
		select {
		case <-ticker.C:
		case <-waitCtx.Done():
			result()
			reportf(stderr, "timed out after %v waiting for the probe: job %q, message %q\n", o.timeout, seen.JobState, seen.OutboxState)
			return 2
		}
	}
}
