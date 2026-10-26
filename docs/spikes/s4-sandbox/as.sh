#!/bin/sh
# Runs a command as another user inside that user's systemd session environment (podman needs XDG_RUNTIME_DIR and the user bus).
# usage, as root: sh as.sh <user> <command...>
. "$(dirname "$0")/guard.sh"
u=$1; shift; id=$(id -u "$u")
exec runuser -u "$u" -- env HOME="/home/$u" XDG_RUNTIME_DIR="/run/user/$id" DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$id/bus" "$@"
