#!/bin/sh
# Can the executor see, from outside the sandbox, that the cgroup OOM killer fired? A kill of a child leaves the container running and exiting 0.
# usage: RT=docker|podman IMAGE=<ref> sh oom-signal.sh
. "$(dirname "$0")/image.sh"; D=$(dirname "$0")
RT=${RT:-docker}; IMAGE=${IMAGE:-$ALPINE}
if [ "$RT" = podman ]; then . "$D/guard.sh"; fi   # RT=podman only inside the throwaway distro m21s0-alma
export MSYS_NO_PATHCONV=1
log=$(mktemp)
HOG='a=$(head -c 150000000 /dev/zero | tr "\0" x); echo hog-survived'
case $RT in docker) fmt="{{.Type}} {{.Action}} {{.Actor.Attributes.name}}" ;; *) fmt="{{.Type}} {{.Status}} {{.Name}}" ;; esac
$RT events --format "$fmt" > "$log" 2>&1 &
ev=$!; sleep 1
echo "--- child is the victim"; $RT run --name m21s0-oom-child --memory 64m --memory-swap 64m -e HOG="$HOG" "$IMAGE" sh -c 'sh -c "$HOG"; echo "init survived, child status=$?"'
echo "--- init is the victim"; $RT run --name m21s0-oom-init --memory 64m --memory-swap 64m -e HOG="$HOG" "$IMAGE" sh -c 'eval "$HOG"; echo not-reached'; echo "client exit=$?"
sleep 1; kill $ev
grep -E "m21s0-oom" "$log" | grep -E " (oom|died|die) "
$RT inspect m21s0-oom-child m21s0-oom-init --format '{{.Name}} OOMKilled={{.State.OOMKilled}} ExitCode={{.State.ExitCode}}'
$RT rm m21s0-oom-child m21s0-oom-init >/dev/null; rm -f "$log"
