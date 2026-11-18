#!/usr/bin/env bash
# deploy/firewall/test.sh — exercises the REAL crew-firewall.sh against
# scratch chains inside an unprivileged user+network+mount namespace, so the
# host's rules, paths and units are never touched. Run it as:
#
#   unshare -Urnm bash deploy/firewall/test.sh
#
# In a check sandbox (make check with CREW_SANDBOX set) a user namespace
# cannot mount /sys, so it runs as the microVM's own root, in a network and
# mount namespace of its own: sudo -n unshare -nm. The barrier below holds in
# both: it never runs in its caller's network namespace.
#
# internal/config/firewall_script_test.go does exactly that under
# `go test ./...` (and skips, loudly, where user namespaces are unavailable).
# The namespace starts with an EMPTY ruleset; /sys is remounted so
# /sys/class/net shows only this namespace's interfaces. No sudo outside a sandbox, no systemd,
# no production path.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${HERE}/crew-firewall.sh"
export PATH="/usr/sbin:/sbin:${PATH}"
PASS=0; FAIL=0
ok()  { echo "PASS $*"; PASS=$((PASS + 1)); }
bad() { echo "FAIL $*"; FAIL=$((FAIL + 1)); }
check() { if eval "$2"; then ok "$1"; else bad "$1"; fi; }

[ "$(id -u)" = 0 ] || { echo "not root inside the namespace — run under: unshare -Urnm bash $0" >&2; exit 2; }
# DEFINITIVE barrier before anything is mounted or changed: this must be a network namespace other than the host's. A real-root
# run outside unshare sees pid 1's netns as its own and stops here.
if [ "$(readlink /proc/self/ns/net 2>/dev/null)" = "$(readlink /proc/1/ns/net 2>/dev/null)" ]; then
    echo "REFUSING: this is the host's network namespace — run under: unshare -Urnm bash $0" >&2; exit 2
fi
mount -t sysfs sysfs /sys
if [ "$(ls /sys/class/net | tr '\n' ' ')" != "lo " ]; then
    echo "expected an empty namespace (/sys/class/net = lo), got: $(ls /sys/class/net | tr '\n' ' ')" >&2; exit 2
fi

# The golden vector shared with internal/config/bridgename_test.go:
# BridgeName("sdb-"+U, ModeInternet) is sdb-e660d58459e, and in ModeLAN it is
# lan-e660d58459e — same suffix, different prefix.
# printf, never echo: echo's trailing newline changes the hash.
U=3e6a772c-b177-4cfc-b209-b14db514bac2
SUFFIX="$(printf 'sdb-%s' "${U}" | sha1sum | cut -c1-11)"
IFACE="sdb-${SUFFIX}"
LANIF="lan-${SUFFIX}"
MDLIF="mdl-${SUFFIX}"
SRCIF="src-${SUFFIX}"
APPIF="app-${SUFFIX}"
APOIF="apo-${SUFFIX}"
check "golden sandbox bridge name" "[ '${IFACE}' = sdb-e660d58459e ]"
check "golden lan bridge name" "[ '${LANIF}' = lan-e660d58459e ]"

# Scratch topology: an uplink on TEST-NET-1, a SANDBOX project bridge and a
# LAN project bridge with Docker's usual /16s, and the DOCKER-USER chain
# Docker would have created. Each is a bridge, as Docker's are: a check
# sandbox's microVM, where make check runs on Crew's project, has no dummy
# link type.
ip link add enp0 type bridge; ip addr add 192.0.2.1/24 dev enp0; ip link set enp0 up
ip link add "${IFACE}" type bridge; ip addr add 172.20.0.1/16 dev "${IFACE}"; ip link set "${IFACE}" up
ip link add "${LANIF}" type bridge; ip addr add 172.21.0.1/16 dev "${LANIF}"; ip link set "${LANIF}" up
ip link add "${MDLIF}" type bridge; ip addr add 172.22.0.1/16 dev "${MDLIF}"; ip link set "${MDLIF}" up
ip link add "${SRCIF}" type bridge; ip addr add 172.23.0.1/16 dev "${SRCIF}"; ip link set "${SRCIF}" up
ip link add "${APPIF}" type bridge; ip addr add 172.24.7.1/24 dev "${APPIF}"; ip link set "${APPIF}" up
ip link add "${APOIF}" type bridge; ip addr add 172.24.8.1/24 dev "${APOIF}"; ip link set "${APOIF}" up
ip link set lo up
iptables -N DOCKER-USER

