# Sourced by the scripts that run as root or drive podman: they belong in the throwaway WSL distro m21s0-alma and nowhere else.
# On the PC the `podman` on the PATH may be the Windows client of the Podman machine, which these scripts must never reach.
[ "${WSL_DISTRO_NAME:-}" = m21s0-alma ] || { echo "${0##*/}: runs only inside the throwaway WSL distro m21s0-alma" >&2; exit 1; }
