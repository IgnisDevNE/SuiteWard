# S4: sandbox spike

- **Date:** 2026-10-26
- **Phase:** [M2.1](../plan/phases/M2.1.md), task S0
- **Decides input for:** ADR 0028 (task A0), the executor (T5) and the install (T9). The design it tests is the "Sandbox" section of the M2.1 design note.

This spike ran the Go sandbox on real runtimes, before the executor and its install are built: Docker Desktop on Windows, and rootless Podman under two accounts in a throwaway AlmaLinux 9 WSL distro. Everything below is **measured** unless a sentence says it is documentation or inference. The helper scripts are in [s4-sandbox/](s4-sandbox/); each command quoted here is one of them or a one-liner.

**Result.** The sandbox works as designed, on both runtimes, with the same flags. Six things in the design note need to change or be added, in the order of how much they matter:

1. A resource limit is read as a test result. When the OOM killer takes the compiler, `go test -json` reports `build failed` for that package and ends normally, so the suite looks complete. Under the receipt rules that is FAILS on the head or NEW API on the base. The executor has to read the kill from outside the sandbox and call the run `unavailable` (section 2.3).
2. `--memory` needs `--memory-swap` equal to it, and 3 GiB, not 1 GiB: with swap hidden behind the default, the suite passed at 1 GiB; with swap pinned, it needs between 1.5 and 2 GiB, so 3 GiB gives margin (section 2.2).
3. On Docker Desktop, "never on the `suiteward` network" does not keep a container away from the service: any bridge network reaches host loopback, and a published port is reachable by container IP from other networks. On rootless Podman it does hold (section 3, 4.4). A networked install step is therefore the only place the sandbox touches anything, and it can be removed: the executor fetches the module files and the sandbox installs from a read-only `file://` proxy with no network (section 3.3, a proposal for A0, because it departs from "network only for the install").
4. The same suite gives different SKIP sets by image and by runtime (section 1.3). Pin one image per profile, run as `nobody` (65534), and record the runtime.
5. On the isolated host rootless Podman works only after a systemd drop-in that delegates the `cpu` controller; the install must write it (section 4.1).
6. For the isolated tier, the executor should be a native process of `suiteward-exec`, not a container with its socket (section 4.5). For both co-located runtimes, a host process (section 5).

## Environment

| | Docker side | Podman side |
| --- | --- | --- |
| Runtime | Docker Desktop, engine 29.9.2 (runc, cgroup v2, context `desktop-linux`); the machine has 8 logical CPUs, and the WSL VM is capped at 24 GB with 6 GB of swap | Podman 5.8.2 (crun 1.27, netavark 1.17.2, passt 20251210) on AlmaLinux 9.8, rootless, in a throwaway WSL distro `m21s0-alma` with systemd, on the same VM and kernel (6.6.114.1-microsoft-standard-WSL2) |
| Images | `golang:1.27.3-alpine`, digest `sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd` (382 MB; the pin of `deploy/Containerfile`), and `golang:1.27.3-bookworm`, digest `sha256:972b4110ba0f47539a975c31e52c64765e1ff19f2d74469ae4a595c6676e80a5` (1.25 GB; resolved from the tag on 2026-10-26). Both are in [image.sh](s4-sandbox/image.sh) | the alpine pin only |
| Targets | The small module ([smallmod.sh](s4-sandbox/smallmod.sh): one dependency, five tests, one fails on purpose, one skips) and this repository at `27e2d84c`, taken with `git archive`: 37 packages with tests, 8161 or more passing tests counting subtests | the same two |

The Windows Podman machine was never addressed. Only the Docker Desktop engine and the `m21s0-alma` distro were used, and both are gone (the end of section 7 lists the state).

## 1. The sandbox run on Docker Desktop

[sandbox.sh](s4-sandbox/sandbox.sh) holds the flags. The module's files arrive as a tar on the container's standard input and are extracted into the tmpfs, so nothing is bind-mounted (a bind mount would depend on host paths, which do not exist in a Podman machine, and on SELinux labels on a real host).

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
| a wrong hash in `go.sum` (met by accident, in the install step) | `SECURITY ERROR … checksum mismatch`; nothing is stored | none needed: `go.sum` is the integrity anchor of the install |
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

