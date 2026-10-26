# S4: sandbox spike

- **Date:** 2026-10-26
- **Phase:** [M2.1](../plan/phases/M2.1.md), task S0
- **Decides input for:** ADR 0028 (task A0), the executor (T5) and the install (T9). The design it tests is the "Sandbox" section of the M2.1 design note.

This spike ran the Go sandbox on real runtimes, before the executor and its install are built: Docker Desktop on Windows, and rootless Podman under two accounts in a throwaway AlmaLinux 9 WSL distro. Everything below is **measured** unless a sentence says it is documentation or inference. The helper scripts are in [s4-sandbox/](s4-sandbox/); each command quoted here is one of them or a one-liner.

**Result.** The sandbox works as designed, on both runtimes, with the same flags. Six things in the design note need to change or be added, in the order of how much they matter:

1. A resource limit is read as a test result. When the OOM killer takes the compiler, `go test -json` reports `build failed` for that package and ends normally, so the suite looks complete. Under the receipt rules that is FAILS on the head or NEW API on the base. Only signals from outside the sandbox can tell: the runtime's `OOMKilled` state, the container's exit status 137 when `go test` is its process, and the executor's own timeout kill. Text in the build output cannot, because candidate code writes it. The run is then `unavailable` with reason `resource-limit`, and a candidate can cause that on purpose (section 2.3).
2. `--memory` needs `--memory-swap` equal to it, and 3 GiB, not 1 GiB: with swap hidden behind the default, the suite passed at 1 GiB; with swap pinned, it needs between 1.5 and 2 GiB, so 3 GiB gives margin (section 2.2).
3. On Docker Desktop, "never on the `suiteward` network" does not keep a container away from the service: any bridge network reaches host loopback, and a published port is reachable by container IP from other networks. On rootless Podman it does hold (section 3, 4.4). A networked install step is therefore the only place the sandbox touches anything, and the owner decided on 2026-10-26 to remove it: the sandbox has no network at all, and the executor prefetches the modules that the candidate's `go.sum` names. That is as strong as a networked install, not stronger (section 3.3).
4. The same suite gives different SKIP sets by image and by runtime (section 1.3). Pin one image per profile, run as `nobody` (65534), and record the runtime.
5. On the isolated host rootless Podman works only after a systemd drop-in that delegates the `cpu` controller; the install must write it (section 4.1).
6. For the isolated tier, the executor should be a native process of `suiteward-exec`, not a container with its socket (section 4.5). For both co-located runtimes, a host process (section 5).

## Environment

| | Docker side | Podman side |
| --- | --- | --- |
| Runtime | Docker Desktop, engine 29.9.2 (runc, cgroup v2, context `desktop-linux`); the machine has 8 logical CPUs, and the WSL VM is capped at 24 GB with 6 GB of swap | Podman 5.8.2 (crun 1.27, netavark 1.17.2, passt 20251210) on AlmaLinux 9.8, rootless, in a throwaway WSL distro `m21s0-alma` with systemd, on the same VM and kernel (6.6.114.1-microsoft-standard-WSL2) |
| Images | `golang:1.27.3-alpine`, digest `sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd` (382 MB; the pin of `deploy/Containerfile`), and `golang:1.27.3-bookworm`, digest `sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5` (1.25 GB; resolved from the tag on 2026-10-26). Both are in [image.sh](s4-sandbox/image.sh) | the alpine pin only |
| Targets | The small module ([smallmod.sh](s4-sandbox/smallmod.sh): one dependency, five tests: three pass, one fails on purpose, one skips) and this repository at `27e2d84c`, taken with `git archive`: 37 packages with tests, 8161 or more passing tests counting subtests | the same two |

The Windows Podman machine was never addressed. Only the Docker Desktop engine and the `m21s0-alma` distro were used, and both are gone (the end of this document shows the state).

## 1. The sandbox run on Docker Desktop

[sandbox.sh](s4-sandbox/sandbox.sh) holds the flags. The module's files arrive as a tar on the container's standard input and are extracted into the tmpfs, so nothing is bind-mounted (a bind mount would depend on host paths, which do not exist in a Podman machine, and on SELinux labels on a real host). In the product that tar is built from the verified tree: the executor checks every archive entry against the tree's blob SHA-1 in-stream (the design note's source-integrity rule) and only then writes it to the container's standard input, so nothing of the tree touches a host filesystem. The spike's helper tars a directory or `git archive` output, without that check.

```sh
. ./image.sh; export IMAGE=$ALPINE                       # the alpine pin; BOOKWORM is the Debian image
sh sandbox.sh prep                                   # a named volume for the module cache, handed to the sandbox user once
git archive HEAD | sh sandbox.sh install             # networked: go mod download into the volume, nothing else
git archive HEAD | sh sandbox.sh test ./...          # offline: go test -json -count=1 -shuffle=on ./...
```

The sandbox run uses `--rm -i --network=none --read-only --tmpfs /work:rw,exec,nosuid,nodev,size=1g --cap-drop=ALL --security-opt no-new-privileges --pids-limit 512 --memory 3g --memory-swap 3g --cpus 4 --user 65534:65534` (the defaults of the script, which uses `--rm` for convenience: the executor must not, section 2.3; the measurements below used other values where they say so: 4 GiB, 2 GiB tmpfs, 1024 processes and uid 65532 at first), the module cache volume mounted read-only at `/gomod`, and `HOME`, `GOCACHE`, `GOPATH` and `TMPDIR` under `/work`, `GOMODCACHE=/gomod`, `GOPROXY=off`, `GOTOOLCHAIN=local`, `GOWORK=off`, `GOFLAGS=-mod=readonly`, `CGO_ENABLED=0` (1 for `-race`). The install step is the same without `--network=none`, with the volume writable, `GOPROXY=https://proxy.golang.org` and `GOVCS=*:off`. The helper hands it the whole archive, but `go mod download` reads only `go.mod` and `go.sum` and ran no repository code; the real step should pass just those two files.

### 1.1 What breaks, and the fix

[breaks.sh](s4-sandbox/breaks.sh) leaves out one setting at a time. All of these were observed on the small module.

