package run

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/version"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// resultBody is the body of POST /jobs/{id}/result (docs/contracts/runtime.md). The lists that the contract allows only for a complete run, or
// only for a receipt, are left out when they are empty.
type resultBody struct {
	Status     string       `json:"status"`
	Reason     string       `json:"reason,omitempty"`
	Head       commitRef    `json:"head"`
	Base       *commitRef   `json:"base,omitempty"`
	Executor   executorBody `json:"executor"`
	Sandbox    sandboxBody  `json:"sandbox"`
	StartedAt  string       `json:"startedAt"`
	EndedAt    string       `json:"endedAt"`
	Tests      *[]testRow   `json:"tests,omitempty"`
	Markers    []markerRow  `json:"markers,omitempty"`
	Submodules []string     `json:"submodules,omitempty"`
	Unparsed   []string     `json:"unparsed,omitempty"`
}

type commitRef struct {
	SHA string `json:"sha"`
}

type executorBody struct {
	Version      string `json:"version"`
	BinarySHA256 string `json:"binarySha256"`
}

// sandboxBody names the sandbox that ran, or would have run: the image is the executor's own, pinned in the code, and the runtime was asked
// at the start. The toolchain is what the image said, and is missing from a result that came before the image was asked.
type sandboxBody struct {
	ImageDigest string `json:"imageDigest"`
	Toolchain   string `json:"toolchain,omitempty"`
	Runtime     string `json:"runtime"`
}

// imageDigest is the digest of the pinned image: the part of its reference after the @.
var imageDigest = func() string {
	_, digest, _ := strings.Cut(goprofile.Image, "@")
	return digest
}()

// body is the part of a result that every status has: the commits, the executor and the sandbox, and the times of an attempt that began at
// started and ends now.
func (e *Executor) body(lease *client.Lease, started time.Time, status string) resultBody {
	e.mu.Lock()
	toolchain := e.toolchain
	e.mu.Unlock()
	body := resultBody{
		Status:    status,
		Head:      commitRef{SHA: lease.Head.SHA},
		Executor:  executorBody{Version: e.cfg.Version, BinarySHA256: e.cfg.BinarySHA256},
		Sandbox:   sandboxBody{ImageDigest: imageDigest, Toolchain: toolchain, Runtime: e.runtime},
		StartedAt: stamp(started),
		EndedAt:   stamp(e.cfg.Now()),
	}
	if lease.Base != nil {
		body.Base = &commitRef{SHA: lease.Base.SHA}
	}
	return body
}

// marshalComplete writes the result of a run that completed with what it found.
func (e *Executor) marshalComplete(lease *client.Lease, started time.Time, done completion) ([]byte, error) {
	body := e.body(lease, started, "complete")
	body.Tests = &done.tests
	body.Markers, body.Submodules, body.Unparsed = done.markers, done.submodules, done.unparsed
	return writeResult(body)
}

// marshalUnavailable writes the result of a run that gives up, with the reason.
func (e *Executor) marshalUnavailable(lease *client.Lease, started time.Time, reason string) ([]byte, error) {
	body := e.body(lease, started, "unavailable")
	body.Reason = reason
	return writeResult(body)
}

func writeResult(body resultBody) ([]byte, error) {
	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("write the result: %w", err)
	}
	return out, nil
}

// stamp is a time as the contract has it: RFC 3339 in UTC.
func stamp(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

// probeLimits are the limits of the one container that asks the image for its Go.
var probeLimits = sandbox.Limits{MemoryMiB: 256, CPUs: 1, Pids: 64, TmpfsMiB: 16, KillAfter: time.Minute}

// goVersion is what the pinned image says its Go is (`go env GOVERSION`), asked once for the life of the executor: the toolchain that a
// result reports, and the Go that a go.mod is compared with. It is the image that answers and not a constant, so that the evidence can not
// say a version that the image does not have.
func (e *Executor) goVersion(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.toolchain != "" {
		return e.toolchain, nil
	}
	stdin, err := markerTar()
	if err != nil {
		return "", err
	}
	_, env := goprofile.Command([]goprofile.TestID{{Package: ".", Name: "TestProbe"}}, time.Minute, true)
	out, err := e.cfg.Runtime.Run(ctx, sandbox.Spec{
		Image: goprofile.Image, Env: env, Argv: []string{"go", "env", "GOVERSION"}, Workdir: ".", Limits: probeLimits, Stdin: stdin, StdoutLimit: 1 << 10,
	})
	if err != nil {
		return "", fmt.Errorf("ask the image for its Go: %w", err)
	}
	answer := strings.TrimSpace(string(out.Stdout))
	if out.ExitCode != 0 || out.Truncated || !version.IsValid(answer) || !printable(answer) {
		return "", fmt.Errorf("%w: the image reports no Go version (status %d)", sandbox.ErrUnavailable, out.ExitCode)
	}
	e.toolchain = answer
	return answer, nil
}

// markerTar is a tar of the completion marker alone: the sandbox refuses to start a program without it.
func markerTar() (*bytes.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: sandbox.CompleteMarker, Typeflag: tar.TypeReg, Mode: 0o644}); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}
