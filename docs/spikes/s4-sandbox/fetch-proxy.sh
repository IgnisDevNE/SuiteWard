#!/bin/sh
# A PROTOTYPE of the executor-side fetch, to measure the idea. It reads go.sum and downloads, from proxy.golang.org only, the files
# `go mod download` will ask for into a directory laid out as a module proxy (<module>/@v/<version>.info|mod|zip). The sandbox then installs
# from it with GOPROXY=file:// and no network; go checks every file against go.sum (the candidate's own: this is as strong as a networked
# install, not stronger).
# Shortcuts that T5 must NOT copy:
#   - the loose character filter below stands for golang.org/x/mod: module.CheckPath, EscapePath, EscapeVersion and semver, before a module or
#     version becomes a path or a URL;
#   - `curl -L` follows a redirect to any host (T5 allows storage.googleapis.com only, in an http.Client with CheckRedirect);
#   - `|| echo FAILED` only prints: a failed or short download must fail the job (as an infrastructure failure, retried), never be skipped;
#   - no limit on the number of files or bytes, and the files land on a host directory (T5 puts them in the sandbox's stdin tar).
# usage: fetch-proxy.sh <go.sum> <outdir>      (Git Bash: leave MSYS_NO_PATHCONV unset here, curl.exe is a native program and needs its paths converted)
set -eu
sum=$1; out=$2; base=https://proxy.golang.org
# one line per file: "<module> <version> <ext>"  (a "/go.mod" line of go.sum needs only the .mod, the other needs the zip)
awk '{ v=$2; ext="zip"; if (v ~ /\/go\.mod$/) { sub(/\/go\.mod$/, "", v); ext="mod" } print $1, v, ext; print $1, v, "info" }' "$sum" | sort -u |
	awk '$1 !~ /^[A-Za-z0-9._~\/+-]+$/ || $2 !~ /^[A-Za-z0-9._+-]+$/ || $1 ~ /\.\./ || $1 ~ /^\// { print "refused go.sum entry: " $0 > "/dev/stderr"; bad=1; next } { print } END { exit bad }' |
	sed -E 's/([A-Z])/!\L\1/g' |
	while read -r mod ver ext; do
		f="$out/$mod/@v/$ver.$ext"; mkdir -p "$(dirname "$f")"
		echo "$base/$mod/@v/$ver.$ext $f"
	done | xargs -P 8 -n 2 sh -c 'curl -sSfL --retry 2 -o "$1" "$0" || echo "FAILED $0" >&2'