# The script exits 1 whenever it has WARNS (a missing xt_physdev included);
# the assertions are the check, so a warning exit must not abort the suite
# under set -e — but the status is KEPT in RUNRC and asserted where it
# matters (`|| true` alone made the exit status unobservable in both
# directions).
RUNRC=0
# shellcheck disable=SC2034  # read inside the check strings the helper evals
run() { local rc=0; bash "${SCRIPT}" || rc=$?; RUNRC=${rc}; return 0; }
rules() { iptables -S CREW-FWD | grep '^-A'; }
in_rules() { iptables -S CREW-IN | grep '^-A'; }

echo "=== 1. base rules"
run >/dev/null 2>&1
check "a clean run exits 0 (no WARNS)" "[ \"\${RUNRC}\" = 0 ]"
check "physdev RETURN present" "rules | grep -- '--physdev-is-bridged -j RETURN' >/dev/null"
check "six private-range DROPs" "[ \$(rules | grep -c -- '-j DROP') -eq 6 ]"
check "CREW-IN is a bare DROP" "[ \"\$(in_rules)\" = '-A CREW-IN -j DROP' ]"
check "INPUT jump to CREW-IN" "iptables -S INPUT | grep -F -- '-i sdb-+ -j CREW-IN' >/dev/null"
check "DOCKER-USER jump to CREW-FWD" "iptables -S DOCKER-USER | grep -F -- '-i sdb-+ -j CREW-FWD' >/dev/null"

echo "=== 2. the jumps match the sandbox prefix and NOT the lan prefix"
# This is the whole feature expressed as a rule-level assertion. The jumps are
# wildcard `sdb-+`, so a lan-<hash> interface is reached by nothing here — and
# a sandbox one must always be.
check "no rule anywhere names the lan- interface" \
    "! iptables -S | grep -- '${LANIF}' >/dev/null"
check "the sdb- wildcard covers the sandbox iface" \
    "[ \"\$(iptables -S INPUT | grep -c -- '-i sdb-+')\" = 1 ]"
check "no rule names a lan- wildcard" "! iptables -S | grep -- 'lan-+' >/dev/null"

echo "=== 2b. a Model only bridge reaches nothing but its own bridge (13.6)"
mdl_rules() { iptables -S CREW-MDL | grep '^-A'; }
check "golden model only bridge name" "[ '${MDLIF}' = mdl-e660d58459e ]"
check "INPUT jump to CREW-IN on the mdl- wildcard" "iptables -S INPUT | grep -F -- '-i mdl-+ -j CREW-IN' >/dev/null"
check "DOCKER-USER jump to CREW-MDL on the mdl- wildcard" "iptables -S DOCKER-USER | grep -F -- '-i mdl-+ -j CREW-MDL' >/dev/null"
check "CREW-MDL returns the same bridge, then drops everything" \
    "[ \"\$(mdl_rules | tr '\n' '|')\" = '-A CREW-MDL -m physdev --physdev-is-bridged -j RETURN|-A CREW-MDL -j DROP|' ]"
check "the mdl- bridge is not sent to CREW-FWD, which lets the internet through" \
    "! iptables -S DOCKER-USER | grep -F -- '-i mdl-+ -j CREW-FWD' >/dev/null"
before="$(mdl_rules)"; run >/dev/null 2>&1
check "a second run changes nothing in CREW-MDL" "[ \"\$(mdl_rules)\" = \"\${before}\" ]"
iptables -F CREW-MDL
iptables -D DOCKER-USER -i mdl-+ -j CREW-MDL
run >/dev/null 2>&1
check "a flushed CREW-MDL and its deleted jump are rebuilt" \
    "iptables -S DOCKER-USER | grep -F -- '-i mdl-+ -j CREW-MDL' >/dev/null && mdl_rules | grep -x -- '-A CREW-MDL -j DROP' >/dev/null"

