#!/bin/sh
# Does a host firewall rule on the executor's uid reach the traffic of its rootless containers? pasta and slirp4netns run as that uid,
# so `meta skuid` matches them. The metadata service is rejected by destination address with no port match, IPv4 and IPv6:
# nothing may be reachable on any port of those addresses. No metadata service answers in the distro, so the rule was tested two ways:
# on the metadata address itself (a rejected connect answers at once where an unanswered one times out, and the rule's packet counter moves),
# and, over a real connection, on a stand-in destination that does answer: proxy.golang.org:443. The IPv6 rule is only syntax-checked by nft.
# usage, as root in the distro: sh owner-block.sh on | probe | off      (NETFLAG=--network=m21s0-inst probes from suiteward-exec's own network)
D=$(dirname "$0")
. "$D/guard.sh"; . "$D/image.sh"; IMG=$ALPINE
UID_EXE=$(id -u suiteward-exec)
IPS=$(getent ahostsv4 proxy.golang.org | awk '{print $1}' | sort -u | tr '\n' ',' | sed 's/,$//')
case $1 in
on)
	nft add table inet m21s0
	nft "add chain inet m21s0 out { type filter hook output priority 0; }"
	nft "add rule inet m21s0 out meta skuid $UID_EXE ip daddr 169.254.169.254 counter reject"
	nft "add rule inet m21s0 out meta skuid $UID_EXE ip6 daddr fd00:ec2::254 counter reject"
	nft "add rule inet m21s0 out meta skuid $UID_EXE ip daddr { $IPS } tcp dport 443 counter reject"   # the stand-in (an address set that rotates is not a design)
	nft list table inet m21s0 ;;
probe)
	for u in suiteward-exec suiteward; do
		for target in "169.254.169.254 80" "169.254.169.254 8080" "proxy.golang.org 443"; do
			printf '%s native:    ' "$u"; sh "$D/as.sh" "$u" bash "$D/connect-time.sh" $target
		done
		flag=""; [ "$u" = suiteward-exec ] && flag=$NETFLAG
		printf '%s container:\n' "$u"
		sh "$D/as.sh" "$u" podman run --rm -i $flag "$IMG" sh -s -- 169.254.169.254:80 169.254.169.254:8080 proxy.golang.org:443 < "$D/probe.sh" 2>&1 | grep -v 'shared mount' | sed 's/^/    /'
	done
	nft list table inet m21s0 | grep counter ;;
off) nft delete table inet m21s0 ;;
esac
