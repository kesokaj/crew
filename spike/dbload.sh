#!/usr/bin/env bash
# Spike items 44 and 46 (design section 24), on a throwaway PostgreSQL with crew-db's image, memory
# limit, and shared memory, on a Docker network of its own, with its data in a Docker volume of its
# own. Crew's own database is not touched. Everything is named crew-spike-dbload and removed at the end.
#
#   spike/dbload.sh load WRITERS DURATION   item 46: the real driver, WRITERS runs at once; the
#                                           database's memory is sampled every 2 seconds
#   spike/dbload.sh fill MEGABYTES          item 44: events up to MEGABYTES, then Crew's own dump
#                                           and restore, timed
# The output goes to spike/raw/dbload/.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=crew-spike-dbload
NET=crew-spike-dbload
VOL=crew-spike-dbload
OUT=spike/raw/dbload
image=$(sed -n 's/^POSTGRES_IMAGE=//p' versions.lock)
check=$(sudo docker images --format '{{.Repository}}:{{.Tag}}' | grep '^crew-check:' | head -1)
[ -n "${check}" ] || { echo "dbload: build the check image first: make check-image" >&2; exit 1; }

cleanup() {
    [ -n "${SAMPLER:-}" ] && kill "${SAMPLER}" 2>/dev/null || true
    sudo docker rm -f "${NAME}" >/dev/null 2>&1 || true
    sudo docker network rm "${NET}" >/dev/null 2>&1 || true
    sudo docker volume rm "${VOL}" >/dev/null 2>&1 || true
    rm -rf "${OUT}/secrets" "${OUT}/dump"
}
trap cleanup EXIT
cleanup
mkdir -p "${OUT}/secrets" "${OUT}/dump"
chmod 0700 spike/raw
for s in db_postgres db_crew db_crew_migrate db_crew_read; do
    head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n' > "${OUT}/secrets/$s"
done
# Read by the database's own user in the container; they protect a throwaway database only.
chmod 0755 "${OUT}" "${OUT}/secrets" && chmod 0644 "${OUT}/secrets/"* && chmod 0777 "${OUT}/dump"
sudo docker network create "${NET}" >/dev/null
sudo docker volume create "${VOL}" >/dev/null
# crew-db's limits (deploy/compose.yaml): 2 GB of memory and 256 MB of shared memory.
sudo docker run -d --name "${NAME}" --network "${NET}" --memory 2g --shm-size 256m \
    -e POSTGRES_PASSWORD_FILE=/run/secrets/db_postgres \
    -v "${PWD}/${OUT}/secrets:/run/secrets:ro" \
    -v "${PWD}/deploy/db/10-roles.sh:/docker-entrypoint-initdb.d/10-roles.sh:ro" \
    -v "${VOL}:/var/lib/postgresql" "${image}" >/dev/null
for _ in $(seq 1 60); do
    sudo docker exec "${NAME}" pg_isready -q -h 127.0.0.1 -d crew -U crew 2>/dev/null && break
    sleep 1
done

go() { # go ARGS...: dbload in the check image, as the server's roles, on the throwaway network
    sudo docker run --rm --user "$(id -u):$(id -g)" -v "${PWD}:/src" -w /src -v crew-gocache:/cache \
        -e HOME=/tmp -e GOMODCACHE=/cache/mod -e GOCACHE=/cache/build -e GOFLAGS="-mod=readonly -buildvcs=false" \
        -e GOTOOLCHAIN=local --network "${NET}" -e CREW_DB_HOST="${NAME}" -e CREW_DB_SECRETS_DIR="/src/${OUT}/secrets" \
        -e DBLOAD_RESTORE_DB=crew_restore "${check}" go run ./spike/dbload "$@"
}
go migrate

case "${1:-}" in
load)
    stamp=$(date +%F-%H%M)
    # The database's memory as Docker counts it, every 2 seconds, while the writers run.
    ( while true; do
        printf '%s %s\n' "$(date +%T)" "$(sudo docker stats --no-stream --format '{{.MemUsage}} {{.CPUPerc}}' "${NAME}")"
        sleep 2
      done ) > "${OUT}/load-${stamp}-mem.txt" &
    SAMPLER=$!
    go load "$2" "$3" | tee "${OUT}/load-${stamp}.txt"
    kill "${SAMPLER}"; SAMPLER=
    echo "memory: $(awk '{print $2}' "${OUT}/load-${stamp}-mem.txt" | sort -h | tail -1) at most of 2GiB; samples in ${OUT}/load-${stamp}-mem.txt"
    sudo docker exec "${NAME}" psql -U postgres -d crew -Atc "SHOW shared_buffers" | sed 's/^/shared_buffers /'
    ;;
fill)
    stamp=$(date +%F-%H%M)
    go fill "$2" | tee "${OUT}/fill-${stamp}.txt"
    # The second database is made as an installation's is, so the restore runs as Crew's does.
    sudo docker exec "${NAME}" psql -U postgres -d postgres -qc "CREATE DATABASE crew_restore OWNER crew_migrate"
    go dumprestore "/src/${OUT}/dump" | tee -a "${OUT}/fill-${stamp}.txt"
    ;;
*)
    echo "usage: spike/dbload.sh load WRITERS DURATION | fill MEGABYTES" >&2
    exit 2
    ;;
esac