Without the swap pin the same suite passed at 896 MiB (no race) and at 1 GiB (race) on this VM: do not carry those numbers over. `GOMEMLIMIT=600MiB` at 1 GiB did not prevent the kills. The tmpfs counts against the limit (it is `shmem` in the cgroup): 352 MB of the 1.4 to 1.7 GB for this repository, 457 MB when the modules are extracted into it (section 3.3).

**Recommended limits for the Go profile:** `--memory 3g --memory-swap 3g --cpus 4 --pids-limit 512` (the peak was 43 to 55 processes) and `--tmpfs /work:rw,exec,nosuid,nodev,size=1g`. A bigger host can give more CPUs for a faster run; a limit under about 2 GiB turns a suite of this size into false failures.

**Isolated host size** (inference from these numbers, not a test on that host): one sandbox at a time needs 4 vCPUs and 3 GiB for the sandbox, besides the service (256 MiB by its quadlet), PostgreSQL and the system; 4 vCPUs and 8 GiB of memory is the floor, and 20 GB of disk is enough for the image, a module cache per distinct `go.sum` and the logs. A receipt runs the head and the base, about 65 s for this repository on 4 CPUs (sequentially) plus the fetch. Running them in parallel needs 8 vCPUs and 12 to 16 GiB. The isolated host is probably arm64: memory and time there are not measured.

### 2.3 What a limit looks like from outside

Killing the compiler with the cgroup OOM killer (the 768 MiB runs above):

- `go test -json` reports `compile: signal: killed` as `build-output`, then `build-fail`, then `FAIL … [build failed]` for the package, and the stream ends normally with the other packages' results. The run's exit status is 1, and the count of tests falls (8161 to 4103). Under the receipt rules this is FAILS (head) or NEW API (base).
- The signal from outside the sandbox works on both runtimes ([oom-signal.sh](s4-sandbox/oom-signal.sh), with a memory hog in a 64 MiB container): `inspect` says `.State.OOMKilled=true` both when only a child of the container was killed (exit status 0) and when its first process was (137). Docker also emits an `oom` event. Podman's event stream showed only `died`. The container must exist when it is inspected, so **do not use `--rm`**: inspect, then remove.
- The `oom_kill` counter that `sandbox.sh` prints is read by the sandbox's own shell, so candidate code could change it. Use it as a diagnostic, not as the signal.

**Rule for T5:** an `OOMKilled` state, a `signal: killed` in build output, or an exit status 137 makes the run `unavailable`, never a result.

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

### 3.1 Restricting the install step, three ways

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
3. **No network in the sandbox at all.** The executor fetches the files itself and the sandbox installs from a read-only `file://` proxy. This is the recommendation (3.3).

### 3.2 Network-less install from a `file://` proxy

`go mod download` in a `--network=none` container with `GOPROXY=file:///fp` (a read-only volume laid out as a module proxy) and the default `GOSUMDB` worked: 1.9 s, and the module cache was identical to the direct one. Every file is checked by `go` against `go.sum`: a wrong `go.mod` hash was refused (`SECURITY ERROR`), and so was a file that was not a zip (my first fetch saved the redirect page of a zip instead of the zip: `go: zip: not a valid zip file`).

[fetch-proxy.sh](s4-sandbox/fetch-proxy.sh) is the executor-side fetch as a prototype: for each line of `go.sum` it downloads `<module>/@v/<version>.info`, `.mod` or `.zip` from `proxy.golang.org` with plain HTTPS GETs (following redirects, 8 at a time). For this repository that is 170 files and 73 MB; it took 13 to 17 s. Of the five runs that followed redirects, four ended with 0 failures and 170 files each (two compared byte for byte, identical), and one failed on every request for a reason I did not find, so the fetch needs retries and any failure makes the run `unavailable`. With that proxy the sandbox needs no install step and no cache volume at all: `FP=<volume> sh sandbox.sh test ./...` mounts it read-only, points `GOPROXY` at it and builds `GOMODCACHE` in the tmpfs. Three runs took 29.6, 30.1 and 32.4 s (no slower than with a prepared cache), and the tmpfs held 457 MB. (Measured on the alpine image before the swap pin; the extra 105 MB counts against the memory limit.)

