#!/bin/sh
# Self-test for smoke.sh. A fake podman and curl on PATH prove the success path and that every failing step
# exits non-zero and is named. Run: sh deploy/smoke_test.sh
set -u
dir=$(cd "$(dirname "$0")" && pwd)
smoke=$dir/smoke.sh
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/bin"

cat > "$tmp/bin/curl" <<'EOF'
#!/bin/sh
for url; do :; done
case $url in
*/readyz)
	if [ "${FAKE_READYZ:-ok}" = down ] || { [ -e "$FAKE_DIR/restarted" ] && [ "${FAKE_READY_AFTER_RESTART:-ok}" = down ]; }; then exit 22; fi
	echo '{"status":"ready","schemaVersion":7}' ;;
*/healthz) echo '{"status":"ok"}' ;;
*/status)
	[ -e "$FAKE_DIR/restarted" ] && FAKE_FAILED=${FAKE_FAILED_AFTER_RESTART:-${FAKE_FAILED:-0}}
	echo "{\"version\":\"test\",\"schemaVersion\":7,\"jobs\":{\"available\":0,\"running\":0,\"retryable\":0,\"scheduled\":0,\"completed\":2,\"discarded\":${FAKE_DISCARDED:-0}},\"outbox\":{\"pending\":0,\"delivered\":2,\"failed\":${FAKE_FAILED:-0}}}" ;;
*) exit 22 ;;
esac
EOF

cat > "$tmp/bin/podman" <<'EOF'
#!/bin/sh
echo "$*" >> "$FAKE_LOG"
# A command that reads stdin would swallow the rest of a script piped in with `sh -s`.
[ "${FAKE_DRAIN_STDIN:-}" = 1 ] && cat > /dev/null
case $* in
"exec suiteward /suiteward probe --delay 20s --no-wait")
	[ "${FAKE_NO_ID:-}" = 1 ] || echo '{"probeId":"42-probe-abc"}'
	exit "${FAKE_NOWAIT_RC:-0}" ;;
"exec suiteward /suiteward probe --wait "*)
	echo '{"job":"completed","outbox":"delivered","elapsedMs":1200}'
	exit "${FAKE_WAIT_RC:-0}" ;;
"exec suiteward /suiteward probe")
	echo '{"probeId":"41-probe-xyz"}'
	echo '{"job":"completed","outbox":"delivered","elapsedMs":900}'
	exit "${FAKE_PROBE_RC:-0}" ;;
"restart suiteward") : > "$FAKE_DIR/restarted"; exit "${FAKE_RESTART_RC:-0}" ;;
inspect*) echo sha256:fakedigest ;;
auto-update) ;;
*) echo "fake podman: unexpected: $*" >&2; exit 99 ;;
esac
EOF
chmod +x "$tmp/bin/curl" "$tmp/bin/podman"

failures=0
run() {
	: > "$tmp/log"
	rm -f "$tmp/restarted"
	out=$(env PATH="$tmp/bin:$PATH" FAKE_LOG="$tmp/log" FAKE_DIR="$tmp" SMOKE_READY_TIMEOUT=1 "$@" 2>&1)
	rc=$?
}
pass() { echo "ok   $1"; }
bad() { echo "FAIL $1: $2"; echo "$out" | sed 's/^/     | /'; failures=$((failures + 1)); }
contains() { echo "$out" | grep -q -- "$1"; }
expect_ok() { # name, text that must appear
	if [ "$rc" -eq 0 ] && contains "$2"; then pass "$1"; else bad "$1" "want exit 0 and '$2', got exit $rc"; fi
}
expect_fail() { # name, text that must appear
	if [ "$rc" -ne 0 ] && contains "$2"; then pass "$1"; else bad "$1" "want non-zero exit and '$2', got exit $rc"; fi
}
logged() { grep -q -- "$1" "$tmp/log"; }

run sh "$smoke"
expect_ok "success path" "PASS"
logged "^restart suiteward" || bad "success path" "service was not restarted"
logged "probe --wait 42-probe-abc" || bad "success path" "pending probe was not awaited after the restart"
contains "status: ok, elapsed [0-9]* ms, version test, schema version 7" || bad "success path" "status does not report the version"
contains "time to ready after restart, including the restart" || bad "success path" "ready-after-restart does not say its clock includes the restart"
contains "delayed job 42-probe-abc survived the restart" || bad "success path" "the pending step does not say that a delayed job survived"
contains "does not show when the outbox message was delivered" || bad "success path" "the pending step claims more than a delayed job surviving"
if contains "elapsed"; then pass "timings reported"; else bad "timings reported" "no elapsed time in the output"; fi

run FAKE_READYZ=down sh "$smoke"
expect_fail "readyz never answers" "FAIL readyz"

run SMOKE_VERSION=test sh "$smoke"
expect_ok "expected version matches" "version test"

run SMOKE_VERSION=sha-abc sh "$smoke"
expect_fail "expected version differs" "FAIL status: version test, want sha-abc"

run FAKE_DISCARDED=2 sh "$smoke"
expect_fail "discarded jobs" "FAIL status"

run FAKE_FAILED=1 sh "$smoke"
expect_fail "failed outbox messages" "FAIL status"

run FAKE_PROBE_RC=2 sh "$smoke"
expect_fail "probe fails" "FAIL probe"

run FAKE_NOWAIT_RC=2 sh "$smoke"
expect_fail "delayed probe cannot be enqueued" "FAIL probe-pending"

run FAKE_NO_ID=1 sh "$smoke"
expect_fail "delayed probe prints no id" "FAIL probe-pending"

run FAKE_RESTART_RC=1 sh "$smoke"
expect_fail "restart fails" "FAIL restart"

run FAKE_FAILED_AFTER_RESTART=1 sh "$smoke"
expect_fail "failed outbox message after the restart" "FAIL status-final"

run FAKE_READY_AFTER_RESTART=down sh "$smoke"
expect_fail "not ready after restart" "FAIL ready-after-restart"

run FAKE_WAIT_RC=2 sh "$smoke"
expect_fail "pending probe lost after restart" "FAIL probe-after-restart"

run sh "$smoke" --promote
expect_ok "promote" "promote"
logged "^auto-update" || bad "promote" "podman auto-update was not run"

# Piped in, as `podman machine ssh "sh -s" < deploy/smoke.sh` does: no step may consume the script's own stdin.
run FAKE_DRAIN_STDIN=1 sh -s < "$smoke"
expect_ok "script piped to sh -s" "PASS"
run FAKE_DRAIN_STDIN=1 sh -s -- --promote < "$smoke"
expect_ok "script piped to sh -s with --promote" "promote"

# PowerShell appends a line ending to what it pipes (`Get-Content -Raw deploy/smoke.sh | podman machine ssh "sh -s"`), and the
# remote bash then runs that stray line (`$'\r': command not found`, exit 127 after a passing run). The script must end the
# shell itself; a marker line stands for whatever the transport appends.
{ cat "$smoke"; echo 'echo STRAY-LINE-AFTER-THE-SCRIPT'; } > "$tmp/smoke_stray.sh"
run sh -s < "$tmp/smoke_stray.sh"
expect_ok "script piped with a stray line after it" "PASS"
if contains "STRAY-LINE-AFTER-THE-SCRIPT"; then bad "script piped with a stray line after it" "the shell ran what followed the script"; fi

run sh "$smoke" --bogus
expect_fail "unknown flag" "usage"

[ "$failures" -eq 0 ] || { echo "$failures case(s) failed"; exit 1; }
echo "smoke self-test passed"
