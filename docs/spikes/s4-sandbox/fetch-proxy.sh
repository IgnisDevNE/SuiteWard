#!/bin/sh
# Prototype of the executor-side fetch: reads go.sum and downloads, from proxy.golang.org only, the files `go mod download` will ask for
# into a directory laid out as a module proxy (<module>/@v/<version>.info|mod|zip). The sandbox then installs from it with GOPROXY=file://
# and no network; go checks every file against go.sum, so this script has to be trusted for availability, not for content.
# usage: fetch-proxy.sh <go.sum> <outdir>      (Git Bash: leave MSYS_NO_PATHCONV unset here, curl.exe is a native program and needs its paths converted)
set -eu
sum=$1; out=$2; base=https://proxy.golang.org
# one line per file: "<escaped module> <version> <ext>"  (a "/go.mod" line of go.sum needs only the .mod, the other needs the zip)
awk '{ v=$2; ext="zip"; if (v ~ /\/go\.mod$/) { sub(/\/go\.mod$/, "", v); ext="mod" } print $1, v, ext; print $1, v, "info" }' "$sum" | sort -u |
	sed -E 's/([A-Z])/!\L\1/g' |
	while read -r mod ver ext; do
		f="$out/$mod/@v/$ver.$ext"; mkdir -p "$(dirname "$f")"
		echo "$base/$mod/@v/$ver.$ext $f"
	done | xargs -P 8 -n 2 sh -c 'curl -sSfL --retry 2 -o "$1" "$0" || echo "FAILED $0" >&2'
