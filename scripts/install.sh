#!/bin/sh
# Installs the SuiteWard binary, `suiteward`, from a GitHub Release:
#   curl -fsSL https://github.com/IgnisDevNE/SuiteWard/releases/download/<tag>/install.sh | SUITEWARD_VERSION=<tag> sh
# The script is the same file in every release and learns nothing from the URL it came from, so a pre-release needs SUITEWARD_VERSION: without it
# the script installs the latest release, which GitHub never makes a pre-release.
# It downloads the binary of this platform and SHA256SUMS, checks the binary against the SHA-256 that SHA256SUMS lists and, when cosign is
# installed, verifies the keyless signature of SHA256SUMS with the identity that scripts/verify-release.ps1 pins. Without cosign the signature is
# NOT verified: the script says so and prints how to check it on another machine; SUITEWARD_REQUIRE_COSIGN=1 makes that an error instead.
# It installs the binary and runs nothing: not the binary, not sudo.
#
# SUITEWARD_VERSION         a release tag such as v0.3.0-alpha.2 (default: the latest release, which GitHub never makes a pre-release)
# SUITEWARD_INSTALL_DIR     where `suiteward` goes (default: $HOME/.local/bin)
# SUITEWARD_REQUIRE_COSIGN  0 (default) or 1: 1 stops when cosign is not installed; any other value is an error
# SUITEWARD_RELEASE_BASE    only for scripts/install_test.sh: replaces the release download URL, and latest.json under it replaces the GitHub API answer
#
# POSIX sh. Everything is in main, called on the last line: a download that stops half way runs nothing.
set -eu

repo=IgnisDevNE/SuiteWard
# The same identity and issuer as scripts/verify-release.ps1; scripts/install_test.sh checks that the two copies agree.
identity='^https://github\.com/IgnisDevNE/SuiteWard/\.github/workflows/release\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(-(rc|alpha)\.[0-9]+)?$'
issuer='https://token.actions.githubusercontent.com'
tag_pattern='^v[0-9]+\.[0-9]+\.[0-9]+(-(rc|alpha)\.[0-9]+)?$'
tmp=
staged=

say() { printf '%s\n' "$*"; }
warn() { printf '%s\n' "$*" >&2; }
die() { printf 'suiteward install: %s\n' "$*" >&2; exit 1; }
cleanup() {
	if [ -n "$tmp" ]; then rm -rf "$tmp"; fi
	if [ -n "$staged" ]; then rm -f "$staged"; fi
}
# The character set comes first: grep matches line by line, so a value with a newline in it would pass the pattern.
valid_tag() {
	case $1 in '' | *[!A-Za-z0-9.-]*) return 1 ;; esac
	printf '%s\n' "$1" | grep -Eq "$tag_pattern"
}
# fetch URL FILE. stdin is closed: it can be the script itself. Over curl, the default URLs may only be https, redirects included.
fetch() {
	if [ "$downloader" = wget ]; then
		wget -q -O "$2" "$1" < /dev/null
	elif [ -n "${SUITEWARD_RELEASE_BASE:-}" ]; then
		curl -fsSL -o "$2" "$1" < /dev/null
	else
		curl -fsSL --proto '=https' --proto-redir '=https' -o "$2" "$1" < /dev/null
	fi
}