| Left out | What happens | Fix |
| --- | --- | --- |
| `exec` on the tmpfs (Docker's default is `noexec`) | `fork/exec /work/tmp/go-build…/b001/small.test: permission denied` | `--tmpfs /work:rw,exec,nosuid,nodev,size=…` |
| `HOME` and `GOCACHE` | `failed to initialize build cache at /.cache/go-build: mkdir /.cache: read-only file system` | both under `/work` |
| `TMPDIR` | `go: creating work dir: mkdir /tmp/go-build…: read-only file system`; `t.TempDir()` needs it too | `/work/tmp` |
| `GOMODCACHE` | `module lookup disabled by GOPROXY=off` (the cache is empty at the default path) | point it at the volume |
| `GOPATH` | harmless offline; the install step, when `go.sum` lacks an entry, fails with `open /go/pkg/sumdb/sum.golang.org/latest: no such file or directory` | `GOPATH=/work/go` |
| the module cache volume owned by the sandbox user | the install step fails with `go: writing go.mod cache: mkdir /gomod/cache: permission denied` | `sandbox.sh prep` hands the volume to the user once, with a helper container that has only `CAP_CHOWN` |
| a dependency missing from `go.sum` | `missing go.sum entry for module providing package …` (the package fails to build) | none needed: it is the safe outcome |
| a wrong hash in `go.sum` (met by accident, in the install step) | `SECURITY ERROR … checksum mismatch`; nothing is stored | none needed: `go.sum` refuses a damaged download (not a hostile candidate, 3.3) |
| `go.mod` that needs a newer `go` than the image | `go: go.mod requires go >= 1.99.0 (running go 1.27.3; GOTOOLCHAIN=local)`; a `toolchain go1.99.0` line alone is ignored | none: maps to `unavailable` (toolchain mismatch) |
| `GOWORK=off` | with a candidate `go.work` and `GOWORK` unset, `go: cannot load module ../nonexistent listed in go.work file`; with `GOWORK=off` the test ran | keep `GOWORK=off` |

The alpine image cannot run `-race`: `cgo: C compiler "gcc" not found` (it has no `gcc`, and no `git`).

### 1.2 What the run produced

The small module: 3 pass, 1 fail, 1 skip, as written, and a normal JSON stream (the failing test also ends its package with `fail`). This repository: all tests pass, offline, with the module cache of 108 MB that the install step fetched in 8.6 to 12 s.

### 1.3 The result depends on the image and the runtime

The same suite, with the sandbox as above:

| Run | pass | skip | What differs |
| --- | --- | --- | --- |
| Docker, alpine, `--user 65532:65532` | 8161 | 5 | `TestOSUserSIDNamesTheCurrentUser` skips: `id: unknown ID 65532`, because the image has no such `/etc/passwd` entry |
| rootless Podman, alpine, `--user 65532:65532` | 8162 | 4 | the same test passes: Podman writes `65532:*:65532:65532:container user:/go:/bin/sh` into the container's `/etc/passwd` |
| Docker or Podman, alpine, `--user 65534:65534` (`nobody`) | 8162 | 4 | identical on both runtimes (the same four skips) |
| Docker, bookworm, `--user 65534:65534` | 8163 | 3 | `TestTerminalScriptClearsTheScreenWhenInterruptedAtItsPrompt` also passes: it needs a tool the Debian image has and alpine lacks |

So a profile has to fix the image (by digest), the user (use `nobody`, 65534, which both images and both runtimes already know, so no runtime invents an entry; `sandbox.sh` now defaults to it), and record the runtime in the evidence binding. A SKIP that differs between the head side and the base side only because the two ran on different images is a false signal.

## 2. Cost

All figures are `go test -json -count=1 -shuffle=on ./...` in the sandbox with a cold build cache on a tmpfs, 4 CPUs, measured from inside the sandbox's own cgroup ([bench.sh](s4-sandbox/bench.sh); timings are from before the swap pin and from after it, which does not change them while memory is ample). The host was otherwise lightly used; one run took 55 s while it was busy, and is left out of the medians.

### 2.1 Time and CPU

| Target | Image | Race | Runs | Wall seconds (median, range) | CPU seconds | Peak non-reclaimable memory | tmpfs used |
| --- | --- | --- | --- | --- | --- | --- | --- |
| small module | alpine | no | 6 | 4.0 (3.9 to 4.4) | 14 to 15 | 0.31 to 0.40 GB | 72 MB |
| small module | bookworm | no | 3 | 4.6 (4.3 to 6.1) | 14 to 22 | 0.3 to 0.4 GB | 72 MB |
| small module | bookworm | yes | 3 | 6.2 (6.1 to 7.1) | 17 to 20 | 0.3 to 0.4 GB | 79 MB |
| this repository | alpine | no | 11 | 32.6 (29.2 to 35.5) | 78 to 103 | 1.4 to 1.7 GB | 352 MB |
| this repository | bookworm | no | 4 | 31.4 (31.1 to 31.6) | 85 to 88 | 1.5 to 1.8 GB | 352 MB |
| this repository | bookworm | yes | 13 | 53.4 (50.2 to 58.8; two runs took 62 and 77) | 108 to 142 | 1.6 to 1.8 GB | 392 MB |

Scaling with the CPU limit (alpine, this repository, no race): 1 CPU 84.8 s, 2 CPUs 47.0 s, 4 CPUs about 32 s, 8 CPUs 23.9 s. The CPU limit is hit (the cgroup reports throttling) at 4 CPUs; the run needs about 80 to 100 CPU seconds in total. `-race` costs 1.7 times the wall time and 1.4 times the CPU time, and needs the Debian image (1.25 GB against 382 MB, and a `gcc`).

A persistent, writable `GOCACHE` made the run 12.8 s instead of 32.8 s (343 MB of cache). That is the price of the isolation below, and it is **not** recommended for M2.1: a cache shared across runs lets the tests of one head write entries that a later head, or the base side, reuses.

The install step on this repository took 8.6 to 12 s (108 MB downloaded, `go.sum` of 110 lines); for the small module about 3 s.

### 2.2 Memory

Docker's default sets the swap to the size of the memory, and this VM has 6 GB of swap, so a run with only `--memory` can pass at a limit that a host without swap would not. These floors are with `--memory-swap` equal to `--memory`:

| `--memory` | alpine, no race, this repository | bookworm, race, this repository |
| --- | --- | --- |
| 3 GiB | passes (32.6 s) | passes (53.4 s) |
| 2 GiB | passes, 3 of 3 (29 to 33 s) | passes, 2 of 2 (51 to 53 s) |
| 1.5 GiB | OOM kill, 2 of 2 (6111 and 5797 tests reported out of 8161) | OOM kill, 2 of 2 (5312 and 6110) |
| 1.25 GiB | OOM kill (4238) | |
| 1 GiB | OOM kill (4103) | OOM kill (4145) |
| 768 MiB | OOM kill (3816) | OOM kill (2914, exit status 137) |

Without the swap pin the same suite passed at 896 MiB (no race) and at 1 GiB (race) on this VM: do not carry those numbers over. `GOMEMLIMIT=600MiB` at 1 GiB did not prevent the kills. The tmpfs counts against the limit (it is `shmem` in the cgroup): 352 MB of the 1.4 to 1.7 GB for this repository, 457 MB when the modules are extracted into it (section 3.2).

**Recommended limits for the Go profile:** `--memory 3g --memory-swap 3g --cpus 4 --pids-limit 512` (the peak was 43 to 55 processes) and `--tmpfs /work:rw,exec,nosuid,nodev,size=1g`. A bigger host can give more CPUs for a faster run; a limit under about 2 GiB turns a suite of this size into false failures.

**Isolated host size** (inference from these numbers, not a test on that host): one sandbox at a time needs 4 vCPUs and 3 GiB for the sandbox, besides the service (256 MiB by its quadlet), PostgreSQL and the system; 4 vCPUs and 8 GiB of memory is the floor, and 20 GB of disk is enough for the image, a module cache per distinct `go.sum` and the logs. A receipt runs the head and the base, about 65 s for this repository on 4 CPUs (sequentially) plus the fetch. Running them in parallel needs 8 vCPUs and 12 to 16 GiB. The isolated host is probably arm64: memory and time there are not measured.

### 2.3 What a limit looks like from outside

Killing the compiler with the cgroup OOM killer (the 768 MiB runs above):

- `go test -json` reports `compile: signal: killed` as `build-output`, then `build-fail`, then `FAIL … [build failed]` for the package, and the stream ends normally with the other packages' results. The run's exit status is 1, and the count of tests falls (8161 to 4103). Under the receipt rules this is FAILS (head) or NEW API (base).
- Text in the output cannot be the signal: candidate code writes that output, and can print `signal: killed` or kill itself on purpose.
- The signal from outside the sandbox works on both runtimes ([oom-signal.sh](s4-sandbox/oom-signal.sh), with a memory hog in a 64 MiB container): `inspect` says `.State.OOMKilled=true` both when only a child of the container was killed (exit status 0) and when its first process was (137). Docker also emits an `oom` event. Podman's event stream showed only `died`. The container must exist when it is inspected, so **do not use `--rm`**: inspect, then remove.
- The container's exit status is go's own only when `go test` is the container's process (the executor starts it with `exec go test …` after the setup; the spike's `sandbox.sh` keeps a wrapper shell for its metrics, so its status is the wrapper's, and this was not measured with `exec`). Then 137 means that go itself was killed. A killed child leaves go alive and exiting 1, which only the `OOMKilled` state shows.
- The `oom_kill` counter that `sandbox.sh` prints is read by the sandbox's own shell, so candidate code could change it. Use it as a diagnostic, not as the signal.

**Rule for T5:** only signals from outside the sandbox count: the runtime's `inspect` `.State.OOMKilled`, the container's exit status 137 (with `go test` as its process), and the executor's own timeout kill. Never text in the build output. Any of them makes the run `unavailable` with reason `resource-limit`: deterministic, final for the job. A candidate can cause it with a memory hog, so the owner should read the cause before unblocking.

Timeouts: on Docker, a `docker run` client killed with SIGKILL left its container running. SIGTERM to the client is passed on but a first process such as `sleep` or `sh` ignores it. `docker kill <name>` took 0.26 s and `--rm` then removed it. On Podman, `podman run --timeout 3` ended the container and removed it, but the command's exit status was 0. So T5 keeps its own deadline, labels every sandbox, kills it by name, and sweeps leftovers at start.

## 3. Isolation, on Docker Desktop

A listener on the Windows host's `127.0.0.1:18081` ([listen.ps1](s4-sandbox/listen.ps1)) stands in for the SuiteWard service; a container on a network `m21s0-svc` publishes `127.0.0.1:18091` ([listener.sh](s4-sandbox/listener.sh)) the way the service does. [probe.sh](s4-sandbox/probe.sh) runs inside a container and tries each target for 3 s.

| From | `host.docker.internal:18081` (host loopback) | `host.docker.internal:18091` (a published port) | the bridge gateway | a container on another network with a published port, by IP | the same without a published port | `169.254.169.254:80` | `proxy.golang.org:443` |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `--network=none` (the sandbox) | no name, no route | no | no | no | no | no | no |
| default bridge | **reached** (the listener logs the peer as 127.0.0.1) | **reached** | blocked | **reached** | blocked | no answer | reached |
| user-defined bridge `m21s0-inst` (the install step) | **reached** | **reached** | blocked | **reached** | blocked | no answer | reached |
| `--internal` network | no name | no | no | no | no | no | no |

In the offline run nothing is reachable: the container has only `lo`. In the networked install step, a bridge network reaches the host's loopback services (`host.docker.internal` is documented to resolve to the host; the page does not say whether loopback-only services are reachable, and they are) and the published port of a container on another network. Only an unpublished port is protected by the network separation. A sandbox on the `suiteward` network, or on any bridge network, would therefore reach the service (and its MCP port, and the database if it were published).

`169.254.169.254` gave no answer here because Docker Desktop has no metadata service; what it does on a cloud host was **not measured**.

### 3.1 Ways to restrict a networked install step (alternatives measured; the owner chose the third)

1. **A plain network:** not acceptable on Docker Desktop, per the table.
2. **An `--internal` network with an allow-list mirror.** An internal network has no route out and no `host.docker.internal`. A container on it and on a normal network forwards `GET` and `HEAD` to `proxy.golang.org` and follows its redirect for zips to `storage.googleapis.com`, and nothing else (a first version that did not follow the redirect failed: `proxy.golang.org` answers a zip with a redirect to a signed URL on that other host). The install through it took 3.9 s and produced a module cache identical to the direct one (`diff -r` empty, 108 MB). From the install container, only the mirror answered. The whole program:

```go
// A forward-only mirror: GET/HEAD to proxy.golang.org (go.mod files, zips, the checksum database) and nothing else.
package main

import (
	"errors"
	"io"
	"log"
	"net/http"
)

func main() {
	c := &http.Client{CheckRedirect: func(r *http.Request, _ []*http.Request) error {
		if r.URL.Scheme != "https" || r.URL.Host != "storage.googleapis.com" {
			return errors.New("redirect to a host that is not allowed")
		}
		return nil
	}}
	log.Fatal(http.ListenAndServe(":8081", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, "https://proxy.golang.org"+r.URL.RequestURI(), nil)
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		resp, err := c.Do(req)
		if err != nil {
			http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})))
}
```

   The install container is then started with `GOPROXY=http://<mirror>:8081`. It needs a second container, a built binary and a network pair per run or per install.
3. **No network in the sandbox at all.** The executor fetches the files itself and the sandbox installs from a `file://` proxy. This is what the owner chose (3.3).

### 3.2 Network-less install from a `file://` proxy (the owner's decision: no network in the sandbox)

`go mod download` in a `--network=none` container with `GOPROXY=file:///fp` (a read-only volume laid out as a module proxy, loaded by a helper container with no network; the product puts the files in the stdin tar instead, 3.3) and the default `GOSUMDB` worked: 1.9 s, and the module cache was identical to the direct one. Every file is checked by `go` against `go.sum`: a wrong `go.mod` hash was refused (`SECURITY ERROR`), and so was a file that was not a zip (my first fetch saved the redirect page of a zip instead of the zip: `go: zip: not a valid zip file`).

[fetch-proxy.sh](s4-sandbox/fetch-proxy.sh) is a **prototype** of the executor-side fetch, with shortcuts that T5 must not copy (3.3): for each line of `go.sum` it downloads `<module>/@v/<version>.info`, `.mod` or `.zip` from `proxy.golang.org` with plain HTTPS GETs (following redirects, 8 at a time). For this repository that is 170 files and 73 MB; it took 13 to 17 s. Of the five runs that followed redirects, four ended with 0 failures and 170 files each (two compared byte for byte, identical), and one failed on every request for a reason I did not find, so a fetch error is an infrastructure failure that is counted and retried. With that proxy the sandbox needs no install step and no cache volume at all: `FP=<volume> sh sandbox.sh test ./...` mounts it read-only, points `GOPROXY` at it and builds `GOMODCACHE` in the tmpfs. Three runs took 29.6, 30.1 and 32.4 s (no slower than with a prepared cache), and the tmpfs held 457 MB. (Measured on the alpine image before the swap pin; the extra 105 MB counts against the memory limit.)

### 3.3 The owner's decision: no network in the sandbox at all

On 2026-10-26 the owner chose this over a networked install step. The executor prefetches the modules that the candidate's `go.sum` names, from `proxy.golang.org` only, and the sandbox gets no network at all, for the test run and for the module install alike. It removes the install container, the volume ownership step, the mirror, the metadata question and the Docker Desktop reachability problem in one change. It replaces the design note's earlier "network only for the dependency install". What the spike found about doing it:

- **Validate before use.** A `go.sum` line is candidate text, and its module path and version would become a URL and a file name. T5 must check each with `golang.org/x/mod/module` (`CheckPath`, then `EscapePath` and `EscapeVersion`) and `semver` before they are used, and refuse the job when one fails: a deterministic `install-failed`. The prototype's loose character filter only stands for this.
- **Bound it:** the number of entries and the bytes fetched (this repository: 170 files, 73 MB), the time, and the tmpfs they take.
- **How the files reach the sandbox:** inside the same stdin tar as the source, never on a host filesystem. The spike used a read-only volume for convenience. In the sandbox `go` runs with `GOPROXY=file:///…`, `GOFLAGS=-mod=readonly` and `GOSUMDB=off`, and builds the module cache in the tmpfs: 457 MB of tmpfs in all for this repository (105 MB more than the source and the build cache); the zips in the tar add their own size (not measured). All of it counts against the memory limit.
- **What the bytes are worth.** `go` checks every file against `go.sum`, and that `go.sum` is the candidate's. TLS to the proxy protects the download on the way; the candidate can still name any module the proxy serves, with its hash. The result is equivalent to a networked install, not stronger. A wrong hash is refused (`SECURITY ERROR`, 1.1) and so is a file that is not a zip (3.2), but that protects against a damaged download, not against a hostile candidate.
- **`replace`.** One to a directory inside the tree needs no fetch: the files come from the source tar. One outside the tree fails the run (deterministic). A module the proxy does not serve (private) fails it too.
- **Egress.** The executor's own network is now the only one. Its HTTP client allows `proxy.golang.org` and the redirect host of its zips, `storage.googleapis.com`, and nothing else (an `http.Client` with `CheckRedirect`, as in the mirror of 3.1). A host-level egress allow-list is recommended on top, but **not built from IP addresses**: the addresses of Google's front ends rotate. In 4.6 the name `proxy.golang.org` resolved to 142.250.219.49 in one run and to 142.251.132.241 in another, an hour apart, so a rule that names an address would have broken. The firewall rule of 4.6 therefore rejects the metadata addresses by destination, which do not change, and leaves the proxy to the client's allow-list.
- **The prototype's shortcuts, which T5 must not copy.** It uses `curl -L`, which follows a redirect to any host, and `|| echo FAILED`, which prints and carries on, so a failed or partial download would not stop the job. Its validation filter prints "refused" and calls `exit`, but in the middle of a pipeline without `pipefail` that drops the refused lines and the script still exits 0. It also writes to a host directory and has no limit on the number of files or the bytes. T5 fails the job on any fetch error (an infrastructure failure, retried with backoff) and refuses a redirect to any host but `storage.googleapis.com`.
- The file set is a superset of what `go mod download` fetches: 73 MB for the 170 files of this repository's `go.sum`, against 30 MB in the `cache/download` that `go mod download` left (I did not find out which entries account for the difference).
- Whether the executor may keep the downloaded files between jobs is open: they are not the composed tree and the executor never unpacks them, but the owner's rule says nothing of the tree touches a host filesystem. `go` verifies every file against the candidate's `go.sum` whatever its origin. Not decided here.

Alternatives measured and not chosen: a mirror on an `--internal` network (3.1, option 2) and, on rootless Podman, a plain user-defined network (4.4). They stay as evidence.

## 4. Rootless Podman on AlmaLinux 9 (a WSL distro)

[alma-setup.sh](s4-sandbox/alma-setup.sh) builds it from a fresh distro: Podman 5.8.2, two accounts, `suiteward` (uid 2000; WSL reserves 1000 for WSLg and its user manager failed there with `219/CGROUP`) and `suiteward-exec` (1001), both with linger. Their `/etc/subuid` ranges are 100000 to 165535 and 165536 to 231071: disjoint. A helper, [as.sh](s4-sandbox/as.sh), runs a command as an account inside its systemd session environment. This script was run on a freshly created distro, twice (the second run after fixing an ordering problem in it); the isolation checks of 4.3 were repeated on that distro.

### 4.1 The `cpu` controller is not delegated

On EL9 the user manager gets only `memory` and `pids`, and `podman run --cpus 2` fails: `Error: OCI runtime error: crun: controller 'cpu' is not available under /sys/fs/cgroup/user.slice/user-1001.slice/user@1001.service/…`. A drop-in `/etc/systemd/system/user@.service.d/delegate.conf` with `Delegate=cpu cpuset io memory pids` fixes it, for a manager that starts after the drop-in exists (the script writes it before the accounts, so no restart is needed; on a host with running accounts, restarting a user manager stops that account's containers, so T9's `install host` has to write it before the stack starts, or ask for a reboot). After it, `--cpus 2 --memory 1g --memory-swap 1g --pids-limit 100` gave `cpu.max 200000 100000`, `memory.max 1073741824`, `memory.swap.max 0`, `pids.max 100`.

### 4.2 The same flags work

`sandbox.sh` ran unchanged under `RT=podman` as `suiteward-exec`: `prep` (the `CAP_CHOWN` helper chowns through the user namespace), `install` on a user-defined network, and `test` (small 3.8 to 4.5 s; this repository 30.9 and 32.7 s, 8162 passing tests; with uid 65534, the same results as Docker alpine, section 1.3). The only warning is a WSL one: `"/" is not a shared mount`.

### 4.3 What the second account can reach of the first

[isolation-users.sh](s4-sandbox/isolation-users.sh): `suiteward` owns a network, a secret, a volume with a file in it and a container that publishes `127.0.0.1:18091`. As `suiteward-exec`:

| Attempt | Result |
| --- | --- |
| `podman ps -a`, `volume ls`, `secret ls`, `network ls` | none of `suiteward`'s objects; only its own default network |
| `podman exec m21s0-svc id` | `no container with name or ID "m21s0-svc" found` |
| `podman volume inspect m21s0-data`, `podman secret inspect --showsecret m21s0-secret` | `no such volume`, `no secret with name or id` |
| `ls /home/suiteward/.local/share/containers`, `ls /home/suiteward` | `Permission denied` (homes are 0700) |
| `ls /run/user/2000`; `podman --remote --url unix:///run/user/2000/podman/podman.sock ps` | `Permission denied`; `connect: permission denied` |
| `/proc/<pid>/environ` and `/proc/<pid>/root` of the container's process | `Permission denied` |
| `ps -eo user,pid,args` | **sees** the container's processes and their command lines (`conmon … -c <id> …`, `nc -lk -p 8080 -e /tmp/h.sh`): never put a secret in an argument |
| a native process connecting to `127.0.0.1:18091` | **reached** (the loopback is shared by all accounts) |

`mount -o remount,hidepid=2 /proc`, as root in the distro, took the processes of `suiteward` out of `suiteward-exec`'s view (5 listed to 0), and was reverted at once. Caveats before T9 uses it (documentation and inference; only the remount was tried): a remount does not survive a reboot, so it has to be made persistent (an `/etc/fstab` entry or a systemd mount drop-in for `/proc`); it has to be tested on the real host first, because it changes what every non-root program sees, `systemd-logind` and other system services included; and it has a group exemption (`gid=<group>`, a group those services belong to) that has to be set so that they keep working. The design note allows `hidepid` only if made persistent and tested. The rule of never putting a secret in an argument holds without it.

### 4.4 What a container of `suiteward-exec` reaches, per network mode

[probe-podman.sh](s4-sandbox/probe-podman.sh), with a listener on the distro's `127.0.0.1:18081` and `suiteward`'s published `127.0.0.1:18091` (`host` below means the loopback of the distro):

| Mode | host loopback, `18081` and `18091` | `suiteward`'s container by its own IP | internet |
| --- | --- | --- | --- |
| `--network=none` (the sandbox) | no | no | no |
| default (pasta) | **no**, by `host.containers.internal` (169.254.1.2), by the gateway, or on its own loopback | no | yes |
| `--network=slirp4netns` | no | no | yes |
| user-defined netavark network (the install step) | no | no | yes |
| `--network=slirp4netns:allow_host_loopback=true` | **yes**, through `10.0.2.2` | no | yes |
| `--network=pasta:--map-gw` | **yes**, through the gateway | no | yes |
| `--network=pasta:-T,18091` | only `18091`, from the container's own `127.0.0.1` | no | yes |
| `--network=host` | **yes** | no | yes |

So on rootless Podman, the default modes and a user-defined network (what the install step would use) reach the internet and neither the host's loopback nor another account's containers. Four opt-ins open the host loopback; `pasta:-T,<port>` opens exactly one port, which is how a container-shaped executor would reach the service (and the Podman documentation says the same about `-T`; page read on 2026-10-26). Different accounts' networks are different namespaces (both used `10.89.0.0/24` without conflict).

### 4.5 The executor's shape on the isolated tier

[executor-shapes.sh](s4-sandbox/executor-shapes.sh) tests both. The sandbox is a sibling container in both; its flags are the same.

**(a) A native process of `suiteward-exec` calling `podman run`.** It reaches the service at `127.0.0.1:<port>` directly (measured: `127.0.0.1:18091` answers from a native process of the other account). No socket, no extra image, and the binary can sit root-owned in a directory `suiteward-exec` cannot write, which is what `doctor` pins (the design, not tested here).

**(b) A container of `suiteward-exec` with that account's own socket mounted.** The user socket (`systemctl --user enable --now podman.socket`) is `srw-rw---- suiteward-exec` in a 0700 directory. The container needs `podman-remote` (the spike built [a small image](s4-sandbox/executor.Containerfile) on `almalinux:9`). It ran the sandbox through the socket as a sibling container (small module, 5.5 s against 4.4 s native), with `-i` and the tar on standard input (so the remote client carries stdin). It reached the service with `--network=pasta:-T,18091` (the container's `127.0.0.1:18091`). It has to run as the container's root (that is the account on the host): a non-root user inside (uid and gid 65532) gets `connect: permission denied` on the socket; a container user whose gid maps to the account's own group was not tried. And the socket is the account's whole authority: a privileged sibling that mounts `/home` read the account's own home (`.config`, `.local`) and was refused `suiteward`'s. That is no more than shape (a) has, but a container around it adds nothing against a compromised executor.

