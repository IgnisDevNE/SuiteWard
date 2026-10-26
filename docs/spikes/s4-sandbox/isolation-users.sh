#!/bin/sh
# What the second account (suiteward-exec) can see of the first (suiteward): containers, volumes, secrets, files, sockets, processes.
# usage, as root in the distro: sh isolation-users.sh setup | check | teardown
D=$(dirname "$0")
. "$D/image.sh"; IMG=$ALPINE
svc() { sh "$D/as.sh" suiteward "$@"; }
exe() { sh "$D/as.sh" suiteward-exec "$@"; }
say() { printf '\n== %s\n' "$*"; }

case $1 in
setup) # the service account owns a network, a secret, a volume and a container that publishes a port on the host loopback
	svc podman network create suiteward
	printf 'value-only-suiteward-may-read' | svc podman secret create m21s0-secret -
	svc podman volume create m21s0-data
	svc podman run -d --name m21s0-svc --network suiteward -v m21s0-data:/data --secret m21s0-secret -e BANNER=svc-of-suiteward \
		-p 127.0.0.1:18091:8080 "$IMG" sh -c "$(cat "$D/listener.sh")"
	svc podman exec m21s0-svc sh -c 'echo marker > /data/marker; cat /run/secrets/m21s0-secret; echo' ;;
check)
	say "suiteward sees"; svc podman ps -a --format '{{.Names}}'; svc podman volume ls --format '{{.Name}}'; svc podman secret ls --format '{{.Name}}'
	say "suiteward-exec: containers, volumes, secrets, networks"
	exe podman ps -a --format '{{.Names}}'; exe podman volume ls --format 'volume {{.Name}}'; exe podman secret ls --format 'secret {{.Name}}'; exe podman network ls --format 'network {{.Name}}'
	say "suiteward-exec: exec into, inspect and read"
	exe podman exec m21s0-svc id 2>&1 | head -2
	exe podman volume inspect m21s0-data 2>&1 | head -2
	exe podman secret inspect --showsecret m21s0-secret 2>&1 | head -2
	say "suiteward-exec: files and sockets of suiteward"
	exe ls /home/suiteward/.local/share/containers 2>&1 | head -2
	exe ls /run/user/2000 2>&1 | head -2
	exe podman --remote --url unix:///run/user/2000/podman/podman.sock ps 2>&1 | head -2
	exe sh -c 'ls /home/suiteward 2>&1 | head -1; stat -c "%U %a %n" /home/suiteward /home/suiteward-exec'
	say "id mapping: the two accounts' subuid ranges, and who owns the data of suiteward's volume on the host"
	cat /etc/subuid
	stat -c '%u:%g %n' /home/suiteward/.local/share/containers/storage/volumes/m21s0-data/_data/marker
	say "processes: what suiteward-exec can see of suiteward's container"
	exe sh -c 'ps -eo user:12,pid,args | grep -E "m21s0|nc -lk" | grep -v grep | cut -c1-160'
	pid=$(pgrep -f 'nc -lk -p 8080' | head -1)
	exe sh -c "cat /proc/$pid/environ 2>&1 | head -c 80; echo; cat /proc/$pid/cmdline 2>&1 | tr '\\0' ' ' | head -c 80; echo; ls /proc/$pid/root 2>&1 | head -1"
	say "the published port on the host loopback, from a native process of suiteward-exec"
	exe sh -c 'printf "GET / HTTP/1.0\r\n\r\n" | timeout 3 bash -c "exec 3<>/dev/tcp/127.0.0.1/18091; cat >&3; cat <&3" | tail -1' ;;
teardown)
	svc podman rm -f m21s0-svc; svc podman volume rm m21s0-data; svc podman secret rm m21s0-secret; svc podman network rm suiteward ;;
esac
