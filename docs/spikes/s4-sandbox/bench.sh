#!/bin/sh
# The cost measurements: time, CPU and memory of `go test ./...` in the sandbox, per image, race on or off, CPU and memory limit.
# One line per run: wall and CPU seconds, peak memory, tests passed, failed and skipped, and whether the OOM killer fired (the METRIC line of sandbox.sh).
# usage: WORK=<dir holding small.tar and repo.tar> ALP=<alpine image ref> DEB=<bookworm image ref> sh bench.sh quick|time|cpu|floor
#   small.tar: tar -C <smallmod dir> -cf small.tar .          repo.tar: git archive HEAD > repo.tar
#   the volumes m21s0-gomod (small) and m21s0-gomod-repo (repo) must hold the module caches (sandbox.sh prep, install)
set -u
. "$(dirname "$0")/image.sh"
WORK=${WORK:?}; ALP=${ALP:-$ALPINE}; DEB=${DEB:-$BOOKWORM}
S=$(dirname "$0")/sandbox.sh
out=$(mktemp -d)

# bench <label> <tar> <volume> <image> <cgo> <memory> <cpus> <go test args...>
bench() {
	label=$1; tarf=$2; vol=$3; image=$4; cgo=$5; mem=$6; cpus=$7; shift 7
	t0=$(date +%s)
	VOL=$vol IMAGE=$image CGO=$cgo MEM=$mem CPUS=$cpus sh "$S" test "$@" < "$tarf" > "$out/r.json" 2> "$out/r.err"
	t1=$(date +%s)
	np=$(grep -c '"Action":"pass".*"Test"' "$out/r.json"); nf=$(grep -c '"Action":"fail".*"Test"' "$out/r.json"); ns=$(grep -c '"Action":"skip".*"Test"' "$out/r.json")
	echo "$label | outer_s=$((t1 - t0)) | tests pass=$np fail=$nf skip=$ns | $(grep METRIC "$out/r.err" | tail -1)"
}

case ${1:?} in
quick)
	bench "small alpine norace" "$WORK/small.tar" m21s0-gomod "$ALP" 0 3g 4 ./...
	bench "repo alpine norace" "$WORK/repo.tar" m21s0-gomod-repo "$ALP" 0 3g 4 ./... ;;
time) # three runs each; the small module has one failing test on purpose, so its exit status is 1
	for i in 1 2 3; do bench "small alpine norace run$i" "$WORK/small.tar" m21s0-gomod "$ALP" 0 3g 4 ./...; done
	for i in 1 2 3; do bench "small bookworm norace run$i" "$WORK/small.tar" m21s0-gomod "$DEB" 0 3g 4 ./...; done
	for i in 1 2 3; do bench "small bookworm race run$i" "$WORK/small.tar" m21s0-gomod "$DEB" 1 3g 4 -race ./...; done
	for i in 1 2 3; do bench "repo alpine norace run$i" "$WORK/repo.tar" m21s0-gomod-repo "$ALP" 0 3g 4 ./...; done
	for i in 1 2 3; do bench "repo bookworm norace run$i" "$WORK/repo.tar" m21s0-gomod-repo "$DEB" 0 3g 4 ./...; done
	for i in 1 2 3; do bench "repo bookworm race run$i" "$WORK/repo.tar" m21s0-gomod-repo "$DEB" 1 3g 4 -race ./...; done ;;
cpu)
	for c in 1 2 4 8; do bench "repo alpine norace cpus=$c" "$WORK/repo.tar" m21s0-gomod-repo "$ALP" 0 3g "$c" ./...; done ;;
floor) # memory-swap equals memory, so the kernel has no swap to hide behind
	for m in 3g 2g 1536m 1g 768m; do bench "repo alpine norace mem=$m" "$WORK/repo.tar" m21s0-gomod-repo "$ALP" 0 "$m" 4 ./...; done
	for m in 3g 2g 1536m 1g; do bench "repo bookworm race mem=$m" "$WORK/repo.tar" m21s0-gomod-repo "$DEB" 1 "$m" 4 -race ./...; done ;;
esac
rm -r "$out"