**SELinux.** WSL's kernel is built with `CONFIG_SECURITY_SELINUX=y` but no policy is loaded: `getenforce` says `Disabled`, and `container-selinux` was not even installed by the dependency resolution. So **nothing about labels was measured**. What must be re-checked on the real host (enforcing): (1) sandbox and executor containers run as `container_t`; this design uses no bind mount, so no `:z`/`:Z` relabelling is needed, and that should stay true; (2) shape (b) would need `--security-opt label=disable` (or a policy) to let a container connect to the socket; (3) pasta and netavark under enforcing; (4) the quadlet units of the executor.

**Lifecycle.** A systemd user unit per shape (a plain unit for (a), a quadlet `.container` for (b)): with linger and nobody logged in, the user managers are active (`loginctl list-users` shows both `lingering`), enabled units start with them, and a `kill -s KILL` of each main process was followed by `Restart=always` (`NRestarts=1`, active again). On WSL, a stopped distro with running containers came back with the account's manager failed (`219/CGROUP`: processes of the old manager lingered in the VM's cgroup tree); [wsl-fix-user-cgroup.sh](s4-sandbox/wsl-fix-user-cgroup.sh) cleared it. That is a WSL artifact and says nothing about a reboot of the real host.

**Recommendation for the isolated tier: (a).** One native process of `suiteward-exec`, started by a systemd user unit with linger, pinned by the binary's SHA-256, calling `podman run` with the sandbox flags. It has the same power as (b) with fewer parts, no socket, no `podman-remote` in the image, no `-T` mapping, no SELinux exception. The install writes the delegation drop-in (4.1), sets `hidepid=2` only if made persistent and tested (4.3), and creates the firewall rules (4.6).