### 3.3 Recommendation, and what it costs

For all tiers: fetch in the executor, and give the sandbox no network, ever. It removes the install container, the volume ownership step, the mirror, the metadata question and the Docker Desktop reachability problem in one change. It is a proposal for A0, not a decision: it departs from the design note's "network only for the dependency install", and the executor then has its own egress (the isolated host should restrict it by user, section 4.6). Its costs:

- The `go.sum` of the candidate is untrusted input: bound the number of entries and the bytes fetched, allow one host (`proxy.golang.org`) and its one redirect host, and set timeouts. A module the proxy does not serve (private, or a `replace` to a path) fails the run as `unavailable`.
- The file set is a superset of what `go mod download` fetches: 73 MB for the 170 files of this repository's `go.sum`, against 30 MB in the `cache/download` that `go mod download` left (I did not find out which entries account for the difference). Every file is one that `go` checks against `go.sum` anyway.
- The proxy volume can be kept per `go.sum` digest and mounted read-only: `go` verifies every file against the candidate's `go.sum` anyway, so a stale or tampered file is refused, not trusted.
- The fetch is code the executor ships (about 60 lines in Go), and `GOFLAGS=-mod=readonly` plus `GOVCS=*:off` keep `go` from doing anything else.

If A0 prefers to keep the install step: on Docker use the `--internal` network and the mirror (3.1, option 2); on rootless Podman a plain user-defined network is acceptable (section 4.4), plus the firewall rule of section 4.6.

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

`mount -o remount,hidepid=2 /proc` took the processes of `suiteward` out of `suiteward-exec`'s view (5 listed to 0) and was reverted; T9 can set it, or rely on the no-secrets-in-arguments rule alone.

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

**Recommendation for the isolated tier: (a).** One native process of `suiteward-exec`, started by a systemd user unit with linger, pinned by the binary's SHA-256, calling `podman run` with the sandbox flags. It has the same power as (b) with fewer parts, no socket, no `podman-remote` in the image, no `-T` mapping, no SELinux exception. The install writes the delegation drop-in (4.1), may set `hidepid=2` (4.3), and creates the firewall rule (4.6).

### 4.6 A firewall rule keyed on the account

[owner-block.sh](s4-sandbox/owner-block.sh): `nft add rule inet m21s0 out meta skuid 1001 ip daddr <address> tcp dport 443 reject`. It blocked the native process of `suiteward-exec` **and** its rootless containers, on the default network and on the user-defined one (pasta and slirp4netns run as that account, so the rule sees their sockets), and left `suiteward` unaffected. This is the mechanism for the metadata service on a cloud host: a hop limit of 1 would not stop it for a rootless container, because the connection is made by a process of the account on the host, not forwarded through an extra hop (this is inference from how pasta works; the test used `proxy.golang.org` as the destination). The rule here is IPv4 only; the metadata service can also answer on an IPv6 address, which needs its own `ip6 daddr` rule. The same rule form can allow only the proxy and the service for the account if the executor's egress should be an allow-list.

## 5. Co-located (from documentation and the Docker measurements only)

No `podman` ran against the Windows machine, and nothing here was measured on a Podman machine or on macOS.

What the Docker measurements say: on Docker Desktop a container on a bridge network reaches the host's loopback and published ports of other networks (section 3); `--network=none` reaches nothing; a network-less install (3.2) makes the reachability question moot for the install step. For a Podman machine, the Podman documentation (`podman run`, read on 2026-10-26) says that on a machine the entry `host.containers.internal` is not added to `/etc/hosts` and the name is resolved by the gvproxy DNS resolver instead, so what a machine's containers reach of the host is not something to rely on without a test, and the same network-less install avoids the question.