echo "=== 2c. a Sources bridge, the build of a release, has the Model only chain (13.6, since 0.51)"
check "golden sources bridge name" "[ '${SRCIF}' = src-e660d58459e ]"
check "INPUT jump to CREW-IN on the src- wildcard" "iptables -S INPUT | grep -F -- '-i src-+ -j CREW-IN' >/dev/null"
check "DOCKER-USER jump to CREW-MDL on the src- wildcard" "iptables -S DOCKER-USER | grep -F -- '-i src-+ -j CREW-MDL' >/dev/null"
check "the src- bridge is not sent to CREW-FWD, which lets the internet through" \
    "! iptables -S DOCKER-USER | grep -F -- '-i src-+ -j CREW-FWD' >/dev/null"
iptables -D DOCKER-USER -i src-+ -j CREW-MDL
run >/dev/null 2>&1
check "a deleted src- jump is put back" "iptables -S DOCKER-USER | grep -F -- '-i src-+ -j CREW-MDL' >/dev/null"

echo "=== 2d. a hosted project's bridge: only the server and the ingress open a connection on it (19.4, since 0.72)"
app_rules() { iptables -S "$1" | grep '^-A'; }
check "golden hosted bridge names" "[ '${APPIF}' = app-e660d58459e ] && [ '${APOIF}' = apo-e660d58459e ]"
check "INPUT jumps to CREW-IN on the app- and apo- wildcards" \
    "iptables -S INPUT | grep -F -- '-i app-+ -j CREW-IN' >/dev/null && iptables -S INPUT | grep -F -- '-i apo-+ -j CREW-IN' >/dev/null"
check "DOCKER-USER jumps to CREW-APP on app- and to CREW-APO on apo-" \
    "iptables -S DOCKER-USER | grep -F -- '-i app-+ -j CREW-APP' >/dev/null && iptables -S DOCKER-USER | grep -F -- '-i apo-+ -j CREW-APO' >/dev/null"
want_head='-A CREW-APP -m conntrack --ctstate RELATED,ESTABLISHED -j RETURN|-A CREW-APP -s 172.16.0.254/255.240.0.255 -m physdev --physdev-is-bridged -j RETURN|-A CREW-APP -s 172.16.0.253/255.240.0.255 -m physdev --physdev-is-bridged -j RETURN|-A CREW-APP -m physdev --physdev-is-bridged -j DROP|'
check "CREW-APP: replies, the server and the ingress open on the bridge, the app opens nothing there" \
    "[ \"\$(app_rules CREW-APP | head -4 | tr '\n' '|')\" = \"\${want_head}\" ]"
check "CREW-APP then drops the six private ranges, and lets the internet through" \
    "[ \$(app_rules CREW-APP | tail -n +5 | grep -c -- '-j DROP') -eq 6 ] && ! app_rules CREW-APP | grep -x -- '-A CREW-APP -j DROP' >/dev/null"
check "CREW-APO: the same head, then drops everything" \
    "[ \"\$(app_rules CREW-APO | tr '\n' '|')\" = \"\$(printf '%s' \"\${want_head}\" | sed 's/CREW-APP/CREW-APO/g')-A CREW-APO -j DROP|\" ]"
check "neither hosted bridge goes to CREW-FWD" \
    "! iptables -S DOCKER-USER | grep -F -- '-i app-+ -j CREW-FWD' >/dev/null && ! iptables -S DOCKER-USER | grep -F -- '-i apo-+ -j CREW-FWD' >/dev/null"
before="$(app_rules CREW-APP)$(app_rules CREW-APO)"; run >/dev/null 2>&1
check "a second run changes neither" "[ \"\$(app_rules CREW-APP)\$(app_rules CREW-APO)\" = \"\${before}\" ]"
iptables -F CREW-APO
iptables -D DOCKER-USER -i app-+ -j CREW-APP
run >/dev/null 2>&1
check "a flushed CREW-APO and a deleted app- jump are rebuilt" \
    "iptables -S DOCKER-USER | grep -F -- '-i app-+ -j CREW-APP' >/dev/null && app_rules CREW-APO | grep -x -- '-A CREW-APO -j DROP' >/dev/null"

echo "=== 3. idempotence and self-heal"
before="$(rules)"; run >/dev/null 2>&1; after="$(rules)"
if [ "${before}" = "${after}" ]; then ok "second run changes nothing"; else bad "second run changes nothing"; fi
iptables -F CREW-FWD
run >/dev/null 2>&1
check "flushed chain is rebuilt" "[ \$(rules | grep -c -- '-j DROP') -eq 6 ]"
check "physdev RETURN restored" "rules | grep -- '--physdev-is-bridged -j RETURN' >/dev/null"
iptables -D INPUT -i sdb-+ -j CREW-IN
run >/dev/null 2>&1
check "deleted INPUT jump is reinserted" "iptables -S INPUT | grep -F -- '-i sdb-+ -j CREW-IN' >/dev/null"