### 4.6 A firewall rule keyed on the account

[owner-block.sh](s4-sandbox/owner-block.sh) adds `nft` rules for the uid of `suiteward-exec` that reject the metadata addresses **by destination, with no port match**: `ip daddr 169.254.169.254` and `ip6 daddr fd00:ec2::254` (the address that AWS Nitro hosts also answer on over IPv6). With no port match, a service on any port of those addresses is out of reach.

Measured in the distro, where no metadata service answers. Before any rule, a connect to `169.254.169.254` on port 80 and on port 8080 timed out (6 s for a native process, 3 s for a container). With the rule, both ports were rejected at once (a native process in about 1 s, a container in under 1 s), the rule's packet counter moved (6 packets after one probe round), and the same attempts by `suiteward` still timed out. The rule held for the native process of `suiteward-exec` **and** for its rootless containers, on the default network and on the user-defined one (pasta and slirp4netns run as that account, so the rule sees their sockets). A hop limit of 1 would not stop the metadata service for a rootless container, because the connection is made by a process of the account on the host, not forwarded through an extra hop (inference from how pasta works). The IPv6 rule was accepted by `nft` but no connection exercised it (its counter stayed 0), so the test says nothing about IPv6 behavior.

Over a real connection, on a destination that does answer: a rule on the address behind `proxy.golang.org`, port 443, rejected the account's connections and left `suiteward` unaffected. That rule is only a stand-in and not a design: the name resolved to 142.250.219.49 in one run and to 142.251.132.241 in another, an hour apart, so an IP set for Google front ends is brittle. The proxy host belongs in the executor's HTTP allow-list (3.3), and the host rule keeps to addresses that do not change.

