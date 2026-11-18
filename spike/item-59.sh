#!/usr/bin/env bash
# Spike item 59 (M1, on dave once nothing else runs there): does a command that was started detached
# in a microVM keep running, and can its end be read, after the server's container restarts and after
# Docker restarts with live restore? As root on the machine, with Crew installed. It starts its own
# microVM from the runtime base image, runs a 150 s command detached in it that writes its progress
# and its end to a file in the sandbox, restarts crew-server, then restarts Docker, and reads the
# file. It removes its microVM, however it ends.
set -euo pipefail
IMG="$(sed -n 's/^RUNTIME_BASE_IMAGE=//p' "$(dirname "$0")/../versions.lock")"
NAME=crew-spike-item59
cleanup() { docker rm -f "${NAME}" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup
now() { date -u +%T; }

docker run -d --name "${NAME}" --runtime mvm "${IMG}" sleep infinity >/dev/null
# Detached, as the run driver starts crew-run: docker exec -d, and the process writes its own log.
docker exec -d "${NAME}" sh -c 'for i in $(seq 1 30); do echo "tick $i $(date -u +%T)" >> /tmp/run.log; sleep 5; done; echo "ended 0 $(date -u +%T)" >> /tmp/run.log'
sleep 12
echo "$(now) before: $(docker exec "${NAME}" tail -1 /tmp/run.log)"

echo "$(now) restarting crew-server"
docker restart crew-server >/dev/null
sleep 10
echo "$(now) after the server's restart: $(docker exec "${NAME}" tail -1 /tmp/run.log)"

echo "$(now) restarting Docker (live-restore: $(docker info -f '{{.LiveRestoreEnabled}}'))"
systemctl restart docker
sleep 15
state="$(docker inspect -f '{{.State.Status}} started {{.State.StartedAt}}' "${NAME}" 2>&1)"
echo "$(now) after Docker's restart: the microVM is ${state}"
echo "$(now) its log: $(docker exec "${NAME}" tail -1 /tmp/run.log 2>&1)"

for _ in $(seq 1 40); do
    if docker exec "${NAME}" grep -q '^ended' /tmp/run.log 2>/dev/null; then break; fi
    sleep 5
done
echo "$(now) the end: $(docker exec "${NAME}" grep '^ended' /tmp/run.log 2>&1)"
echo "$(now) ticks written: $(docker exec "${NAME}" grep -c '^tick' /tmp/run.log 2>&1) of 30"
docker exec "${NAME}" sh -c 'awk "{print \$3}" /tmp/run.log' | python3 -c '
import sys
from datetime import datetime
t = [datetime.strptime(x.strip(), "%H:%M:%S") for x in sys.stdin if x.strip()]
gaps = [(b - a).seconds for a, b in zip(t, t[1:])]
print("largest gap between ticks: %d s (a tick every 5 s means the command never stopped)" % max(gaps))'