main() {
	trap cleanup EXIT
	trap 'exit 130' INT
	trap 'exit 143' TERM
	trap 'exit 129' HUP
	umask 077

	os=$(uname -s)
	arch=$(uname -m)
	case $os in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) die "unsupported operating system: $os (only Linux and macOS)" ;;
	esac
	case $arch in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) die "unsupported CPU architecture: $arch (only amd64 and arm64)" ;;
	esac

	tag=${SUITEWARD_VERSION:-}
	if [ -n "$tag" ] && ! valid_tag "$tag"; then
		die "SUITEWARD_VERSION must look like v1.2.3, v1.2.3-rc.1 or v1.2.3-alpha.1, not '$tag'"
	fi

	case ${SUITEWARD_REQUIRE_COSIGN:-} in
	'' | 0 | 1) ;;
	*) die "SUITEWARD_REQUIRE_COSIGN must be 0 or 1 (or unset), not '$SUITEWARD_REQUIRE_COSIGN'" ;;
	esac

	if command -v curl > /dev/null 2>&1; then downloader=curl
	elif command -v wget > /dev/null 2>&1; then downloader=wget
	else die "curl or wget is required to download the release"
	fi
	if command -v sha256sum > /dev/null 2>&1; then hasher=sha256sum
	elif command -v shasum > /dev/null 2>&1; then hasher='shasum -a 256'
	else die "sha256sum or shasum is required to check the download"
	fi
	have_cosign=
	if command -v cosign > /dev/null 2>&1; then
		have_cosign=1
	elif [ "${SUITEWARD_REQUIRE_COSIGN:-}" = 1 ]; then
		die "cosign was not found and SUITEWARD_REQUIRE_COSIGN=1 asks for the signature check; install cosign (https://github.com/sigstore/cosign/releases) or unset it"
	fi

	# The staged file proves the directory is writable before anything is downloaded. It sits in the directory itself so that the final mv is atomic.
	dir=${SUITEWARD_INSTALL_DIR:-}
	if [ -z "$dir" ]; then
		[ -n "${HOME:-}" ] || die "HOME is not set: set SUITEWARD_INSTALL_DIR to the directory for suiteward"
		dir=$HOME/.local/bin
	fi
	# mv would put the binary inside a directory of that name instead of replacing anything.
	if [ -d "$dir/suiteward" ]; then
		die "$dir/suiteward is a directory: remove it, or set SUITEWARD_INSTALL_DIR to another directory"
	fi
	(umask 022 && mkdir -p "$dir") 2> /dev/null || true
	staged=$(mktemp "$dir/.suiteward.XXXXXX" 2> /dev/null) || {
		staged=
		die "cannot write to $dir, and this script never uses sudo.
Run it again with SUITEWARD_INSTALL_DIR set to a directory you can write, then copy the binary as an administrator:
  sudo install -m 755 <a directory you can write>/suiteward $dir/suiteward"
	}

	tmp=$(mktemp -d "${TMPDIR:-/tmp}/suiteward-install.XXXXXX")
	base=https://github.com/$repo/releases/download
	latest_url=https://api.github.com/repos/$repo/releases/latest
	if [ -n "${SUITEWARD_RELEASE_BASE:-}" ]; then
		base=${SUITEWARD_RELEASE_BASE%/}
		latest_url=$base/latest.json
		warn "Using SUITEWARD_RELEASE_BASE=$base (for tests only)"
	fi
	pick="set SUITEWARD_VERSION to a release that has it, for example: curl -fsSL https://github.com/$repo/releases/download/<tag>/install.sh | SUITEWARD_VERSION=<tag> sh (the releases are listed at https://github.com/$repo/releases)"

	if [ -z "$tag" ]; then
		fetch "$latest_url" "$tmp/latest.json" || die "could not look up the latest release at $latest_url (GitHub limits anonymous requests); $pick"
		tag=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmp/latest.json" | sed -n 1p)
		valid_tag "$tag" || die "the latest release answer has no SuiteWard release tag ('$tag'); $pick"
		say "Latest release: $tag"
	fi

	asset=suiteward_${tag}_${os}_${arch}
	url=$base/$tag
	fetch "$url/SHA256SUMS" "$tmp/SHA256SUMS" || die "could not download SHA256SUMS of $tag: the release does not exist or predates the installer binaries; $pick"

	if [ -n "$have_cosign" ]; then
		fetch "$url/SHA256SUMS.sigstore.json" "$tmp/SHA256SUMS.sigstore.json" || die "could not download SHA256SUMS.sigstore.json of $tag, the signature of SHA256SUMS"
		(cd "$tmp" && cosign verify-blob --bundle SHA256SUMS.sigstore.json --certificate-identity-regexp "$identity" --certificate-oidc-issuer "$issuer" SHA256SUMS < /dev/null) ||
			die "cosign could not verify the signature of SHA256SUMS (cosign 3.x is needed); nothing was installed"
		say "Verified: SHA256SUMS was signed by the SuiteWard release workflow."
	else
		sums_hash=$($hasher "$tmp/SHA256SUMS" | awk '{ print $1 }')
		warn "SHA256SUMS was NOT verified by signature: cosign is not installed. The binary is checked only against the SHA256SUMS of this same download."
		# BusyBox wget (Alpine, routers) can skip the check of TLS certificates, and then only the signature would have authenticated the download.
		if [ "$downloader" = wget ] && wget --help 2>&1 < /dev/null | grep -qi busybox; then
			warn "The downloader is BusyBox wget, so TLS certificates may not have been verified: install curl or cosign, or check the signature as below."
		fi
		warn "To verify the signature on a machine with cosign 3.x, download $url/SHA256SUMS and $url/SHA256SUMS.sigstore.json there and run, from the release source of $tag:"
		warn "  pwsh ./scripts/verify-release.ps1 -Blob SHA256SUMS -Bundle SHA256SUMS.sigstore.json"
		warn "or, without the source:"
		warn "  cosign verify-blob --bundle SHA256SUMS.sigstore.json --certificate-identity-regexp '$identity' --certificate-oidc-issuer $issuer SHA256SUMS"
		warn "Then compare the hash of the file you verified (verify-release.ps1 prints it; or run sha256sum SHA256SUMS) with this one:"
		warn "SHA256SUMS sha256: $sums_hash"
	fi

	# The rule of verify-release.ps1 -Sums: the file is listed exactly once, with 64 hex digits (the name may carry the * of binary mode).
	expected=$(awk -v name="$asset" '
		NF == 2 { file = $2; sub(/^\*/, "", file); if (file == name) { count++; sum = $1 } }
		END { if (count == 1 && length(sum) == 64 && sum !~ /[^0-9A-Fa-f]/) print tolower(sum) }' "$tmp/SHA256SUMS")
	[ -n "$expected" ] || die "SHA256SUMS of $tag does not list $asset exactly once with a SHA-256: the release has no binary for $os/$arch; $pick"
	fetch "$url/$asset" "$tmp/$asset" || die "could not download $asset from $url: the release has no binary for $os/$arch; $pick"
	# The staged copy is the file that mv puts in place, so it is the one that is hashed.
	cp "$tmp/$asset" "$staged"
	actual=$($hasher "$staged" | awk '{ print $1 }')
	[ "$actual" = "$expected" ] || die "the SHA-256 of $asset ($actual) does not match SHA256SUMS ($expected); nothing was installed"
	chmod 755 "$staged"
	mv -f "$staged" "$dir/suiteward"
	staged=
	say "Installed suiteward $tag ($os/$arch) to $dir/suiteward"

	cmd=suiteward
	case ":$PATH:" in
	*":$dir:"*) ;;
	*)
		cmd=$dir/suiteward
		warn "$dir is not in your PATH: add it, or use the full path below."
		;;
	esac
	say "Nothing has been run. Next:"
	say "  $cmd install            co-located tier: this PC runs the stack with Podman or Docker"
	say "  sudo $cmd install host  isolated tier: an EL9 host, as an administrator; first copy the binary to a path the service account can read, such as /usr/local/bin (install guide, section 10)"
}

main "$@"
