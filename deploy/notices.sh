#!/bin/sh
# Writes the license texts of the Go standard library and of every module linked into ./cmd/suiteward for GOOS/GOARCH to the file named by $1.
# Fails, naming the module, when a module has no license file (D-RELEASE).
set -eu
out=$1
main=$(go list -m)
cp "$(go env GOROOT)/LICENSE" "$out"
# A command substitution, so a failing go list stops the script (POSIX sh has no pipefail).
modules=$(go list -deps -f '{{with .Module}}{{.Path}} {{.Version}} {{.Dir}}{{end}}' ./cmd/suiteward)
printf '%s\n' "$modules" | sort -u | while read -r path version dir; do
	[ "$path" = "$main" ] && continue
	files=$(find "$dir" -maxdepth 1 -type f \( -iname 'LICENSE*' -o -iname 'LICENCE*' -o -iname 'COPYING*' -o -iname 'NOTICE*' \) | sort)
	[ -n "$files" ] || { echo "notices: module $path $version has no license file" >&2; exit 1; }
	for file in $files; do
		printf '\n\n================================================================\n%s %s (%s)\n================================================================\n\n' "$path" "$version" "${file##*/}" >>"$out"
		cat "$file" >>"$out"
	done
done