**Recommendation: on both Docker Desktop and a Podman machine, the executor is a host process of the user that runs `suiteward execute`, not a container with the runtime socket.**

- It reaches the service where `doctor` and `/status` already do: `http://127.0.0.1:<HTTP port>` (27081 by default), the loopback port the stack publishes. A container would reach it by the service's name on the `suiteward` network instead, which keeps it off the host loopback but puts a container on that network.
- No runtime socket goes into a container. On Docker Desktop the socket is the rootful engine; on a Podman machine it is the machine's user socket, and a container holding it has what shape (b) showed: the account's whole authority.
- It reuses what the installer already does: `internal/installer` drives `docker` and `podman` by explicit binary and explicit context or connection, and identifies the engine before it creates anything, refusing Podman's `docker` shim (spike S2). The sandbox runs through the same driver, with `-i` and the tar on standard input (measured with a remote client in 4.5).
- The cost is a lifecycle the installer must provide on each OS (a logon task, a launch agent, or a user unit): T9's choice. The alternative, a container in the stack, would get the runtime's restart policy and an image pinned by digest for free, and costs the socket mount, `--user 0` and, on a real Linux host, the SELinux exception of section 4.5.

**What it means for assurance.** None gained against a determined agent: the agent runs as the same user, so it can read the executor's token file, replace the binary, call the same `docker` or `podman` and edit `.suiteward.yml`, whichever shape the executor has. The pins (binary SHA-256, image digest) catch drift and accident. Receipts and canonical results on this tier are evidence about a head, stated as reduced assurance, as the design note already says.

## 6. Recommendations

**For T5 (`suiteward execute` and the sandbox backend)**

1. Flags as in section 1: `--network=none --read-only --tmpfs /work:rw,exec,nosuid,nodev,size=1g --cap-drop=ALL --security-opt no-new-privileges --pids-limit 512 --memory 3g --memory-swap 3g --cpus 4 --user 65534:65534`, the source as a tar on standard input, no bind mounts, `HOME`, `GOCACHE`, `GOPATH`, `TMPDIR` on the tmpfs, `GOTOOLCHAIN=local GOWORK=off GOFLAGS=-mod=readonly GOPROXY=… CGO_ENABLED=0`, and a cold build cache per run. No shared writable cache.
2. One image per profile, pinned by digest, the same on both sides of a receipt; record the image digest and the runtime in the evidence. The Debian image gives the most tools (and `gcc`); the alpine pin is smaller; either way the profile fixes it. No `-race` in M2.1 (1.7 times the time, needs the larger image).
3. Do not use `--rm`. Label every sandbox (job id), read `.State.OOMKilled` and the exit status after the run, then remove it. An `OOMKilled`, a `signal: killed` in build output or exit status 137 is `unavailable`. Keep an own deadline and kill by name; sweep labelled leftovers at start.
4. Treat the JSON as complete only with the process status and these signals: a package with a `build-fail` is a build error only if nothing else says the sandbox was starved.
5. Run as `nobody` (65534) so Docker and Podman give the same observations.
6. Prefer the executor-side fetch (3.3): no network in any sandbox. If A0 keeps an install step, give it an `--internal` network and the mirror on Docker, and a plain user-defined network plus the firewall rule on rootless Podman.
7. Bound what the candidate controls: number of `go.sum` entries, bytes fetched, test output size, wall time, and the size of the tar.

**For T9 (the installs)**

1. Isolated host: write the delegation drop-in `/etc/systemd/system/user@.service.d/delegate.conf` (`Delegate=cpu cpuset io memory pids`) before the stack starts, because restarting a user manager stops its containers.
2. Isolated host: `suiteward-exec` as a second account with its own subuid and subgid range (checked disjoint from `suiteward`), linger, a systemd user unit for the executor (shape (a)), the binary root-owned and read-only to it, and a `doctor` check of the binary's SHA-256 and the sandbox image digest.
3. Isolated host: a firewall rule keyed on the executor's uid for the metadata address (IPv4 and IPv6), and optionally `hidepid=2` on `/proc`. Size the host for 4 vCPUs and 3 GiB per sandbox: 4 vCPUs and 8 GiB at least for one at a time.
4. Co-located: the executor as a host process of the user (section 5); the lifecycle per OS is T9's. `doctor` pins its binary and the image digest and says the assurance is reduced.
5. SELinux enforcing: re-check the items of section 4.5 on the real host, and keep the design free of bind mounts.

