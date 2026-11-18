#!/usr/bin/env bash
# crew-firewall.sh — sandbox network isolation (design 13.6). Installed to
# /usr/local/sbin by deploy/firewall/install.sh; run at boot after Docker and by
# crew-firewall.timer every 60 s.
#
# ONE job: keep every sandbox bridge in Internet mode (sdb-*) isolated
# from the machine, the local network, other sandboxes, cloud metadata, and the
# carrier range, while leaving the internet and the same-bridge broker
# reachable; and keep every bridge in Model only mode (mdl-*) from reaching
# anything but the same-bridge server, whose proxy lets through the few hosts
# its CLI needs; a bridge in Sources mode (src-*), the build of a release, the same; and on a hosted
# project's bridge (app-*, apo-*) let only the server and the ingress open a connection. Self-heal-safe: every rule is INSERT-IF-MISSING,
# never delete-then-insert, so the 60 s timer re-runs open no unprotected
# window.
#
# The mode of a sandbox is the bridge INTERFACE NAME, which the server picks at `docker network
# create` (design 13.6). A LAN sandbox's bridge is `lan-<id>`, so the `-i sdb-+` jumps below do not
# match it and it behaves like any other Docker container. Root parses no input from the server.
#
# Cross-project isolation does NOT depend on this script: Docker writes
# `-A DOCKER ! -i <br> -o <br> -j DROP` per bridge, which is what enforces
# the cross-project isolation for both modes.
#
# Paths come from the environment with NO defaults, so a test cannot
# reach a production path by omission. Only the cleanup below reads one,
# and it writes nothing outside iptables.
set -eu
# Command substitutions inherit errexit too: a failure inside $(...) must
# abort the run like any other (a swallowed mktemp failure once hid a
# broken status write).
shopt -s inherit_errexit

log()  { echo "crew-firewall: $*"; }
WARNS=()
# warn: journal + status. note: status only (a standing condition such as a
# missing conf must not write a journal line every minute).
warn() { echo "crew-firewall: WARN $*" >&2; WARNS+=("$*"); }
note() { WARNS+=("$*"); }

ip4="$(command -v iptables)"
# -w 5: wait for a concurrent netlink writer (dockerd) instead of failing the
# whole run under set -e.
ipt() { "$ip4" -w 5 "$@"; }

# ─── BASE: isolation rules, unchanged but for -w ─────────────────────────────
# The `-i sdb-+` wildcard appears only on the chain jumps (matched textually
# via -S, which renders it stably); the chains' own rules carry no wildcard,
# so `-C` is reliable for them.
modprobe xt_physdev 2>/dev/null || true   # same-bridge exemption below needs it

ensure_chain() { ipt -nL "$1" >/dev/null 2>&1 || ipt -N "$1"; }
ensure_rule()  { local c="$1"; shift; ipt -C "$c" "$@" 2>/dev/null || ipt -A "$c" "$@"; }
ensure_jump()  { # parent iface target — textual presence test is wildcard-safe
    ipt -S "$1" 2>/dev/null | grep -F -- "-i $2 -j $3" >/dev/null || ipt -I "$1" -i "$2" -j "$3"
}

# Host itself: a project bridge may send it nothing, in either mode.
ensure_chain CREW-IN
ensure_rule  CREW-IN -j DROP
ensure_jump  INPUT sdb-+ CREW-IN
ensure_jump  INPUT mdl-+ CREW-IN
ensure_jump  INPUT src-+ CREW-IN
ensure_jump  INPUT app-+ CREW-IN
ensure_jump  INPUT apo-+ CREW-IN

# Routed egress: RETURN genuinely same-bridge L2 first (broker + proxy on the
# project's OWN bridge), then drop every private range. Other projects live in
# 172.16/12 too, so cross-project stays blocked; public internet falls through.
# The per-project LAN allowances (below) are inserted AHEAD of all of these.
ensure_chain CREW-FWD
# Through warn(), not a bare echo: without xt_physdev the same-bridge RETURN
# is missing and the private-range DROP below swallows the credential broker
# and the port preview for EVERY sandbox project — a degraded state that must
# reach the exit path like every other one here, not read as green in
# systemd.
ensure_rule  CREW-FWD -m physdev --physdev-is-bridged -j RETURN 2>/dev/null \
    || warn "xt_physdev unavailable — the same-bridge RETURN is missing, so the credential broker and port preview are blocked for sandbox projects"
# 100.64.0.0/10 is CGNAT — where Tailscale/headscale put their tailnet, so a
# project could otherwise reach the operator's whole private mesh; 198.18.0.0/15
# is the benchmark range some VPNs and container fabrics route real traffic on.
# Both were missing from the original RFC1918 list.
for net in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16 \
           100.64.0.0/10 198.18.0.0/15; do
    ensure_rule CREW-FWD -d "$net" -j DROP
