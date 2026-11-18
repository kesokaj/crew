#!/usr/bin/env bash
# Starts and stops the PostgreSQL that make check's database tests use (design 22: "a real
# PostgreSQL container"). It is the image versions.lock names, made with the same role script as an
# install, on a Docker network of its own, with throwaway passwords in .check/, which git ignores.
#   tools/check-db.sh start   start it, and wait until it answers
#   tools/check-db.sh stop    remove it, its network, and .check/
set -euo pipefail
cd "$(dirname "$0")/.."
DOCKER=${DOCKER:-docker}
NAME=crew-check-db
NET=crew-check
image=$(sed -n 's/^POSTGRES_IMAGE=//p' versions.lock)

case "${1:-}" in
start)
    if [ -f .check/secrets/db_postgres ] && $DOCKER exec "$NAME" pg_isready -q -h 127.0.0.1 -d crew -U crew 2>/dev/null; then
        echo "check-db: $NAME answers"
        exit 0
    fi
    $DOCKER rm -f "$NAME" >/dev/null 2>&1 || true
    rm -rf .check && mkdir -p .check/secrets
    for s in db_postgres db_crew db_crew_migrate db_crew_read; do
        head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n' > ".check/secrets/$s"
    done
    # Read by the database's own user in the container, and by the tests; they protect nothing.
    chmod 0755 .check .check/secrets && chmod 0644 .check/secrets/*
    $DOCKER network inspect "$NET" >/dev/null 2>&1 || $DOCKER network create "$NET" >/dev/null
    tag="${image%@*}"
    if ! $DOCKER image inspect "$image" >/dev/null 2>&1; then
        if ! $DOCKER image inspect "$tag" >/dev/null 2>&1 && [ -f /crew/cache/postgres.tar ]; then
            $DOCKER load -q -i /crew/cache/postgres.tar >/dev/null
        fi
        if $DOCKER image inspect "$tag" >/dev/null 2>&1; then
            image="$tag"
        fi
    fi
    # Tests in a package run in parallel, each with its own database and a pool of up to 16 connections (internal/db/db.go), and make check runs several packages at once, so the default 100 connections and Docker's 64 MB /dev/shm can run out.
    $DOCKER run -d --name "$NAME" --network "$NET" --shm-size 1g \
        -e POSTGRES_PASSWORD_FILE=/run/secrets/db_postgres \
        -v "$PWD/.check/secrets:/run/secrets:ro" \
        -v "$PWD/deploy/db/10-roles.sh:/docker-entrypoint-initdb.d/10-roles.sh:ro" \
        --tmpfs /var/lib/postgresql:rw \
        "$image" \
        -c max_connections=300 \
        -c fsync=off \
        -c synchronous_commit=off \
        -c full_page_writes=off \
        -c checkpoint_timeout=1h \
        -c max_wal_size=4GB \
        -c work_mem=16MB >/dev/null
    for _ in $(seq 1 300); do
        # The init scripts run on a server that listens only on its socket; the real start follows.
        if $DOCKER exec "$NAME" pg_isready -q -h 127.0.0.1 -d crew -U crew 2>/dev/null; then
            echo "check-db: $NAME answers"
            exit 0
        fi
        sleep 0.2
    done
    $DOCKER logs "$NAME" 2>&1 | tail -20
    echo "check-db: $NAME did not start" >&2
    exit 1
    ;;
stop)
    $DOCKER rm -f "$NAME" >/dev/null 2>&1 || true
    $DOCKER network rm "$NET" >/dev/null 2>&1 || true
    rm -rf .check
    ;;
*)
    echo "usage: tools/check-db.sh start|stop" >&2
    exit 2
    ;;
esac
