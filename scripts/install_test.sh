#!/bin/sh
# Self-test for install.sh. The installer runs under a hermetic PATH of wrappers for the few tools it needs, a fake uname and, per case, a fake
# cosign or wget, against a fake release directory served through file:// URLs, so no network, no real cosign and no host tool changes a result.
# Run: sh scripts/install_test.sh   (INSTALL_TEST_SHELL=dash runs the installer with another shell than sh)
set -u
here=$(cd "$(dirname "$0")" && pwd)
installer=$here/install.sh
verify=$here/verify-release.ps1
shell=$(command -v "${INSTALL_TEST_SHELL:-sh}") || { echo "FAIL: ${INSTALL_TEST_SHELL:-sh} was not found" >&2; exit 1; }
unset SUITEWARD_VERSION SUITEWARD_INSTALL_DIR SUITEWARD_REQUIRE_COSIGN SUITEWARD_RELEASE_BASE
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
trap 'exit 1' INT TERM
bin=$tmp/bin release=$tmp/release dest=$tmp/dest logs=$tmp/log
mkdir "$bin" "$release" "$logs"
if command -v cygpath >/dev/null 2>&1; then root=$(cygpath -m "$tmp"); else root=$tmp; fi # native curl on Windows cannot open a POSIX file:// path
base=file://$root/release
nl='
'
msys=
case $(uname -s) in MINGW* | MSYS* | CYGWIN*) msys=1 ;; esac

fails=0
fail() { echo "FAIL: $*" >&2; fails=$((fails + 1)); }
sha256_of() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | awk '{ print $1 }'; }
wrap() { real=$(command -v "$1") || return 0; printf '#!/bin/sh\nexec "%s" "$@"\n' "$real" > "$bin/$1"; chmod +x "$bin/$1"; }
for tool in mktemp mkdir rm mv cp chmod awk sed grep tr cat sha256sum shasum curl; do wrap "$tool"; done
cat > "$bin/uname" <<'EOF'
#!/bin/sh
case $1 in -s) echo "${FAKE_UNAME_S:-Linux}" ;; -m) echo "${FAKE_UNAME_M:-x86_64}" ;; *) exit 2 ;; esac
EOF
# The installer never uses sudo by itself: this one records a call, and the end of the test refuses one.
cat > "$bin/sudo" <<'EOF'
#!/bin/sh
echo "$@" >> "$FAKE_LOGS/sudo"
exit 99
EOF
cat > "$tmp/cosign" <<'EOF'
#!/bin/sh
cat > /dev/null
for arg; do echo "$arg"; done > "$FAKE_LOGS/cosign"
exit "${FAKE_COSIGN_EXIT:-0}"
EOF
cat > "$tmp/wget" <<'EOF'
#!/bin/sh
# wget -q -O FILE URL for file:// URLs, which neither GNU wget nor busybox wget reads.
out= url=
while [ $# -gt 0 ]; do case $1 in -O) out=$2; shift ;; -q) ;; -*) exit 2 ;; *) url=$1 ;; esac; shift; done
echo "$url" >> "$FAKE_LOGS/wget"
cp "${url#file://}" "$out" 2>/dev/null || exit 8
EOF
chmod +x "$bin/uname" "$bin/sudo" "$tmp/cosign" "$tmp/wget"
add() { cp "$tmp/$1" "$bin/$1"; }
drop() { rm -f "$bin/$1"; }

# mkrelease TAG: the four platform binaries and the Windows one (small shell scripts that print who they are), SHA256SUMS in sha256sum format, and the bundle.
mkrelease() {
	mkdir -p "$release/$1"
	: > "$release/$1/SHA256SUMS"
	for platform in linux_amd64 linux_arm64 darwin_arm64 darwin_amd64 windows_amd64.exe; do
		file=suiteward_$1_$platform
		printf '#!/bin/sh\necho "suiteward %s %s"\n' "$1" "$platform" > "$release/$1/$file"
		printf '%s  %s\n' "$(sha256_of "$release/$1/$file")" "$file" >> "$release/$1/SHA256SUMS"
	done
	echo bundle > "$release/$1/SHA256SUMS.sigstore.json"
}

# run [VAR=value ...]: run the installer for v1.2.3 on linux/amd64 under the hermetic PATH with a closed stdin (pipe=1: the script itself on stdin, as in
# curl | sh). It sets rc and out (stdout and stderr), and fails when a temporary directory is left behind.
run_keep() {
	rm -f "$logs/cosign" "$logs/wget"
	rm -rf "$tmp/t"
	mkdir "$tmp/t"
	if [ -n "${pipe:-}" ]; then
		env PATH="$bin" HOME="$tmp/home" TMPDIR="$tmp/t" FAKE_LOGS="$logs" SUITEWARD_RELEASE_BASE="$base" SUITEWARD_INSTALL_DIR="$dest" SUITEWARD_VERSION=v1.2.3 "$@" "$shell" < "$installer" > "$tmp/out" 2>&1
	else
		env PATH="$bin" HOME="$tmp/home" TMPDIR="$tmp/t" FAKE_LOGS="$logs" SUITEWARD_RELEASE_BASE="$base" SUITEWARD_INSTALL_DIR="$dest" SUITEWARD_VERSION=v1.2.3 "$@" "$shell" "$installer" < /dev/null > "$tmp/out" 2>&1
	fi
	rc=$?
	out=$(cat "$tmp/out")
	[ -z "$(ls -A "$tmp/t")" ] || fail "$name: a temporary directory was left behind: $(ls -A "$tmp/t")"
}
run() { rm -rf "$dest" "$tmp/home"; mkdir "$dest"; run_keep "$@"; }
want_ok() { [ "$rc" -eq 0 ] || fail "$name: exit $rc, wanted 0: $out"; }
# A refused install leaves nothing behind in the install directory, not even a staged file.
want_refused() {
	[ "$rc" -ne 0 ] || fail "$name: exit 0, wanted a failure: $out"
	[ -z "$(ls -A "$dest")" ] || fail "$name: the install directory is not empty after a refusal: $(ls -A "$dest")"
}
has() { case $out in *"$1"*) ;; *) fail "$name: the output lacks '$1': $out" ;; esac; }
lacks() { case $out in *"$1"*) fail "$name: the output has '$1': $out" ;; esac; }
installed() { [ "$("${2:-$dest}/suiteward" 2> /dev/null)" = "suiteward $1" ] || fail "$name: ${2:-$dest}/suiteward is not the $1 binary"; }
# sums_without TAG: the lines of the SHA256SUMS of TAG that do not name linux_amd64, in $tmp/sums, so a case can add its own line for it.
sums_without() { grep -v "suiteward_$1_linux_amd64$" "$release/$1/SHA256SUMS" > "$tmp/sums"; }
quoted() { grep "$2" "$1" | tr -d '\r' | sed "s/^[^']*'//; s/'[^']*\$//"; }
cosign_arg() { awk -v key="$1" '$0 == key { getline; print; exit }' "$logs/cosign"; }