## 5. Co-located (from documentation and the Docker measurements only)

No `podman` ran against the Windows machine, and nothing here was measured on a Podman machine or on macOS.

What the Docker measurements say: on Docker Desktop a container on a bridge network reaches the host's loopback and published ports of other networks (section 3); `--network=none` reaches nothing; a sandbox with no network at all (3.3) makes the reachability question moot for the sandbox. For a Podman machine, the Podman documentation (`podman run`, read on 2026-10-26) says that on a machine the entry `host.containers.internal` is not added to `/etc/hosts` and the name is resolved by the gvproxy DNS resolver instead, so what a machine's containers reach of the host is not something to rely on without a test; the sandbox has no network, so the question does not arise for it.

A known limit, from the installer rather than from this spike: a Podman machine's published loopback port does not always answer from Windows. The installer's `statusHint` (`internal/installer/colocated.go`) exists for it: when this PC cannot reach the port, it prints the command that reads `/status` inside the machine instead of an address. A host-process executor on such a setup cannot reach the service either: `doctor` detects it and the executor reports it, the run then reads `no-executor`, and the owner can unblock it (the design note, decisions after S0). Not measured here.

**Recommendation: on both Docker Desktop and a Podman machine, the executor is a host process of the user that runs `suiteward execute`, not a container with the runtime socket.**

