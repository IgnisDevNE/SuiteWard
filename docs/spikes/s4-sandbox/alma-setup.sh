#!/bin/sh
# Prepares the throwaway AlmaLinux 9 WSL distro (named m21s0-alma, systemd on): rootless Podman, two accounts, linger, cpu delegation.
# usage, as root in the distro: sh alma-setup.sh
set -eu
D=$(dirname "$0")
. "$D/guard.sh"; . "$D/image.sh"; IMG=$ALPINE

dnf -y install podman iproute procps-ng policycoreutils passt

# EL9 delegates only memory and pids to user managers, and a rootless `podman run --cpus` then fails (crun: controller `cpu` is not available).
# Written before the accounts exist, so their managers start with the delegation; on an account that already runs, it applies after a restart of its manager.
mkdir -p /etc/systemd/system/user@.service.d
printf '[Service]\nDelegate=cpu cpuset io memory pids\n' | tee /etc/systemd/system/user@.service.d/delegate.conf
systemctl daemon-reload

# WSL reserves uid 1000 (WSLg): a user manager for it fails with 219/CGROUP, so the service account gets 2000.
useradd -m -u 2000 suiteward
useradd -m -u 1001 suiteward-exec
loginctl enable-linger suiteward suiteward-exec
systemctl start user@2000.service user@1001.service

for u in suiteward suiteward-exec; do sh "$D/as.sh" "$u" podman pull -q "$IMG"; done