identity=$(quoted "$verify" '^[$]identity = ')
issuer=$(quoted "$verify" '^[$]issuer = ')
[ -n "$identity" ] && [ -n "$issuer" ] || fail "the identity or the issuer was not found in verify-release.ps1"
name="the pinned identity"
[ "$(quoted "$installer" "^identity=")" = "$identity" ] || fail "$name: install.sh and verify-release.ps1 disagree about the certificate identity"
[ "$(quoted "$installer" "^issuer=")" = "$issuer" ] || fail "$name: install.sh and verify-release.ps1 disagree about the OIDC issuer"
name="syntax"
"$shell" -n "$installer" || fail "$name: install.sh does not parse with $shell"
! grep -qw eval "$installer" || fail "$name: install.sh must never evaluate downloaded content"

mkrelease v1.2.3
sums=$(sha256_of "$release/v1.2.3/SHA256SUMS")

name="success without cosign"
run
want_ok; installed "v1.2.3 linux_amd64"
[ -n "$msys" ] || [ "$(ls -l "$dest/suiteward" | awk '{ print substr($1, 1, 10) }')" = "-rwxr-xr-x" ] || fail "$name: the binary is not mode 755"
[ "$(ls -A "$dest")" = suiteward ] || fail "$name: the install directory holds more than the binary: $(ls -A "$dest")"
grep -qx "SHA256SUMS sha256: $sums" "$tmp/out" || fail "$name: no 'SHA256SUMS sha256: $sums' line: $out"
has "NOT verified by signature"
has "cosign verify-blob --bundle SHA256SUMS.sigstore.json --certificate-identity-regexp '$identity' --certificate-oidc-issuer $issuer SHA256SUMS"
has "verify-release.ps1 -Blob SHA256SUMS -Bundle SHA256SUMS.sigstore.json"
has "Using SUITEWARD_RELEASE_BASE=$base"
has "$dest is not in your PATH"
has "$dest/suiteward install"
has "install host"

