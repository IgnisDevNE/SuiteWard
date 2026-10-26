#!/bin/sh
# WSL only. After `wsl --terminate` the old user manager's processes can outlive the distro in the VM's cgroup tree, and the new manager then
# fails with 219/CGROUP ("Failed to kill control group ... Input/output error"). Kill the stale cgroup and start the manager again.
# usage, as root in the distro: sh wsl-fix-user-cgroup.sh <uid>...
for u in "$@"; do
	if ! systemctl is-active -q "user@$u.service"; then
		printf 1 | tee "/sys/fs/cgroup/user.slice/user-$u.slice/user@$u.service/cgroup.kill" >/dev/null 2>&1
		sleep 2; systemctl reset-failed "user@$u.service"; systemctl start "user@$u.service"; sleep 3
	fi
done
ls /run/user