- It reaches the service where `doctor` and `/status` already do: `http://127.0.0.1:<HTTP port>` (27081 by default), the loopback port the stack publishes, with the limit above for a Podman machine. A container would reach it by the service's name on the `suiteward` network instead, which keeps it off the host loopback but puts a container on that network.
- No runtime socket goes into a container. On Docker Desktop the socket is the rootful engine; on a Podman machine it is the machine's user socket, and a container holding it has what shape (b) showed: the account's whole authority.
- It reuses what the installer already does: `internal/installer` drives `docker` and `podman` by explicit binary and explicit context or connection, and identifies the engine before it creates anything, refusing Podman's `docker` shim (spike S2). The sandbox runs through the same driver, with `-i` and the tar on standard input (measured with a remote client in 4.5).
- The cost is a lifecycle the installer must provide on each OS (a logon task, a launch agent, or a user unit): T9's choice. The alternative, a container in the stack, would get the runtime's restart policy and an image pinned by digest for free, and costs the socket mount, `--user 0` and, on a real Linux host, the SELinux exception of section 4.5.

**What it means for assurance.** None gained against a determined agent: the agent runs as the same user, so it can read the executor's token file, replace the binary, call the same `docker` or `podman` and edit `.suiteward.yml`, whichever shape the executor has. The pins (binary SHA-256, image digest) catch drift and accident. Receipts and canonical results on this tier are evidence about a head, stated as reduced assurance, as the design note already says.

## 6. Recommendations

**For T5 (`suiteward execute` and the sandbox backend)**

