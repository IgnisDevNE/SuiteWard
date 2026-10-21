#!/bin/sh
# Smoke test of the running SuiteWard stack: readiness, status, the GitHub synchronization (every configured repository
# synchronized once), the MCP listener's unauthenticated answers, a probe through the real worker, and a
# delayed probe job that must survive a restart. Needs only podman, curl and coreutils; reads nothing from the
# environment but the SMOKE_* knobs below. Run it on the target:
#   sh deploy/smoke.sh
#   podman machine ssh --username user "sh -s" < deploy/smoke.sh          (this PC, inside the Podman machine, as its rootless user: the default ssh user is root, the rootful store)
# Exit 0 only if every step passed; a failing step prints "FAIL <step>: <reason>" and exits 1.
set -u

BASE=${SMOKE_BASE:-http://127.0.0.1:8081}
CTR=${SMOKE_CONTAINER:-suiteward}
READY_TIMEOUT=${SMOKE_READY_TIMEOUT:-120}
# Optional: the version /status must report, for example sha-<commit> of the image that was just deployed.
EXPECT_VERSION=${SMOKE_VERSION:-}
# Optional: the tier /status must report: isolated on the host, co-located on this PC. Unset, any of the two passes.
EXPECT_TIER=${SMOKE_TIER:-}
# Both targets run with GitHub configured: the github step waits until every repository in /status has a lastSuccess.
# SMOKE_GITHUB=0 skips it, only for a stack that was started without the GitHub settings.
GITHUB=${SMOKE_GITHUB:-1}
GITHUB_TIMEOUT=${SMOKE_GITHUB_TIMEOUT:-120}
# The MCP listener's published port. The mcp step sends no token and never reads one.
MCP_BASE=${SMOKE_MCP_BASE:-http://127.0.0.1:8091}

[ $# -eq 0 ] || { echo "usage: smoke.sh" >&2; exit 2; }

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

# Waits until every repository in the /status github array has a lastSuccess; dies on a lastError, an absent or empty array, or the timeout.
check_github() {
	end=$(($(now_ms) + GITHUB_TIMEOUT * 1000))
	while :; do
		body=$(curl -fsS -m 5 "$BASE/status") || die github "GET /status failed"
		printf '%s' "$body" | grep -q '"github" *: *\[' || die github "no github section in /status (start the stack with the GitHub settings, or set SMOKE_GITHUB=0)"
		# Entries hold nested arrays and objects (protection): walk from "github":[ to the ] that closes it, outside strings,
		# and start a new line at each object directly inside the array.
		entries=$(printf '%s' "$body" | awk '
			{
				if (!match($0, /"github" *: *\[/)) exit
				s = substr($0, RSTART + RLENGTH); depth = 1
				for (i = 1; i <= length(s); i++) {
					c = substr(s, i, 1)
					if (esc) esc = 0
					else if (c == "\\" && str) esc = 1
					else if (c == "\"") str = !str
					else if (!str) {
						if (c == "{" || c == "[") { if (c == "{" && depth == 1) printf "\n"; depth++ }
						else if (c == "}" || c == "]") { if (--depth == 0) break }
					}
					printf "%s", c
				}
				printf "\n"
			}')
		repos=$(($(printf '%s\n' "$entries" | grep -c '"repo" *:')))
		[ "$repos" -gt 0 ] || die github "no repositories in the github section"
		bad=$(printf '%s\n' "$entries" | grep '"lastError" *:' | head -n 1)
		if [ -n "$bad" ]; then
			die github "$(printf '%s' "$bad" | sed -n 's/.*"repo" *: *"\([^"]*\)".*/\1/p') has lastError: $(printf '%s' "$bad" | sed -n 's/.*"lastError" *: *"\([^"]*\)".*/\1/p')"
		fi
		synced=$(($(printf '%s\n' "$entries" | grep -c '"lastSuccess" *:')))
		[ "$synced" -lt "$repos" ] || return 0
		[ "$(now_ms)" -lt "$end" ] || die github "no lastSuccess for $((repos - synced)) of $repos repositories within ${GITHUB_TIMEOUT}s"
		sleep 1
	done
}

# The HTTP status of one unauthenticated request to the MCP endpoint; 000 when nothing answers.
mcp_code() { curl -sS -m 5 -o /dev/null -w '%{http_code}' "$@" "$MCP_BASE/mcp" </dev/null 2>/dev/null; }
mcp_post() { mcp_code -X POST -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' -d '{}' "$@"; }

check_status() { # step
	body=$(curl -fsS -m 5 "$BASE/status") || die "$1" "GET /status failed"
	version=$(printf '%s' "$body" | sed -n 's/.*"version" *: *"\([^"]*\)".*/\1/p' | head -n 1)
	[ -n "$version" ] || die "$1" "no version in $body"
	[ -z "$EXPECT_VERSION" ] || [ "$version" = "$EXPECT_VERSION" ] || die "$1" "version $version, want $EXPECT_VERSION"
	schema=$(num schemaVersion "$body")
	[ -n "$schema" ] || die "$1" "no schemaVersion in $body"
	tier=$(printf '%s' "$body" | sed -n 's/.*"tier" *: *"\([^"]*\)".*/\1/p' | head -n 1)
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
[ -n "$tier" ] || die tier "no tier in $body" # check_status read it from the same /status answer
if [ -n "$EXPECT_TIER" ]; then
	[ "$tier" = "$EXPECT_TIER" ] || die tier "$tier, want $EXPECT_TIER"
else
	[ "$tier" = isolated ] || [ "$tier" = co-located ] || die tier "$tier, want isolated or co-located"
fi
ok tier "$tier"

begin
if [ "$GITHUB" = 0 ]; then
	ok github "skipped (SMOKE_GITHUB=0)"
else
	check_github
	ok github "$repos repositories synchronized"
fi

begin
code=$(mcp_post)
[ "$code" = 401 ] || die mcp "POST without a token answered $code, want 401"
code=$(mcp_post -H 'Origin: http://evil.example')
[ "$code" = 403 ] || die mcp "POST with a foreign Origin answered $code, want 403"
ok mcp "without a token 401, foreign Origin 403"

begin
out=$(podman exec "$CTR" /suiteward probe </dev/null 2>&1) || die probe "$(last "$out")"
ok probe "$(last "$out")"

begin
out=$(podman exec "$CTR" /suiteward probe --delay 20s --no-wait </dev/null 2>&1) || die probe-pending "$(last "$out")"
id=$(printf '%s\n' "$out" | sed -n 's/.*"probeId" *: *"\([^"]*\)".*/\1/p' | head -n 1)
[ -n "$id" ] || die probe-pending "no probeId in: $(last "$out")"
ok probe-pending "delayed probe enqueued, due in 20 s, id $id"

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
ok probe-after-restart "delayed job $id survived the restart and completed; this does not show when the outbox message was delivered (the relay sends it independently of the job, before or after the restart)"

begin
check_status status-final
ok status-final

print_summary PASS
# Exit here: piped through PowerShell, `sh -s` would otherwise run the line the pipe appends and exit 127.
exit 0