**For A0 (ADR 0028)**

1. Record that a resource kill is `unavailable`, never a result, and that observations depend on image and runtime, so both are part of the evidence.
2. Decide whether the sandbox has no network at all (the executor fetches, 3.3) or keeps a networked install, knowing that on Docker Desktop a plain bridge reaches the host loopback and other networks' published ports.
3. State that the Go profile's "expected tests" and "observations" do not cover what the image lacks (tools), and that two sides of a receipt must run on the same image.

## 7. Not measured

- **Cloud metadata** (`169.254.169.254`): whether it is reachable from a sandbox, from the install step or from the executor on the isolated host, with a hop limit of 1 or not, and the firewall rule against it. Docker Desktop and the WSL distro have no metadata service; the rule was tested with another destination.
- **SELinux enforcing**: WSL's kernel has the option but no policy (`Disabled`), and `container-selinux` was not installed, so no label, no `container_t` transition, no socket-mount denial was seen.
- **arm64**: time and memory (the isolated host is probably Graviton).
- **A real reboot** of an AlmaLinux host: lifecycle was shown with linger and unit restarts only. WSL artifacts that are not about AlmaLinux: the reserved uid 1000, the `219/CGROUP` failure of a user manager after the distro stopped with running containers, the shared-mount warning, and the distro stopping by itself between commands.
- **Podman machine** and **macOS** (anything), **Docker on Linux**, a **Podman rootful** connection.
- **Hostile load**: a fork bomb, a memory bomb in a test, a full tmpfs, a CPU burner, a test that tries `/proc` or `ptrace`. Only a benign suite and a 64 MiB OOM test ran, so the limits are shown to hold, not to resist an attack. The executor-side fetch was not tried with a hostile `go.sum`.
- **Concurrency**: two sandboxes at once, many jobs, the growth of images and volumes over time, image pull time on a fresh host, and a repository with `vendor/` or private modules.
- **`GOMEMLIMIT`** and `-p` as ways to lower the memory floor (one `GOMEMLIMIT` run did not help).
- **The file-proxy test mode under the swap pin** (it was measured before it), and **`hidepid`** outside WSL.

## Reproduce

```sh
# Docker side (Git Bash or any sh; Docker Desktop running)
sh smallmod.sh <dir>                                     # the small module
. ./image.sh; export IMAGE=$ALPINE
docker network create m21s0-inst
sh sandbox.sh prep; tar -C <dir> -cf - . | sh sandbox.sh install; tar -C <dir> -cf - . | sh sandbox.sh test ./...
IMAGE=$IMAGE SMALL=<dir> sh breaks.sh                    # section 1.1
WORK=<dir with small.tar and repo.tar> sh bench.sh quick|time|cpu|floor    # section 2
RT=docker IMAGE=$IMAGE sh oom-signal.sh                  # section 2.3
pwsh -File listen.ps1 -Port 18081 &                      # section 3: the host listener; then listener.sh in a container, and probe.sh
# Podman side, in a fresh `wsl --install -d AlmaLinux-9 --name m21s0-alma --no-launch`, as root:
sh alma-setup.sh; sh isolation-users.sh setup; sh isolation-users.sh check; sh probe-podman.sh <network flag>; sh executor-shapes.sh socket
sh owner-block.sh on; sh owner-block.sh probe; sh owner-block.sh off
```

Cleanup, as done: the Docker volumes and networks named `m21s0-*`, both Go images (they were not on the machine before), the listener process, and `wsl --unregister m21s0-alma`.
