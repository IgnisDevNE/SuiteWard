#!/bin/sh
# Reproduces what breaks when one flag of the sandbox run is left out, on the small module. Each case prints the first lines of go's answer.
# usage: IMAGE=<ref> SMALL=<dir written by smallmod.sh> sh breaks.sh      (the volume m21s0-gomod must hold the small module's cache: sandbox.sh prep, install)
set -u
. "$(dirname "$0")/image.sh"
IMAGE=${IMAGE:-$ALPINE}; SMALL=${SMALL:?}; VOL=${VOL:-m21s0-gomod}
export MSYS_NO_PATHCONV=1
EXEC=/work:rw,exec,nosuid,nodev,size=1g
FULL="-e HOME=/work/home -e GOCACHE=/work/gocache -e GOMODCACHE=/gomod -e GOPATH=/work/go -e TMPDIR=/work/tmp"

# try <label> <module dir> <tmpfs spec> <extra docker flags>  (the module goes in as a tar on stdin)
try() {
	label=$1; dir=$2; tmp=$3; shift 3
	printf '== %s\n' "$label"
	tar -C "$dir" -cf - . | docker run --rm -i --read-only --cap-drop=ALL --security-opt no-new-privileges --user 65532:65532 --network=none \
		-v "$VOL":/gomod:ro --tmpfs "$tmp" -e GOPROXY=off -e GOFLAGS=-mod=readonly -e GOTOOLCHAIN=local -e GOWORK=off "$@" "$IMAGE" \
		sh -c 'mkdir -p /work/src /work/home /work/gocache /work/tmp && tar -x -C /work/src && cd /work/src && go test -count=1 -run "TestAdd|TestWritesTemp" ./... 2>&1 | head -4'
}

try "A: the tmpfs keeps Docker's default options (noexec)" "$SMALL" /work $FULL
try "B: no GOPATH (harmless offline; the install step needs it when go.sum lacks an entry and the checksum database is asked)" "$SMALL" $EXEC -e HOME=/work/home -e GOCACHE=/work/gocache -e GOMODCACHE=/gomod -e TMPDIR=/work/tmp
try "C: no HOME and no GOCACHE" "$SMALL" $EXEC -e GOMODCACHE=/gomod -e GOPATH=/work/go -e TMPDIR=/work/tmp
try "D: no TMPDIR" "$SMALL" $EXEC -e HOME=/work/home -e GOCACHE=/work/gocache -e GOMODCACHE=/gomod -e GOPATH=/work/go
try "E: no GOMODCACHE" "$SMALL" $EXEC -e HOME=/work/home -e GOCACHE=/work/gocache -e GOPATH=/work/go -e TMPDIR=/work/tmp

# variants of the module, each in a directory of its own so nothing has to be deleted
work=$(mktemp -d); mkdir "$work/nosum" "$work/newgo" "$work/gowork"
for d in nosum newgo gowork; do cp "$SMALL/go.mod" "$SMALL/small.go" "$SMALL/small_test.go" "$work/$d/"; done
cp "$SMALL/go.sum" "$work/newgo/"; cp "$SMALL/go.sum" "$work/gowork/"
sed -i 's/^go 1.27.3/go 1.99.0/' "$work/newgo/go.mod"
printf 'go 1.27.3\n\nuse .\nuse ../nonexistent\n' > "$work/gowork/go.work"
try "F: go.sum without the dependency" "$work/nosum" $EXEC $FULL
try "G: go.mod needs a newer go than the image (GOTOOLCHAIN=local)" "$work/newgo" $EXEC $FULL
try "H: a go.work in the candidate, run with GOWORK unset (the sandbox sets GOWORK=off)" "$work/gowork" $EXEC $FULL -e GOWORK=

printf '== I: install into a volume the sandbox user does not own\n'
docker volume create m21s0-rootvol >/dev/null
tar -C "$SMALL" -cf - . | docker run --rm -i --user 65532:65532 --network="${NET:-m21s0-inst}" -v m21s0-rootvol:/gomod --tmpfs "$EXEC" $FULL \
	-e GOTOOLCHAIN=local -e GOFLAGS=-mod=readonly -e GOPROXY=https://proxy.golang.org "$IMAGE" \
	sh -c 'mkdir -p /work/src /work/home && tar -x -C /work/src && cd /work/src && go mod download' 2>&1 | head -3
docker volume rm m21s0-rootvol >/dev/null
rm -r "$work"