done
ensure_jump  DOCKER-USER sdb-+ CREW-FWD

# Model only (13.6): nothing routed at all. The same-bridge RETURN keeps the
# server, which is the broker, the git endpoint, the Crew MCP, and the proxy;
# everything else, the internet included, is dropped. Without xt_physdev the
# server is blocked too, which fails closed, and is a warning as above.
ensure_chain CREW-MDL
ensure_rule  CREW-MDL -m physdev --physdev-is-bridged -j RETURN 2>/dev/null \
    || warn "xt_physdev unavailable — the same-bridge RETURN is missing, so Model only sandboxes cannot reach the server"
ensure_rule  CREW-MDL -j DROP
ensure_jump  DOCKER-USER mdl-+ CREW-MDL
# Sources (13.6, since 0.51), the build of a release: the same chain. Only the list its proxy
# lets through differs, and the proxy is the server's.
ensure_jump  DOCKER-USER src-+ CREW-MDL

# A hosted project (19.4, since 0.72): app- with the internet from inside, apo- without. On its own
# bridge only the server and the ingress open a connection, from their fixed addresses, .254 and .253
# of the bridge's /24 in the pool 172.16.0.0/12, which the server checks before it starts one; the
# app answers them and opens nothing there. Routed, app- reaches the internet and no private range,
# apo- nothing. Without xt_physdev the RETURNs are missing and the DROPs stay, which fails closed.
for chain in CREW-APP CREW-APO; do
    ensure_chain "$chain"
    ensure_rule  "$chain" -m conntrack --ctstate RELATED,ESTABLISHED -j RETURN
    ensure_rule  "$chain" -s 172.16.0.254/255.240.0.255 -m physdev --physdev-is-bridged -j RETURN 2>/dev/null \
        || warn "xt_physdev unavailable — the server cannot reach a hosted project"
    ensure_rule  "$chain" -s 172.16.0.253/255.240.0.255 -m physdev --physdev-is-bridged -j RETURN 2>/dev/null \
        || warn "xt_physdev unavailable — the ingress cannot reach a hosted project"
    ensure_rule  "$chain" -m physdev --physdev-is-bridged -j DROP 2>/dev/null || true
done
for net in 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 169.254.0.0/16 \
           100.64.0.0/10 198.18.0.0/15; do
    ensure_rule CREW-APP -d "$net" -j DROP
done
ensure_rule  CREW-APO -j DROP
ensure_jump  DOCKER-USER app-+ CREW-APP
ensure_jump  DOCKER-USER apo-+ CREW-APO

# IPv6: Docker gives a sandbox no IPv6 address (ipv6 off in daemon.json), and a sandbox bridge has
# IPv6 switched off. All the same, every IPv6 packet from a sandbox bridge is dropped, to the machine
# and through it (design 16.10). A rule that cannot be put in place is a warning, not a silence.
# A kernel booted with ipv6.disable=1 has no IPv6 to switch off.
if [ -d /proc/sys/net/ipv6 ]; then
    for ifp in /sys/class/net/sdb-* /sys/class/net/mdl-* /sys/class/net/src-* /sys/class/net/app-* /sys/class/net/apo-*; do
        [ -e "$ifp" ] || continue
        sysctl -qw "net.ipv6.conf.${ifp##*/}.disable_ipv6=1" 2>/dev/null \
            || warn "could not switch IPv6 off on ${ifp##*/}"
    done
fi
if command -v ip6tables >/dev/null 2>&1; then
    ip6="$(command -v ip6tables)"
    ip6t() { "$ip6" -w 5 "$@"; }
    for pair in INPUT:CREW-IN6 FORWARD:CREW-FWD6; do
        parent="${pair%%:*}" chain="${pair##*:}"
        ip6t -nL "$chain" >/dev/null 2>&1 || ip6t -N "$chain" 2>/dev/null || warn "could not make ${chain}"
        ip6t -C "$chain" -j DROP 2>/dev/null || ip6t -A "$chain" -j DROP 2>/dev/null || warn "could not add the DROP to ${chain}"
        for pfx in sdb-+ mdl-+ src-+ app-+ apo-+; do
            ip6t -S "$parent" 2>/dev/null | grep -F -- "-i ${pfx} -j ${chain}" >/dev/null \
                || ip6t -I "$parent" -i "$pfx" -j "$chain" 2>/dev/null || warn "could not jump from ${parent} to ${chain} for ${pfx}"
        done
    done
else
    warn "ip6tables is missing: IPv6 from sandbox bridges is not dropped"
fi

if [ "${#WARNS[@]}" -gt 0 ]; then
    exit 1
fi
exit 0
