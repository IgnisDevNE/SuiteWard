#!/bin/sh
# The executor as (a) a native process of suiteward-exec and (b) a container of suiteward-exec with its own user socket mounted.
# usage, as root in the distro (after alma-setup.sh and isolation-users.sh setup):  sh executor-shapes.sh build | socket | b-run <tar> | b-power | units | state
D=$(dirname "$0")
. "$D/image.sh"; IMG=$ALPINE
exe() { sh "$D/as.sh" suiteward-exec "$@"; }
say() { printf '\n== %s\n' "$*"; }
UID_EXE=$(id -u suiteward-exec)
SOCK=/run/user/$UID_EXE/podman/podman.sock
NONROOT=65532:65532

case $1 in
build) exe podman build -q -t m21s0-executor -f "$D/executor.Containerfile" "$D" ;;
socket) exe systemctl --user enable --now podman.socket; exe ls -l "$SOCK" ;;
b-run) # the executor container runs the sandbox through the socket; the sandbox is a sibling container, not a child
	exe podman run --rm -i --name m21s0-executor --network=pasta:-T,18091 -v "$SOCK":/run/podman.sock -v "$D":/h:ro \
		-e CONTAINER_HOST=unix:///run/podman.sock -e RT=podman-remote -e IMAGE="$IMG" -e VOL=m21s0-gomod -e NET=m21s0-inst \
		localhost/m21s0-executor sh -c 'echo "service via the executor container loopback: $(curl -s --max-time 3 http://127.0.0.1:18091/)"; podman-remote ps --format "sibling-visible: {{.Names}}"; sh /h/sandbox.sh test ./...' < "$2" 2>&1 | grep -v 'shared mount' | grep -E 'service via|sibling|METRIC' ;;
b-nonroot) # a non-root user inside the executor container cannot use a socket owned by the account it maps to
	exe podman run --rm --user "$NONROOT" -v "$SOCK":/run/podman.sock -e HOME=/tmp -e CONTAINER_HOST=unix:///run/podman.sock localhost/m21s0-executor podman-remote ps 2>&1 | grep -v 'shared mount' | head -3 ;;
b-power) # what the socket is worth: a privileged sibling that mounts the account's view of the host
	exe podman run --rm -v "$SOCK":/run/podman.sock -e CONTAINER_HOST=unix:///run/podman.sock localhost/m21s0-executor sh -c '
		podman-remote run --rm --privileged -v /home:/host/home -v /run/user:/host/run "'"$IMG"'" sh -c "ls /host/home; ls -a /host/home/suiteward-exec | tr "\n" " "; ls -a /host/home/suiteward 2>&1 | head -2; ls /host/run/'"$UID_EXE"'/podman | head -2"' 2>&1 | grep -v 'shared mount' ;;
units) # lifecycle: a systemd user unit per shape; linger lets them run with nobody logged in
	exe sh -c 'mkdir -p ~/.config/systemd/user ~/.config/containers/systemd'
	printf '[Unit]\nDescription=executor stand-in, shape (a): a native process of suiteward-exec\n[Service]\nExecStart=/bin/sh -c "while :; do date; sleep 30; done"\nRestart=always\n[Install]\nWantedBy=default.target\n' | exe tee /home/suiteward-exec/.config/systemd/user/m21s0-exec-a.service >/dev/null
	printf '[Unit]\nDescription=executor stand-in, shape (b): a container with the user socket\n[Container]\nImage=localhost/m21s0-executor\nContainerName=m21s0-executor-b\nVolume=%s:/run/podman.sock\nEnvironment=CONTAINER_HOST=unix:///run/podman.sock\nNetwork=pasta:-T,18091\nExec=sh -c "while :; do podman-remote ps -q | wc -l; curl -s --max-time 3 http://127.0.0.1:18091/; sleep 30; done"\n[Service]\nRestart=always\n[Install]\nWantedBy=default.target\n' "$SOCK" | exe tee /home/suiteward-exec/.config/containers/systemd/m21s0-exec-b.container >/dev/null
	exe systemctl --user daemon-reload
	exe systemctl --user enable --now m21s0-exec-a.service
	exe systemctl --user start m21s0-exec-b.service
	exe systemctl --user is-active m21s0-exec-a.service m21s0-exec-b.service podman.socket ;;
state) exe systemctl --user is-active m21s0-exec-a.service m21s0-exec-b.service podman.socket; loginctl list-users --no-legend; exe podman logs --tail 2 m21s0-executor-b | cut -c1-150; exe podman ps --format "{{.Names}} {{.Status}}" ;;
crash) # Restart=always brings a killed executor back, in both shapes
	for s in m21s0-exec-a m21s0-exec-b; do exe systemctl --user kill -s KILL "$s.service"; done
	sleep 8
	for s in m21s0-exec-a m21s0-exec-b; do exe systemctl --user show "$s.service" -p ActiveState -p NRestarts; done ;;
esac
