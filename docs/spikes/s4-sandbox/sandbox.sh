#!/bin/sh
# Spike helper for M2.1-S0: the networked install step and the offline sandbox run, on docker or podman.
# The module's files arrive as a tar on stdin (git archive, or tar of a directory); nothing is bind-mounted.
# usage: RT=docker|podman IMAGE=<name@sha256:digest> [VOL= NET= FP= SUID= MEM= CPUS= PIDS= TMPFS= CGO= GOMEMLIMIT=] sandbox.sh prep|install|test [go test args]
# VOL: the module cache volume (install writes it, test mounts it read-only). FP: a volume laid out as a module proxy; with FP, install runs
# from file:///fp (NET=none works) and test needs no VOL at all: go extracts the modules it needs into the tmpfs. (FP is a convenience of the
# spike: the product puts the module files in the same stdin tar as the source, and no volume.)
# RT=podman runs only inside the throwaway distro m21s0-alma (guard.sh): on the PC `podman` may be the client of the Podman machine.
set -eu
. "$(dirname "$0")/image.sh"; D=$(dirname "$0")
RT=${RT:-docker}; IMAGE=${IMAGE:-$ALPINE}
if [ "$RT" = podman ]; then . "$D/guard.sh"; fi
VOL=${VOL:-m21s0-gomod}; NET=${NET:-m21s0-inst}; FP=${FP:-}
SUID=${SUID:-65534}   # nobody: both images and both runtimes know it, so none has to invent a passwd entry
MEM=${MEM:-3g}; CPUS=${CPUS:-4}; PIDS=${PIDS:-512}; TMPFS=${TMPFS:-1g}; CGO=${CGO:-0}
export MSYS_NO_PATHCONV=1   # Git Bash on Windows rewrites arguments that start with /
cmd=$1; shift

# exec on the tmpfs: go test runs the binaries it compiles there. cache and temp live on the tmpfs because the root is read-only.
common="--rm -i --read-only --tmpfs /work:rw,exec,nosuid,nodev,size=$TMPFS --cap-drop=ALL --security-opt no-new-privileges --pids-limit $PIDS --memory $MEM --memory-swap $MEM --cpus $CPUS --user $SUID:$SUID"
goenv="-e HOME=/work/home -e GOCACHE=/work/gocache -e GOPATH=/work/go -e TMPDIR=/work/tmp -e GOTOOLCHAIN=local -e GOWORK=off -e GOFLAGS=-mod=readonly -e GOSUMDB=sum.golang.org -e CGO_ENABLED=$CGO ${GOMEMLIMIT:+-e GOMEMLIMIT=$GOMEMLIMIT}"
setup='mkdir -p /work/src /work/home /work/gocache /work/tmp && tar -x -C /work/src && cd /work/src'
# up: seconds since boot, 10 ms resolution (busybox date has no %N).
# memory.peak counts page cache, which the kernel gives back under pressure; the sampler keeps the largest anon+shmem
# (heap plus tmpfs), the memory a limit really has to cover.
inner='up() { cut -d" " -f1 /proc/uptime; }
( while :; do awk "/^(anon|shmem) /{s+=\$2} END{print s}" /sys/fs/cgroup/memory.stat; sleep 0.25; done > /work/mem.log ) &
sampler=$!
s=$(up); rc=0
go test -json -count=1 -shuffle=on "$@" || rc=$?
e=$(up)
kill $sampler
echo "METRIC wall_s=$(echo "$e $s" | awk "{print \$1-\$2}") exit=$rc peak_bytes=$(cat /sys/fs/cgroup/memory.peak) anon_shmem_peak=$(sort -n /work/mem.log | tail -1) pids_peak=$(cat /sys/fs/cgroup/pids.peak) cpu_usec=$(awk "/^usage_usec/{print \$2}" /sys/fs/cgroup/cpu.stat) throttled=$(awk "/^nr_throttled/{print \$2}" /sys/fs/cgroup/cpu.stat) oom=$(awk "/^oom_kill /{print \$2}" /sys/fs/cgroup/memory.events) tmpfs_mb=$(du -sm /work | cut -f1)" >&2'

case $cmd in
prep) # a named volume is root-owned and the sandbox user is not: hand it to 65532 once
	$RT volume create "$VOL" >/dev/null
	$RT run --rm --network=none --user 0 --cap-drop=ALL --cap-add=CHOWN -v "$VOL":/gomod "$IMAGE" chown $SUID:$SUID /gomod ;;
install) # networked, no repository code runs: go mod download only fetches and verifies against go.sum
	$RT run $common --network="$NET" -v "$VOL":/gomod ${FP:+-v "$FP":/fp:ro} $goenv -e GOMODCACHE=/gomod -e GOPROXY="${GOPROXY:-https://proxy.golang.org}" -e GOVCS='*:off' \
		"$IMAGE" sh -c "$setup && go mod download" ;;
test) # offline: no network at all; the module cache is read-only, or (FP) built in the tmpfs from the proxy layout
	if [ -n "$FP" ]; then mod="-v $FP:/fp:ro -e GOPROXY=file:///fp -e GOMODCACHE=/work/gomod"
	else mod="-v $VOL:/gomod:ro -e GOPROXY=off -e GOMODCACHE=/gomod"; fi
	$RT run $common --network=none $mod $goenv \
		"$IMAGE" sh -c "$setup && $inner" sh "$@" ;;
*) echo "usage: sandbox.sh prep|install|test" >&2; exit 2 ;;
esac
