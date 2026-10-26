#!/bin/sh
# Does a host firewall rule on the executor's uid reach the traffic of its rootless containers? pasta and slirp4netns run as that uid,
# so `meta skuid` matches them. Stand-in destination: proxy.golang.org:443 (on a cloud host the destination would be 169.254.169.254).
# usage, as root in the distro: sh owner-block.sh on | probe | off      (NETFLAG=--network=m21s0-inst probes from suiteward-exec's own network)
# IPv4 only: IPv6 destinations (the metadata service also answers on an IPv6 address on some clouds) need an `ip6 daddr` rule of their own.
D=$(dirname "$0")
. "$D/image.sh"; IMG=$ALPINE
IPS=$(getent ahostsv4 proxy.golang.org | awk '{print $1}' | sort -u | tr '\n' ',' | sed 's/,$//')
case $1 in
on)
	nft add table inet m21s0
	nft "add chain inet m21s0 out { type filter hook output priority 0; }"
	nft "add rule inet m21s0 out meta skuid 1001 ip daddr { $IPS } tcp dport 443 reject"
	nft list table inet m21s0 ;;
probe)
	for u in suiteward-exec suiteward; do
		printf '%s native:    ' "$u"; sh "$D/as.sh" "$u" sh -c 'timeout 4 bash -c "exec 3<>/dev/tcp/proxy.golang.org/443" 2>&1 && echo reached || echo blocked'
		flag=""; [ "$u" = suiteward-exec ] && flag=$NETFLAG
		printf '%s container: ' "$u"; sh "$D/as.sh" "$u" podman run --rm -i $flag "$IMG" sh -s -- proxy.golang.org:443 < "$D/probe.sh" 2>&1 | grep -v 'shared mount'
	done ;;
off) nft delete table inet m21s0 ;;
esac