name="success with cosign"
add cosign
run
want_ok; installed "v1.2.3 linux_amd64"
has "Verified: SHA256SUMS was signed by the SuiteWard release workflow"
lacks "NOT verified"
[ "$(sed -n 1p "$logs/cosign")" = verify-blob ] || fail "$name: cosign was not called as 'verify-blob': $(cat "$logs/cosign")"
[ "$(cosign_arg --bundle)" = SHA256SUMS.sigstore.json ] || fail "$name: the bundle was not passed: $(cat "$logs/cosign")"
[ "$(cosign_arg --certificate-identity-regexp)" = "$identity" ] || fail "$name: the identity was not the pinned one: $(cat "$logs/cosign")"
[ "$(cosign_arg --certificate-oidc-issuer)" = "$issuer" ] || fail "$name: the issuer was not the pinned one: $(cat "$logs/cosign")"
[ "$(tail -n 1 "$logs/cosign")" = SHA256SUMS ] || fail "$name: cosign must verify SHA256SUMS: $(cat "$logs/cosign")"

name="success through the curl pipe (cosign reads stdin)"
pipe=1 run
want_ok; installed "v1.2.3 linux_amd64"
has "Verified: SHA256SUMS"

name="a cosign failure stops the install"
run FAKE_COSIGN_EXIT=1
want_refused
has "cosign could not verify the signature of SHA256SUMS"

name="cosign without the bundle"
mkrelease v1.2.5
rm "$release/v1.2.5/SHA256SUMS.sigstore.json"
run SUITEWARD_VERSION=v1.2.5
want_refused
has "SHA256SUMS.sigstore.json"
[ ! -e "$logs/cosign" ] || fail "$name: cosign ran without a bundle"

name="SUITEWARD_REQUIRE_COSIGN without cosign"
drop cosign
run SUITEWARD_REQUIRE_COSIGN=1
want_refused
has "SUITEWARD_REQUIRE_COSIGN=1"
has "cosign"
name="SUITEWARD_REQUIRE_COSIGN with cosign"
add cosign
run SUITEWARD_REQUIRE_COSIGN=1
want_ok; installed "v1.2.3 linux_amd64"
drop cosign

name="a wrong hash"
mkrelease v1.2.4
sums_without v1.2.4
printf '%s  suiteward_v1.2.4_linux_amd64\n' 0000000000000000000000000000000000000000000000000000000000000000 >> "$tmp/sums"
cp "$tmp/sums" "$release/v1.2.4/SHA256SUMS"
run SUITEWARD_VERSION=v1.2.4
want_refused
has "does not match SHA256SUMS"

name="a binary that SHA256SUMS lists twice"
mkrelease v1.2.6
grep "suiteward_v1.2.6_linux_amd64$" "$release/v1.2.6/SHA256SUMS" >> "$release/v1.2.6/SHA256SUMS"
run SUITEWARD_VERSION=v1.2.6
want_refused
has "exactly once"

name="a binary that SHA256SUMS does not list"
mkrelease v1.2.7
sums_without v1.2.7
cp "$tmp/sums" "$release/v1.2.7/SHA256SUMS"
run SUITEWARD_VERSION=v1.2.7
want_refused
has "exactly once"
has "SUITEWARD_VERSION"

name="a malformed hash"
mkrelease v1.2.10
sums_without v1.2.10
printf 'abc123  suiteward_v1.2.10_linux_amd64\n' >> "$tmp/sums"
cp "$tmp/sums" "$release/v1.2.10/SHA256SUMS"
run SUITEWARD_VERSION=v1.2.10
want_refused
has "exactly once"

name="a hash line in binary mode"
mkrelease v1.2.11
sed 's/  suiteward_v1.2.11_linux_amd64$/ *suiteward_v1.2.11_linux_amd64/' "$release/v1.2.11/SHA256SUMS" > "$tmp/sums"
cp "$tmp/sums" "$release/v1.2.11/SHA256SUMS"
run SUITEWARD_VERSION=v1.2.11
want_ok; installed "v1.2.11 linux_amd64"

