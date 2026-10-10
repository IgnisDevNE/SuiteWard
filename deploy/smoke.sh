#!/bin/sh
# Smoke test of the running SuiteWard stack: readiness, status, a probe through the real worker, and a
# pending probe that must survive a restart. Needs only podman, curl and coreutils; reads nothing from the
# environment but the SMOKE_* knobs below. Run it on the target:
#   sh deploy/smoke.sh [--promote]
#   podman machine ssh "sh -s" < deploy/smoke.sh          (this PC, inside the Podman machine)
#   podman machine ssh "sh -s -- --promote" < deploy/smoke.sh
# --promote also runs `podman auto-update` once; it only works for the quadlet-managed service on the host.
# Exit 0 only if every step passed; a failing step prints "FAIL <step>: <reason>" and exits 1.
set -u

BASE=${SMOKE_BASE:-http://127.0.0.1:8081}
CTR=${SMOKE_CONTAINER:-suiteward}
READY_TIMEOUT=${SMOKE_READY_TIMEOUT:-120}
# Optional: the version /status must report, for example sha-<commit> of the image that was just deployed.
EXPECT_VERSION=${SMOKE_VERSION:-}

promote=0
for arg; do
	case $arg in
	--promote) promote=1 ;;
	*) echo "usage: smoke.sh [--promote]" >&2; exit 2 ;;
	esac
done

nl='
'
summary=
now_ms() { echo $(($(date +%s%N) / 1000000)); }
stamp() { date -u +%Y-%m-%dT%H:%M:%SZ; }
begin() { t0=$(now_ms); }
print_summary() {
	echo "=== smoke summary: $1 ==="
	printf '%s' "$summary"
	echo "image: ${image:-unknown}"
	echo "podman: $(podman --version 2>/dev/null | head -n 1)"
	echo "host: $(uname -sm)"
	echo "=== end summary ==="
}
ok() { # step, detail
	line="$(stamp) $1: ok, elapsed $(($(now_ms) - t0)) ms${2:+, $2}"
	echo "$line"
	summary="$summary$line$nl"
}
die() { # step, reason
	echo "$(stamp) FAIL $1: $2"
	print_summary FAIL
	exit 1
}
num() { printf '%s' "$2" | grep -Eo "\"$1\" *: *[0-9]+" | head -n 1 | grep -Eo '[0-9]+$'; }
last() { printf '%s\n' "$1" | tail -n 2 | tr '\n' ' '; }

wait_ready() {
	end=$(($(now_ms) + READY_TIMEOUT * 1000))
	until curl -fsS -m 3 "$BASE/readyz" >/dev/null 2>&1; do
		[ "$(now_ms)" -lt "$end" ] && sleep 1 || return 1
	done
}

check_status() { # step
	body=$(curl -fsS -m 5 "$BASE/status") || die "$1" "GET /status failed"
	version=$(printf '%s' "$body" | sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$version" ] || die "$1" "no version in $body"
	[ -z "$EXPECT_VERSION" ] || [ "$version" = "$EXPECT_VERSION" ] || die "$1" "version $version, want $EXPECT_VERSION"
	schema=$(num schemaVersion "$body")
	[ -n "$schema" ] || die "$1" "no schemaVersion in $body"
	discarded=$(num discarded "$body")
	failed=$(num failed "$body")
	[ "$discarded" = 0 ] || die "$1" "discarded jobs: ${discarded:-missing}"
	[ "$failed" = 0 ] || die "$1" "failed outbox messages: ${failed:-missing}"
}

image=$(podman inspect --format '{{.ImageName}}' "$CTR" 2>/dev/null </dev/null)

begin
wait_ready || die readyz "$BASE/readyz not ready within ${READY_TIMEOUT}s"
ok readyz

begin
curl -fsS -m 5 "$BASE/healthz" | grep -q '"ok"' || die healthz "$BASE/healthz did not answer ok"
ok healthz

begin
check_status status
ok status "version $version, schema version $schema, no failed or discarded"

begin
out=$(podman exec "$CTR" /suiteward probe </dev/null 2>&1) || die probe "$(last "$out")"
ok probe "$(last "$out")"

begin
out=$(podman exec "$CTR" /suiteward probe --delay 20s --no-wait </dev/null 2>&1) || die probe-pending "$(last "$out")"
id=$(printf '%s\n' "$out" | sed -n 's/.*"probeId" *: *"\([^"]*\)".*/\1/p' | head -n 1)
[ -n "$id" ] || die probe-pending "no probeId in: $(last "$out")"
ok probe-pending "id $id"

# On the host the service is a quadlet unit; restarting the container behind its back would race systemd's own restart.
begin
if systemctl --user cat "$CTR.service" </dev/null >/dev/null 2>&1; then
	systemctl --user restart "$CTR.service" </dev/null || die restart "systemctl restart $CTR.service failed"
else
	podman restart "$CTR" </dev/null >/dev/null 2>&1 || die restart "podman restart $CTR failed"
fi
ok restart
# No new begin: the clock keeps running from before the restart, so the next step is the time from restart to ready.
wait_ready || die ready-after-restart "not ready within ${READY_TIMEOUT}s after the restart"
ok ready-after-restart "time to ready after restart, including the restart"

begin
out=$(podman exec "$CTR" /suiteward probe --wait "$id" --timeout 90s </dev/null 2>&1) || die probe-after-restart "$(last "$out")"
ok probe-after-restart "pending probe $id completed after the restart"

begin
check_status status-final
ok status-final

if [ "$promote" = 1 ]; then
	begin
	before=$(podman inspect --format '{{.ImageDigest}}' "$CTR" </dev/null) || die promote "cannot read the image digest"
	podman auto-update </dev/null >/dev/null || die promote "podman auto-update failed"
	wait_ready || die promote "not ready within ${READY_TIMEOUT}s after auto-update"
	after=$(podman inspect --format '{{.ImageDigest}}' "$CTR" </dev/null) || die promote "cannot read the image digest after the update"
	ok promote "digest before $before, after $after"
fi

print_summary PASS
