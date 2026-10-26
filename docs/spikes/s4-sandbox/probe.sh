#!/bin/sh
# Runs inside a container (busybox): connect to each host:port with a 3 s timeout and say what happened, and how many seconds it took
# (a rejected connect answers at once, an unanswered one takes the whole timeout). The host name GATEWAY stands for the default gateway.
# usage: <runtime> run --rm -i [--network=...] <image> sh -s -- host:port ... < probe.sh
for t in "$@"; do
	h=${t%:*}; p=${t##*:}
	[ "$h" = GATEWAY ] && h=$(ip route | awk '/^default/{print $3; exit}')
	start=$(date +%s)
	out=$(printf 'GET / HTTP/1.0\r\n\r\n' | nc -w 3 "$h" "$p" 2>&1); rc=$?
	secs=$(( $(date +%s) - start ))
	body=$(echo "$out" | tr -d '\r' | sed -n '/^$/,$p' | tail -n +2 | head -1 | cut -c1-40)
	if [ $rc -eq 0 ] || [ -n "$body" ]; then echo "REACHED  $t ($h) rc=$rc ${body:+body=$body}"; else echo "blocked  $t ($h) rc=$rc after ${secs}s"; fi
done