name="a missing binary file"
mkrelease v1.2.8
rm "$release/v1.2.8/suiteward_v1.2.8_linux_amd64"
run SUITEWARD_VERSION=v1.2.8
want_refused
has "could not download suiteward_v1.2.8_linux_amd64"
has "SUITEWARD_VERSION"

name="a release that does not exist"
run SUITEWARD_VERSION=v9.9.9
want_refused
has "v9.9.9"
has "SUITEWARD_VERSION"

name="pre-release tags"
mkrelease v1.2.3-rc.1
run SUITEWARD_VERSION=v1.2.3-rc.1
want_ok; installed "v1.2.3-rc.1 linux_amd64"
mkrelease v1.2.3-alpha.12
run SUITEWARD_VERSION=v1.2.3-alpha.12
want_ok; installed "v1.2.3-alpha.12 linux_amd64"

# The same tags verify-release.ps1 and release.yml refuse, plus the ones that would reach a URL or a file name.
for bad in 1.2.3 v1.2 v1.2.3.4 latest main ../v1.2.3 v1.2.3/x vx.y.z v1.2.3-alpha v1.2.3-alpha. v1.2.3-alpha.1x v1.2.3-ALPHA.1 v1.2.3-alpha.1-rc.1 v1.2.3-rc v1.2.3-beta.1 "v1.2.3${nl}junk" "v1.2.3 " "v1.2.3;id"; do
	name="the invalid version '$bad'"
	run "SUITEWARD_VERSION=$bad"
	want_refused
	has "SUITEWARD_VERSION must look like"
done

for platform in "Linux x86_64 linux_amd64" "Linux aarch64 linux_arm64" "Linux arm64 linux_arm64" "Darwin arm64 darwin_arm64" "Darwin x86_64 darwin_amd64"; do
	set -- $platform
	name="platform $1 $2"
	run FAKE_UNAME_S="$1" FAKE_UNAME_M="$2"
	want_ok; installed "v1.2.3 $3"
done
for platform in "Linux riscv64" "Linux i686" "FreeBSD amd64" "MINGW64_NT-10.0 x86_64"; do
	set -- $platform
	name="unsupported platform $1 $2"
	run FAKE_UNAME_S="$1" FAKE_UNAME_M="$2"
	want_refused
	has "unsupported"
	case $1 in FreeBSD | MINGW*) has "$1" ;; *) has "$2" ;; esac
done

name="a directory that does not exist yet is created"
run SUITEWARD_INSTALL_DIR="$dest/new/bin"
want_ok; installed "v1.2.3 linux_amd64" "$dest/new/bin"
name="an existing binary is replaced"
run
printf 'old\n' > "$dest/suiteward"
run_keep SUITEWARD_VERSION=v1.2.3-rc.1
want_ok; installed "v1.2.3-rc.1 linux_amd64"
[ "$(ls -A "$dest")" = suiteward ] || fail "$name: the install directory holds more than the binary: $(ls -A "$dest")"
name="the default directory is under HOME"
run SUITEWARD_INSTALL_DIR=
want_ok; installed "v1.2.3 linux_amd64" "$tmp/home/.local/bin"
[ -n "$msys" ] || [ "$(ls -ld "$tmp/home/.local/bin" | awk '{ print substr($1, 1, 10) }')" = "drwxr-xr-x" ] || fail "$name: the created directory is not mode 755"
name="no HOME and no install directory"
run SUITEWARD_INSTALL_DIR= HOME=
want_refused
has "SUITEWARD_INSTALL_DIR"
name="the directory is in PATH"
run PATH="$bin:$dest"
want_ok
lacks "not in your PATH"

# The directory is checked before anything is downloaded: the base here does not exist, and the message is still about the directory.
name="a directory that cannot be written"
: > "$tmp/notadir"
run SUITEWARD_INSTALL_DIR="$tmp/notadir/sub" SUITEWARD_RELEASE_BASE=file:///nonexistent
[ "$rc" -ne 0 ] || fail "$name: exit 0, wanted a failure: $out"
has "$tmp/notadir/sub"
has "never uses sudo"
has "sudo install -m 755 <a directory you can write>/suiteward $tmp/notadir/sub/suiteward"
lacks "download"
if [ -z "$msys" ] && [ "$(id -u)" != 0 ]; then
	name="a read-only directory"
	rm -rf "$dest" "$tmp/home"
	mkdir "$dest"
	chmod 555 "$dest"
	run_keep
	chmod 755 "$dest"
	has "sudo install -m 755 <a directory you can write>/suiteward $dest/suiteward"
	want_refused
