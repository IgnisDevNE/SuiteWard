#!/bin/sh
# What containers of suiteward-exec can reach, per rootless network mode. Needs a listener on the distro's 127.0.0.1:18081
# (systemd-run --unit=m21s0-listener python3 -m http.server 18081 --bind 127.0.0.1) and suiteward's container on 18091 (isolation-users.sh setup).
# usage, as root in the distro: sh probe-podman.sh [network-flag...]     e.g.  sh probe-podman.sh --network=none
D=$(dirname "$0")
. "$D/image.sh"; IMG=$ALPINE
SVC_IP=$(sh "$D/as.sh" suiteward podman inspect m21s0-svc --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}')
echo "# flags: $*   (suiteward's container is $SVC_IP on its own network)"
sh "$D/as.sh" suiteward-exec podman run --rm "$@" "$IMG" sh -c 'getent hosts host.containers.internal; ip -4 route | head -2; ip -4 -o addr | grep -v " lo "' 2>&1 | grep -v "shared mount"
sh "$D/as.sh" suiteward-exec podman run --rm -i "$@" "$IMG" sh -s -- host.containers.internal:18081 host.containers.internal:18091 127.0.0.1:18081 127.0.0.1:18091 \
	GATEWAY:18081 GATEWAY:18091 10.0.2.2:18081 10.0.2.2:18091 "$SVC_IP":8080 169.254.169.254:80 proxy.golang.org:443 < "$D/probe.sh" 2>&1 | grep -v "shared mount"
