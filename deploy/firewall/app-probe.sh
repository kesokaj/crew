#!/usr/bin/env bash
# deploy/firewall/app-probe.sh — the hosted bridges' rules (design 19.4, since 0.72) on real traffic,
# not only as rules: a throwaway world of network namespaces inside a network and mount namespace of
# its own, with the REAL crew-firewall.sh, and connections tried both ways. Run it as root on a
# machine with Docker's modules (br_netfilter, xt_physdev, xt_conntrack), on demand:
#
#   sudo unshare -nm bash deploy/firewall/app-probe.sh
#
# The world: in "br", the namespace the script runs in, an app- bridge (172.24.7.0/24) and an apo-
# bridge (172.24.8.0/24), the hosted bridges' kinds, with Docker's DOCKER-USER chain; on each, a
# "server" at .254, an "ingress" at .253 and an "app" at .252, each a namespace with a veth on the
# bridge, as a container is; routed beyond "br", a "lan" at 192.168.77.2 and a "pub" at 198.51.100.2,
# a private address and one that stands for the internet. /run/netns is a tmpfs of this mount
# namespace, so nothing of it is seen outside, and all of it ends with the script.
set -euo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export PATH="/usr/sbin:/sbin:${PATH}"
PASS=0; FAIL=0
ok()  { echo "PASS $*"; PASS=$((PASS + 1)); }
bad() { echo "FAIL $*"; FAIL=$((FAIL + 1)); }

[ "$(id -u)" = 0 ] || { echo "run as root: sudo unshare -nm bash $0" >&2; exit 2; }
if [ "$(readlink /proc/self/ns/net)" = "$(readlink /proc/1/ns/net)" ]; then
    echo "REFUSING: this is the host's network namespace — run under: sudo unshare -nm bash $0" >&2; exit 2
fi
mount --make-rprivate /
mount -t tmpfs tmpfs /run/netns 2>/dev/null || { mkdir -p /run/netns && mount -t tmpfs tmpfs /run/netns; }
mount -t sysfs sysfs /sys

cleanup() { for p in $(jobs -p); do kill "$p" 2>/dev/null || true; done; }
trap cleanup EXIT

APP=app-0123456789a; APO=apo-0123456789a
ip link set lo up
sysctl -qw net.ipv4.ip_forward=1
for br in "$APP" "$APO"; do ip link add "$br" type bridge; done
ip addr add 172.24.7.1/24 dev "$APP"; ip addr add 172.24.8.1/24 dev "$APO"
ip link set "$APP" up; ip link set "$APO" up
# Bridged frames through iptables, as Docker sets it.
sysctl -qw net.bridge.bridge-nf-call-iptables=1
[ "$(sysctl -n net.bridge.bridge-nf-call-iptables)" = 1 ] && ok "bridge-nf-call-iptables is on in this namespace" || bad "bridge-nf-call-iptables is off"
grep -q '^xt_physdev ' /proc/modules && ok "xt_physdev is loaded" || bad "xt_physdev is not loaded"

me="$(readlink /proc/self/ns/net)"
node() { # name bridge address [gateway]
    local n="$1" br="$2" addr="$3" gw="$4"
    ip netns add "$n"
    ip link add "v-$n" type veth peer name eth0 netns "$n"
    ip link set "v-$n" master "$br" up
    ip -n "$n" addr add "$addr/24" dev eth0
    ip -n "$n" link set eth0 up
    ip -n "$n" link set lo up
    ip -n "$n" route add default via "$gw"
}
for side in app apo; do
    if [ "$side" = app ]; then br="$APP"; net=172.24.7; else br="$APO"; net=172.24.8; fi
    node "srv-$side" "$br" "$net.254" "$net.1"
    node "ing-$side" "$br" "$net.253" "$net.1"
    node "app-$side" "$br" "$net.252" "$net.1"
done
routed() { # name address/24 my-address
    ip netns add "$1"
    ip link add "v-$1" type veth peer name eth0 netns "$1"
    ip addr add "$3/24" dev "v-$1"; ip link set "v-$1" up
    ip -n "$1" addr add "$2/24" dev eth0; ip -n "$1" link set eth0 up; ip -n "$1" link set lo up
    ip -n "$1" route add default via "$3"
}
routed lan 192.168.77.2 192.168.77.1
routed pub 198.51.100.2 198.51.100.1

# Docker's chain, which FORWARD jumps to first, and the real script.
iptables -N DOCKER-USER; iptables -A FORWARD -j DOCKER-USER
rc=0; bash "${HERE}/crew-firewall.sh" >/dev/null 2>&1 || rc=$?
[ "$rc" = 0 ] && ok "crew-firewall.sh runs clean here" || bad "crew-firewall.sh exited ${rc}"

# A listener on 8080 in each namespace that someone should or should not reach.
for n in srv-app ing-app app-app srv-apo ing-apo app-apo lan pub; do
    ip netns exec "$n" python3 -I -m http.server 8080 --bind 0.0.0.0 >/dev/null 2>&1 &
done
sleep 1.5
reach() { ip netns exec "$1" timeout 3 bash -c "exec 3<>/dev/tcp/$2/8080" 2>/dev/null; }
want() { # from to yes|no what
    if reach "$1" "$2"; then got=yes; else got=no; fi
    [ "$got" = "$3" ] && ok "$4" || bad "$4 (it was ${got})"
}
for side in app apo; do
    if [ "$side" = app ]; then net=172.24.7; else net=172.24.8; fi
    want "ing-$side" "$net.252" yes "${side}-: the ingress opens a connection to the app"
    want "srv-$side" "$net.252" yes "${side}-: the server opens a connection to the app (its health)"
    want "app-$side" "$net.253" no  "${side}-: the app cannot open one to the ingress"
    want "app-$side" "$net.254" no  "${side}-: the app cannot open one to the server"
    want "app-$side" 192.168.77.2 no "${side}-: the app cannot reach a private address"
done
want app-app 198.51.100.2 yes "app-: the app reaches the internet from inside"
want app-apo 198.51.100.2 no  "apo-: the app reaches nothing beyond its bridge"
# The rules were consulted, not passed around: the app- chain's DROP of bridged traffic counted.
drops="$(iptables -L CREW-APP -v -n -x | awk '/physdev-is-bridged/ && $3 == "DROP" {print $1}')"
[ "${drops:-0}" -gt 0 ] && ok "CREW-APP's bridged DROP counted ${drops} packets" || bad "CREW-APP's bridged DROP counted nothing"
rets="$(iptables -L CREW-APP -v -n -x | awk '/172.16.0.253/ && $3 == "RETURN" {print $1}')"
[ "${rets:-0}" -gt 0 ] && ok "CREW-APP's ingress RETURN counted ${rets} packets" || bad "CREW-APP's ingress RETURN counted nothing"
[ "$(readlink /proc/self/ns/net)" = "${me}" ] || bad "the script left its own namespace"

echo
echo "PASS=${PASS} FAIL=${FAIL}"
[ "${FAIL}" -eq 0 ]