fi

name="wget instead of curl"
drop curl
add wget
run
want_ok; installed "v1.2.3 linux_amd64"
grep -q "/v1.2.3/suiteward_v1.2.3_linux_amd64$" "$logs/wget" || fail "$name: wget did not download the binary: $(cat "$logs/wget")"
name="neither curl nor wget"
drop wget
run
want_refused
has "curl or wget"
wrap curl

if command -v shasum >/dev/null 2>&1; then
	name="shasum instead of sha256sum"
	drop sha256sum
	run
	want_ok; installed "v1.2.3 linux_amd64"
fi
name="neither sha256sum nor shasum"
drop sha256sum
drop shasum
run
want_refused
has "sha256sum or shasum"
wrap sha256sum
wrap shasum

# The latest release comes from the releases/latest answer of the GitHub API, here latest.json under the base.
name="latest release, pretty JSON"
printf '{\n  "url": "https://api.github.com/repos/IgnisDevNE/SuiteWard/releases/1",\n  "tag_name": "v1.2.3",\n  "name": "v1.2.3",\n  "prerelease": false\n}\n' > "$release/latest.json"
run SUITEWARD_VERSION=
want_ok; installed "v1.2.3 linux_amd64"
has "Latest release: v1.2.3"
name="latest release, compact JSON"
printf '{"url":"https://api.github.com/repos/IgnisDevNE/SuiteWard/releases/1","tag_name":"v1.2.3-rc.1","name":"x"}' > "$release/latest.json"
run SUITEWARD_VERSION=
want_ok; installed "v1.2.3-rc.1 linux_amd64"
name="latest release without the installer binaries"
printf '{"tag_name": "v0.1.3"}\n' > "$release/latest.json"
run SUITEWARD_VERSION=
want_refused
has "v0.1.3"
has "SUITEWARD_VERSION"
mkdir "$release/v0.1.4"
printf '%s  somethingelse\n' "$(sha256_of "$release/latest.json")" > "$release/v0.1.4/SHA256SUMS"
printf '{"tag_name": "v0.1.4"}\n' > "$release/latest.json"
run SUITEWARD_VERSION=
want_refused
has "SUITEWARD_VERSION"
name="latest release with an answer that has no tag"
printf '{"message": "API rate limit exceeded"}\n' > "$release/latest.json"
run SUITEWARD_VERSION=
want_refused
has "SUITEWARD_VERSION"
name="latest release with a tag that is not a release tag"
printf '{"tag_name": "../../x"}\n' > "$release/latest.json"
run SUITEWARD_VERSION=
want_refused
has "SUITEWARD_VERSION"
name="latest release that cannot be read"
rm "$release/latest.json"
run SUITEWARD_VERSION=
want_refused
has "SUITEWARD_VERSION"

# A download that stops anywhere runs nothing, because the whole script is one function that is called on its last line.
lines=$(wc -l < "$installer")
cut=5
while [ "$cut" -lt "$lines" ]; do
	name="the script cut after $cut of $lines lines"
	rm -rf "$dest" "$tmp/t"
	mkdir "$dest" "$tmp/t"
	sed -n "1,${cut}p" "$installer" > "$tmp/cut.sh"
	env PATH="$bin" HOME="$tmp/home" TMPDIR="$tmp/t" FAKE_LOGS="$logs" SUITEWARD_RELEASE_BASE="$base" SUITEWARD_INSTALL_DIR="$dest" SUITEWARD_VERSION=v1.2.3 "$shell" < "$tmp/cut.sh" > "$tmp/out" 2>&1
	[ -z "$(ls -A "$dest")" ] && [ -z "$(ls -A "$tmp/t")" ] || fail "$name: it installed or left a file: $(ls -A "$dest" "$tmp/t")"
	cut=$((cut + 7))
done

name="sudo"
[ ! -e "$logs/sudo" ] || fail "$name: the installer called sudo: $(cat "$logs/sudo")"

[ "$fails" -eq 0 ] || { echo "$fails install.sh check(s) failed" >&2; exit 1; }
echo "install.sh checks passed."