echo "=== 4. every IPv6 packet from a sandbox bridge is dropped (design 16.10)"
# Not only the private ranges: a sandbox has no IPv6 at all, to the machine or through it.
command -v ip6tables >/dev/null 2>&1 || { bad "ip6tables is missing: the IPv6 rules cannot be tested"; }
run >/dev/null 2>&1
check "the run exits 0" "[ \"\${RUNRC}\" = 0 ]"
check "CREW-IN6 is a bare DROP" "[ \"\$(ip6tables -S CREW-IN6 | grep '^-A')\" = '-A CREW-IN6 -j DROP' ]"
check "CREW-FWD6 is a bare DROP" "[ \"\$(ip6tables -S CREW-FWD6 | grep '^-A')\" = '-A CREW-FWD6 -j DROP' ]"
check "INPUT jumps to CREW-IN6 on the sdb- wildcard" "ip6tables -S INPUT | grep -F -- '-i sdb-+ -j CREW-IN6' >/dev/null"
check "FORWARD jumps to CREW-FWD6 on the sdb- wildcard" "ip6tables -S FORWARD | grep -F -- '-i sdb-+ -j CREW-FWD6' >/dev/null"
if [ -d /proc/sys/net/ipv6 ]; then
    check "IPv6 is off on the sandbox bridge" "[ \"\$(cat /proc/sys/net/ipv6/conf/${IFACE}/disable_ipv6)\" = 1 ]"
else
    ok "IPv6 is off in the kernel (ipv6.disable=1): nothing to switch off on the bridge"
fi
check "no ip6 rule names the lan- interface" "! ip6tables -S | grep -- '${LANIF}' >/dev/null"
check "INPUT jumps to CREW-IN6 on the mdl- wildcard" "ip6tables -S INPUT | grep -F -- '-i mdl-+ -j CREW-IN6' >/dev/null"
check "FORWARD jumps to CREW-FWD6 on the mdl- wildcard" "ip6tables -S FORWARD | grep -F -- '-i mdl-+ -j CREW-FWD6' >/dev/null"
check "INPUT jumps to CREW-IN6 on the src- wildcard" "ip6tables -S INPUT | grep -F -- '-i src-+ -j CREW-IN6' >/dev/null"
check "FORWARD jumps to CREW-FWD6 on the src- wildcard" "ip6tables -S FORWARD | grep -F -- '-i src-+ -j CREW-FWD6' >/dev/null"
for pfx in app apo; do
    check "INPUT and FORWARD jump to the IPv6 DROPs on the ${pfx}- wildcard" \
        "ip6tables -S INPUT | grep -F -- '-i ${pfx}-+ -j CREW-IN6' >/dev/null && ip6tables -S FORWARD | grep -F -- '-i ${pfx}-+ -j CREW-FWD6' >/dev/null"
done
if [ -d /proc/sys/net/ipv6 ]; then
    check "IPv6 is off on the hosted bridges" "[ \"\$(cat /proc/sys/net/ipv6/conf/${APPIF}/disable_ipv6)\$(cat /proc/sys/net/ipv6/conf/${APOIF}/disable_ipv6)\" = 11 ]"
fi
if [ -d /proc/sys/net/ipv6 ]; then
    check "IPv6 is off on the sources bridge" "[ \"\$(cat /proc/sys/net/ipv6/conf/${SRCIF}/disable_ipv6)\" = 1 ]"
fi
if [ -d /proc/sys/net/ipv6 ]; then
    check "IPv6 is off on the model only bridge" "[ \"\$(cat /proc/sys/net/ipv6/conf/${MDLIF}/disable_ipv6)\" = 1 ]"
fi
ip6tables -F CREW-FWD6
run >/dev/null 2>&1
check "a flushed CREW-FWD6 is rebuilt" "ip6tables -S CREW-FWD6 | grep -- '-j DROP' >/dev/null"

echo
echo "PASS=${PASS} FAIL=${FAIL}"
[ "${FAIL}" -eq 0 ]