1. Flags as in section 1: `--network=none --read-only --tmpfs /work:rw,exec,nosuid,nodev,size=1g --cap-drop=ALL --security-opt no-new-privileges --pids-limit 512 --memory 3g --memory-swap 3g --cpus 4 --user 65534:65534`, the source and the module files in one tar on standard input, no bind mounts, `HOME`, `GOCACHE`, `GOPATH`, `TMPDIR` on the tmpfs, `GOTOOLCHAIN=local GOWORK=off GOFLAGS=-mod=readonly GOPROXY=file:///… GOSUMDB=off CGO_ENABLED=0`, and a cold build cache per run. No shared writable cache.
2. One image per profile, pinned by digest, the same on both sides of a receipt; record the image digest and the runtime in the evidence. The Debian image gives the most tools (and `gcc`); the alpine pin is smaller; either way the profile fixes it. No `-race` in M2.1 (1.7 times the time, needs the larger image).
3. Do not use `--rm`. Label every sandbox (job id), start `go test` as the container's own process (`exec go test …`), read `.State.OOMKilled` and the exit status after the run, then remove it. Only signals from outside the sandbox count: `.State.OOMKilled`, exit status 137, and the executor's own timeout kill. Any of them is `unavailable` with reason `resource-limit`; a candidate can cause it on purpose. Never decide it from text in the build output. Keep an own deadline and kill by name; sweep labelled leftovers at start.
4. A package with a `build-fail` is a build error. Whether the sandbox was starved is decided only by the signals of item 3.
5. Run as `nobody` (65534) so Docker and Podman give the same observations.
6. No network in any sandbox (the owner's decision, 3.3): fetch the modules in the executor. Validate every module path and version with `golang.org/x/mod/module` (`CheckPath`, `EscapePath`, `EscapeVersion`) and `semver` before it becomes a URL or a path, and refuse the job when one fails (a deterministic `install-failed`). Bound the entries, the bytes and the time. Allow `proxy.golang.org` and `storage.googleapis.com` only, in the HTTP client, and never an IP set. Fail the job on any fetch error (an infrastructure failure, retried); do not copy the prototype's `curl -L` or `|| echo FAILED`. A `replace` to a directory inside the tree needs no fetch; outside the tree it fails. The integrity of the bytes is TLS to the proxy plus the candidate's own `go.sum`: equivalent to a networked install, not stronger.
7. Bound what the candidate controls: number of `go.sum` entries, bytes fetched, test output size, wall time, and the size of the tar (source plus modules), which counts against the tmpfs and the memory.
8. Build the stdin tar from the verified tree: check every archive entry against the tree's blob SHA-1 in-stream, and only then write it to the container's standard input. Nothing of the tree or the modules is written to a host filesystem.

**For T9 (the installs)**

1. Isolated host: write the delegation drop-in `/etc/systemd/system/user@.service.d/delegate.conf` (`Delegate=cpu cpuset io memory pids`) before the stack starts, because restarting a user manager stops its containers.
2. Isolated host: `suiteward-exec` as a second account with its own subuid and subgid range (checked disjoint from `suiteward`), linger, a systemd user unit for the executor (shape (a)), the binary root-owned and read-only to it, and a `doctor` check of the binary's SHA-256 and the sandbox image digest.
3. Isolated host: `nft` rules keyed on the executor's uid (`meta skuid`) that reject the metadata addresses by destination, with no port match: `ip daddr 169.254.169.254` and `ip6 daddr fd00:ec2::254` (4.6). `hidepid=2` on `/proc` only if made persistent and tested first, with its group exemption (4.3). Size the host for 4 vCPUs and 3 GiB per sandbox: 4 vCPUs and 8 GiB at least for one at a time.
4. Co-located: the executor as a host process of the user (section 5); the lifecycle per OS is T9's. `doctor` pins its binary and the image digest and says the assurance is reduced.
5. SELinux enforcing: re-check the items of section 4.5 on the real host, and keep the design free of bind mounts.

**For A0 (ADR 0028)**

1. Record that a resource kill is `unavailable` with reason `resource-limit`, decided only from signals outside the sandbox, that a candidate can cause one, and that observations depend on image and runtime, so both are part of the evidence.
2. Record the owner's decision of no network in the sandbox (3.3): the integrity of the fetched bytes is TLS to the proxy plus the candidate's own `go.sum`, equivalent to a networked install; the module path and version are validated before use; a Docker Desktop plain bridge would have reached the host loopback and other networks' published ports.
3. State that the Go profile's "expected tests" and "observations" do not cover what the image lacks (tools), and that two sides of a receipt must run on the same image.

## 7. Not measured

- **Cloud metadata** (`169.254.169.254`, `fd00:ec2::254`): whether it is reachable from the executor or a sandbox on the isolated host, with a hop limit of 1 or not. Docker Desktop and the WSL distro have no metadata service: the rule was tested by what happens to a connect to that address (rejected at once against timing out), and the IPv6 rule was never exercised by a connection.
- **SELinux enforcing**: WSL's kernel has the option but no policy (`Disabled`), and `container-selinux` was not installed, so no label, no `container_t` transition, no socket-mount denial was seen.
- **arm64**: time and memory (the isolated host is probably Graviton).
- **A real reboot** of an AlmaLinux host: lifecycle was shown with linger and unit restarts only. WSL artifacts that are not about AlmaLinux: the reserved uid 1000, the `219/CGROUP` failure of a user manager after the distro stopped with running containers, the shared-mount warning, and the distro stopping by itself between commands.
- **Podman machine** and **macOS** (anything), **Docker on Linux**, a **Podman rootful** connection.
- **Hostile load**: a fork bomb, a memory bomb in a test, a full tmpfs, a CPU burner, a test that tries `/proc` or `ptrace`. Only a benign suite and a 64 MiB OOM test ran, so the limits are shown to hold, not to resist an attack. The executor-side fetch was not tried with a hostile `go.sum`.
- **Concurrency**: two sandboxes at once, many jobs, the growth of images and volumes over time, image pull time on a fresh host, and a repository with `vendor/` or private modules.
- **`GOMEMLIMIT`** and `-p` as ways to lower the memory floor (one `GOMEMLIMIT` run did not help).
- **The file-proxy test mode under the swap pin** (it was measured before it).
- **A Podman machine's published loopback port from Windows** (the installer's known limit, section 5), and whether a machine's containers reach the Windows host through gvproxy.
- **The product's tar:** putting the module zips into the stdin tar (size, time, the tmpfs and memory they take, on top of the 457 MB measured with a volume), the in-stream blob SHA-1 check's cost, and `exec go test` as the container's process (the exit status 137 case was seen only through the spike's wrapper shell).
- **The executor-side fetch:** the validation with `golang.org/x/mod`, a hostile `go.sum`, the proxy's rate limits and outages and the retry policy (one run of five failed on every request, cause unknown), TLS interception, and a host-level egress allow-list for `proxy.golang.org` (IP addresses rotate, 4.6; none was designed).
- **`GOSUMDB=off`** (the sandbox in the decision): the runs used the default `GOSUMDB` with a `go.sum` that had every entry, offline.
- **`hidepid`** persistence and its group exemption, and its effect on `systemd-logind` and other services (documentation only; the remount was reverted at once).
- **Other platforms and versions:** Docker Desktop on macOS, Podman 5.5 (the installer's minimum; 5.8.2 was tested), an AlmaLinux other than 9.8, a cgroup v1 host, a kernel other than WSL's 6.6.
- **Larger suites:** a repository whose tar, modules and build cache do not fit the 1 GiB tmpfs, and a test output that is very large.
- **The 73 MB versus 30 MB** difference between the prototype's fetch and a prepared module cache (3.3): not explained.
- **`-race` and the bookworm image on Podman:** both ran on Docker only; Podman ran the alpine pin only.
- **`.State.OOMKilled` read through a remote client** (podman-remote, or a Podman machine's Windows client), which is how a co-located executor would read it.
- **A hostile stdin tar:** very many files, deep paths, symlinks, entries at the 8 MiB cap, extracted by busybox `tar` in the sandbox.
- **Seccomp profiles:** the differences between Docker Desktop's and rootless Podman's defaults.
- **Reproduce gaps:** the commands of the persistent-`GOCACHE` run (2.2) and of the timeout experiments (2.3) were not recorded.
- **Whether the executor may cache the downloaded module files** between jobs (3.3), and how much disk they and the images take over time.


## Reproduce

All helpers are in `docs/spikes/s4-sandbox/` and are run from there. Docker side, in Git Bash or any `sh` with Docker Desktop running (`export MSYS_NO_PATHCONV=1` for the `docker` commands, but not for `fetch-proxy.sh`, whose `curl.exe` is a native program):

```sh
. ./image.sh; export IMAGE=$ALPINE; docker pull $ALPINE
sh smallmod.sh <dir>                                       # the small module
docker network create m21s0-inst
sh sandbox.sh prep; tar -C <dir> -cf - . | sh sandbox.sh install; tar -C <dir> -cf - . | sh sandbox.sh test ./...
SMALL=<dir> sh breaks.sh                                   # section 1.1
git -C <repo> archive HEAD > repo.tar; tar -C <dir> -cf small.tar .
VOL=m21s0-gomod-repo sh sandbox.sh prep; VOL=m21s0-gomod-repo sh sandbox.sh install < repo.tar    # the volumes bench.sh uses
WORK=<dir holding small.tar and repo.tar> sh bench.sh quick|time|cpu|floor                          # section 2
sh oom-signal.sh                                           # section 2.3

# section 3: the probes
pwsh -File listen.ps1 -Port 18081                          # another terminal: the host listener on 127.0.0.1:18081
docker network create m21s0-svc; docker network create --internal m21s0-internal
docker run -d --name m21s0-svc-listener --network m21s0-svc -e BANNER=svc-listener-8080 -p 127.0.0.1:18091:8080 $ALPINE sh -c "$(cat listener.sh)"
docker run -d --name m21s0-bridge-listener -e BANNER=bridge-listener-8080 $ALPINE sh -c "$(cat listener.sh)"
docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' m21s0-svc-listener m21s0-bridge-listener    # the two addresses
for net in bridge m21s0-inst m21s0-internal none; do
  docker run --rm -i --network $net $ALPINE sh -s -- host.docker.internal:18081 host.docker.internal:18091 <svc address>:8080 <bridge address>:8080 169.254.169.254:80 proxy.golang.org:443 < probe.sh
done

# 3.1 the mirror: the program of 3.1 as main.go with a go.mod, built and run on the two networks
docker run --rm -v <dir>:/src -w /src -e GOCACHE=/tmp/c -e CGO_ENABLED=0 -e GOFLAGS=-mod=mod $ALPINE go build -trimpath -o /src/px .
docker run -d --name m21s0-proxy --network m21s0-internal --read-only --cap-drop=ALL --security-opt no-new-privileges --user 65534:65534 -v <dir>:/p:ro $ALPINE /p/px
docker network connect m21s0-inst m21s0-proxy
VOL=m21s0-gomod-px sh sandbox.sh prep; VOL=m21s0-gomod-px NET=m21s0-internal GOPROXY=http://m21s0-proxy:8081 sh sandbox.sh install < repo.tar

# 3.2 the prefetched file proxy
sh fetch-proxy.sh <repo>/go.sum <outdir>
VOL=m21s0-fp sh sandbox.sh prep; tar -C <outdir> -cf - . | docker run --rm -i --network=none --user 65534:65534 --read-only -v m21s0-fp:/fp $ALPINE tar -x -C /fp
FP=m21s0-fp sh sandbox.sh test ./... < repo.tar            # no install step, no cache volume
VOL=m21s0-gomod-fp sh sandbox.sh prep; VOL=m21s0-gomod-fp FP=m21s0-fp NET=none GOPROXY=file:///fp sh sandbox.sh install < repo.tar
```

Podman side. `RT=podman` and every script that runs as root run **only inside the throwaway distro** (`guard.sh` refuses elsewhere: on the PC `podman` may be the client of the Podman machine). From the repository, with `<repo>` as the path inside the distro (`/mnt/d/...`):

```sh
wsl --install -d AlmaLinux-9 --name m21s0-alma --no-launch
wsl -d m21s0-alma --cd /tmp -u root -- sh <repo>/docs/spikes/s4-sandbox/alma-setup.sh      # then, in the distro, as root:
sh isolation-users.sh setup; sh isolation-users.sh check; sh isolation-users.sh teardown   # 4.3
systemd-run --unit=m21s0-listener python3 -m http.server 18081 --bind 127.0.0.1             # the distro's host listener, for 4.4
sh isolation-users.sh setup; sh probe-podman.sh [network flag, for example --network=none]   # 4.4, once per mode
sh as.sh suiteward-exec env RT=podman sh sandbox.sh prep                                     # 4.2: the sandbox as suiteward-exec
sh as.sh suiteward-exec env RT=podman sh oom-signal.sh
sh executor-shapes.sh build; sh executor-shapes.sh socket; sh executor-shapes.sh b-run <tar> # 4.5; also b-nonroot, b-power, units, crash, state
sh owner-block.sh on; sh owner-block.sh probe; sh owner-block.sh off                         # 4.6 (NETFLAG=--network=m21s0-inst for the user network)
mount -o remount,hidepid=2 /proc; sh as.sh suiteward-exec ps -eo user,args; mount -o remount,hidepid=0 /proc    # 4.3
```

Cleanup, as done, and what the machine showed afterwards (the Podman machine and the Docker Desktop images that were there before are untouched):

```sh
docker stop m21s0-proxy m21s0-svc-listener m21s0-bridge-listener; docker rm m21s0-proxy m21s0-svc-listener m21s0-bridge-listener
docker volume rm $(docker volume ls -q --filter name=m21s0)
docker network rm m21s0-inst m21s0-internal m21s0-svc
docker image rm $ALPINE $BOOKWORM
wsl --unregister m21s0-alma
```
```text
> docker ps -a --format '{{.Names}}'
> docker volume ls --format '{{.Name}}'
> docker network ls --format '{{.Name}}'
bridge
host
none
> docker images --format '{{.Repository}}'
ghcr.io/ignisdevne/suiteward      (four, there before)
postgres                          (there before)
> wsl -l -v
  NAME                      STATE           VERSION
* Ubuntu-24.04              Stopped         2
  podman-machine-default    Running         2
  docker-desktop            Running         2
> netstat -ano | findstr ":18081 :18091"
(nothing; the listener process was stopped, and port 47081 was never touched)
```
