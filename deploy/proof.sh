#!/usr/bin/env bash
# The sandbox part of install's proof (design 16.11, 13.6; spike item 29). As root on the machine:
# starts one microVM on a sandbox bridge (sdb-), the kind of network an Internet-mode sandbox gets,
# and from inside it shows that it has Kata's own kernel and no IPv6, that it reaches the internet,
# and that it reaches neither the machine, nor the database, nor a cloud's metadata address, nor the
# local network. It removes what it made, however it ends. PROOF_IMAGE is the image to start.
set -euo pipefail
: "${PROOF_IMAGE:?set PROOF_IMAGE}"
NET=crew-proof
BRIDGE=sdb-crewproof
cleanup() { docker network rm "${NET}" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
docker network create --driver bridge -o "com.docker.network.bridge.name=${BRIDGE}" "${NET}" >/dev/null

# Addresses that must not answer: the machine's own (every one it has, the bridge's gateway too),
# the database, a cloud's metadata address, and the machine's default gateway on the local network.
host_ips="$(hostname -I 2>/dev/null || true)"
gateway="$(docker network inspect -f '{{range .IPAM.Config}}{{.Gateway}}{{end}}' "${NET}")"
lan_gw="$(ip -4 route show default 2>/dev/null | awk '!f {print $3; f = 1}')"
db_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' crew-db 2>/dev/null || true)"

docker run --rm --runtime mvm --network "${NET}" --dns 8.8.8.8 \
    -e "HOST_KERNEL=$(uname -r)" -e "HOST_IPS=${host_ips} ${gateway}" -e "LAN_GW=${lan_gw}" -e "DB_IP=${db_ip}" \
    "${PROOF_IMAGE}" bash -c '
set -u
fails=0
# Kata has no Docker resolver inside the microVM: 127.0.0.11 is the guest'"'"'s own loopback. The
# entrypoint of the runtime image writes two public resolvers (13.6); this bare image gets them here.
printf "nameserver 8.8.8.8\nnameserver 1.1.1.1\n" > /etc/resolv.conf
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*"; fails=$((fails + 1)); }
reach() { timeout 3 bash -c "exec 3<>/dev/tcp/$1/$2" 2>/dev/null; }

k="$(uname -r)"
[ "$k" != "$HOST_KERNEL" ] && pass "the microVM runs its own kernel, $k" || fail "the microVM runs the machine'"'"'s kernel"

# IPv6: the guest kernel may give itself a link-local address (fe80::, scope 20) and its route,
# which reach no further than the sandbox'"'"'s own bridge. Any other address, or any route that is
# not link-local, multicast, or loopback, is a way out.
if [ -e /proc/net/if_inet6 ] && awk "\$6 != \"lo\" && \$4 != \"20\"" /proc/net/if_inet6 | grep . >/dev/null; then
    fail "the sandbox has an IPv6 address that is not link-local"
else pass "no IPv6 address but link-local"; fi
if [ -e /proc/net/ipv6_route ] && awk "\$10 != \"lo\" && \$1 !~ /^fe80/ && \$1 !~ /^ff/" /proc/net/ipv6_route | grep . >/dev/null; then
    fail "the sandbox has an IPv6 route that is not link-local"
else pass "no IPv6 route but link-local"; fi
getent hosts github.com >/dev/null && pass "resolves github.com" || fail "cannot resolve github.com"
reach github.com 443 && pass "reaches the internet (github.com:443)" || fail "cannot reach the internet"

for ip in $HOST_IPS; do
    case "$ip" in *:*) continue ;; esac
    for port in 22 80 443 8080; do
        reach "$ip" "$port" && fail "reaches the machine at $ip:$port" || pass "does not reach the machine at $ip:$port"
    done
done
for ip in $DB_IP; do
    reach "$ip" 5432 && fail "reaches the database at $ip:5432" || pass "does not reach the database at $ip:5432"
done
reach 169.254.169.254 80 && fail "reaches the metadata address" || pass "does not reach the metadata address"
if [ -n "$LAN_GW" ]; then
    for port in 22 53 80 443; do
        reach "$LAN_GW" "$port" && fail "reaches the local network at $LAN_GW:$port" || pass "does not reach the local network at $LAN_GW:$port"
    done
fi
echo "proof: $fails failure(s)"
exit $(( fails > 0 ))
'
