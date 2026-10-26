#!/bin/bash
# Tries a TCP connect for at most 6 s and prints the outcome and how long it took: a rejecting firewall rule answers at once (rc 1),
# a destination nobody answers makes the attempt time out (rc 124).
# usage: bash connect-time.sh <host> <port>
start=$(date +%s%N)
timeout 6 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null; rc=$?
echo "$1:$2 rc=$rc ms=$(( ($(date +%s%N) - start) / 1000000 ))"
