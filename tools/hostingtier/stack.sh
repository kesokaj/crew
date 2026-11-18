#!/usr/bin/env bash
# The Hosting tier's throwaway stack (design 22, plan M19 D4): a Crew server of this checkout with the
# machine's Docker socket, its own PostgreSQL, and an ingress built from this checkout with internal
# certificates for crew.test and the made-up apps domain apps.test, on a network of its own. Every name
# is t19-…, every secret is made up here, and nothing of the installed Crew is read or touched; the
# hosted sandboxes it starts are crew-host-… microVMs, which it removes again (down). The firewall's
# chains of the hosted bridges (machine setup 6) must be on the machine: without them the probe of
# each deploy fails, as it must.
#   stack.sh up        build and start it
#   stack.sh down      remove all of it, the hosted sandboxes, their images and networks included
#   stack.sh env       the values the test reads, as KEY=value lines
#   stack.sh test      up, the tier's test on the stack's network, and down whatever it said (make test-hosting)
set -euo pipefail
repo=$(cd "$(dirname "$0")/../.." && pwd)
w=${T19_DIR:-$repo/.agents/scratch/t19}
. "$repo/tools/tier-secrets.sh"
NET=t19-net DB=t19-db SRV=t19-server CADDY=t19-caddy
D=${DOCKER:-sudo docker}
lock() { sed -n "s/^$1=//p" "$repo/versions.lock"; }
uid=$(id -u) gid=$(id -g)
project=0f8fad5b-d9cb-469f-a165-70867728950e

# rm_gone removes a container and waits until Docker no longer has it: the kill of a running Kata
# microVM can fail while it exits ("is zombie and can not be killed"), and the container stays,
# exited, with its bridge (BUG-008).
rm_gone() {
    local i
    for i in $(seq 1 12); do
        $D rm -fv "$1" >/dev/null 2>&1 || true
        $D inspect "$1" >/dev/null 2>&1 || return 0
        sleep 5
    done
    return 1
}

down() {
    # What the server started, by its label and by its own database: each sandbox's microVM, a hosted
    # one's probe, and its bridge; then the server and the ingress, which are on those bridges; then
    # the images. Anything of it left fails (BUG-008).
    local started="" left
    started=$($D ps -a --filter label=crew.server=$SRV --format '{{.Names}}')
    if $D inspect "$DB" >/dev/null 2>&1; then
        started="$started $($D exec "$DB" psql -tA -U postgres -d crew -c "SELECT container FROM sandboxes" 2>/dev/null || true)"
    fi
    started=$(printf '%s\n' $started | sed 's/-probe$//' | sort -u)
    for c in $started; do rm_gone "$c" || true; rm_gone "$c-probe" || true; done
    for c in "$CADDY" "$SRV" "$DB"; do rm_gone "$c" || true; done
    for c in $started; do $D network rm "$c" >/dev/null 2>&1 || true; done
    $D network rm "$NET" >/dev/null 2>&1 || true
    for i in $($D image ls --format '{{.Repository}}:{{.Tag}}' | grep "^crew-host/$project:" || true); do $D rmi "$i" >/dev/null || true; done
    $D rmi t19-caddy:dev >/dev/null 2>&1 || true
    left=$( { $D ps -a --filter label=crew.server=$SRV --format '{{.Names}}'; $D ps -a --filter name=^t19- --format '{{.Names}}'
        for c in $started; do $D network inspect "$c" >/dev/null 2>&1 && echo "$c (bridge)"; done
        $D network inspect "$NET" >/dev/null 2>&1 && echo "$NET"
        $D image ls --format '{{.Repository}}:{{.Tag}}' | grep -x -e "crew-host/$project:.*" -e "t19-caddy:dev"; } | sort -u || true)
    if [ -n "$left" ]; then
        echo "t19: FAIL: left on the machine:" $left >&2
        return 1
    fi
}

case "${1:-}" in
up)
    down
    [ -d "$w/data" ] && $D run --rm -v "$w:/w" "$(lock GO_IMAGE)" rm -rf /w/data /w/src >/dev/null
    mkdir -p "$w/src" "$w/data/canonical" "$w/data/repos" "$w/data/sandboxes" "$w/data/hosted" "$w/data/builds" "$w/data/backups"
    (cd "$repo" && git ls-files -co --exclude-standard -z | tar --null -T - -c) | tar -x -C "$w/src"
    tier_secrets "$w/secrets"
    $D run --rm --user "$uid:$gid" -v "$w/src:/src" -w /src -v crew-gocache:/cache \
        -e HOME=/tmp -e GOMODCACHE=/cache/mod -e GOCACHE=/cache/build -e GOFLAGS="-mod=readonly -buildvcs=false" -e GOTOOLCHAIN=local -e CGO_ENABLED=0 \
        "$(lock GO_IMAGE)" go build -trimpath -ldflags "-X github.com/kesokaj/crew/internal/version.Version=0.0.0-dev.t19" -o /src/out/crew ./cmd/crew
    $D build -q -t t19-caddy:dev "$w/src/ingress" >/dev/null
    # The fixture's repository: main with the fixture's files, as a project with no remote has it.
    fx=$(mktemp -d)
    cp -r "$repo/tools/hostingtier/fixture/." "$fx/"
    git -C "$fx" init -q -b main && git -C "$fx" -c user.name=t19 -c user.email=t19@example.test add -A &&
        git -C "$fx" -c user.name=t19 -c user.email=t19@example.test commit -qm "the fixture"
    git clone -q --bare "$fx" "$w/data/canonical/t19app.git" && git clone -q --bare "$fx" "$w/data/repos/t19app.git"
    rm -rf "$fx"
    $D network create "$NET" >/dev/null
    $D run -d --name "$DB" --network "$NET" --shm-size 256m -e POSTGRES_PASSWORD_FILE=/run/secrets/db_postgres \
        -v "$w/secrets:/run/secrets:ro" -v "$w/src/deploy/db/10-roles.sh:/docker-entrypoint-initdb.d/10-roles.sh:ro" \
        --tmpfs /var/lib/postgresql:rw "$(lock POSTGRES_IMAGE)" >/dev/null
    for _ in $(seq 1 60); do $D exec "$DB" pg_isready -q -h 127.0.0.1 -d crew -U crew 2>/dev/null && break; sleep 1; done
    runtime=$($D image ls --format '{{.Repository}}:{{.Tag}}' crew-runtime | sort -V | tail -1)
    [ -n "$runtime" ] || { echo "t19: no crew-runtime image on this machine" >&2; exit 1; }
    sock=$(stat -c %g /var/run/docker.sock)
    env=(-e CREW_DB_HOST="$DB" -e CREW_HOSTNAME=crew.test -e CREW_APPS_DOMAIN=apps.test -e CREW_ADMIN_EMAIL=admin@example.test
        -e CREW_DATA_DIR="$w/data" -e CREW_INGRESS_TOKEN_FILE=/run/secrets/ingress_token -e CREW_RUNTIME_IMAGE="$runtime"
        -e CREW_SELF_CONTAINER="$SRV" -e CREW_INGRESS_CONTAINER="$CADDY" -e CREW_TLS=internal -e CREW_INGRESS_ADDR="$CADDY:443" -e HOME=/tmp)
    mounts=(-v "$w/src/out:/opt/crew:ro" -v "$w/secrets:/run/secrets:ro" -v "$w/data:$w/data")
    $D run --rm --user "$uid:$gid" --network "$NET" "${env[@]}" "${mounts[@]}" "$(lock GO_IMAGE)" /opt/crew/crew admin migrate >/dev/null
    $D exec "$DB" psql -q -v ON_ERROR_STOP=1 -U postgres -d crew -c "INSERT INTO projects (id, name, key, repo_source, state) VALUES ('$project', 't19app', 'TA', 'local', 'done');
        INSERT INTO git_repos (project, name) VALUES ('$project', 't19app') ON CONFLICT DO NOTHING;
        INSERT INTO settings (project, doc, updated_by) VALUES ('$project', '{\"version\": 1, \"project\": \"t19app\", \"key\": \"TA\", \"hosting\": {\"public_rps\": 2}}', 't19')" >/dev/null
    # The ingress reaches the server by an alias of this network alone, as crew-server-door on Compose's:
    # the server's own name could answer with its address on a hosted bridge, where the door is a
    # sandbox's and refuses (plan M19 D25). The server's door answers only that alias's network (13.6).
    # It joins every sandbox's bridge, so it routes nothing, as compose starts crew-server (13.6).
    $D run -d --name "$SRV" --user "$uid:$gid" --group-add "$sock" --network "$NET" --network-alias t19-server-door -e CREW_DOOR_NAME=t19-server-door "${env[@]}" "${mounts[@]}" \
        --sysctl net.ipv4.ip_forward=0 -v /var/run/docker.sock:/var/run/docker.sock "$(lock GO_IMAGE)" /opt/crew/crew serve >/dev/null
    $D run -d --name "$CADDY" --user "$uid:$gid" --network "$NET" \
        --network-alias crew.test --network-alias auth.crew.test --network-alias auth.apps.test --network-alias t19app.apps.test \
        --sysctl net.ipv4.ip_unprivileged_port_start=0 --sysctl net.ipv4.ip_forward=0 -v "$w/secrets:/run/secrets:ro" \
        -e BASE_DOMAIN=crew.test -e ADMIN_EMAIL=admin@example.test -e APPS_DOMAIN=apps.test -e CREW_TLS=internal \
        -e CREW_SERVER_ADDR=t19-server-door:8080 -e XDG_DATA_HOME=/tmp/data -e XDG_CONFIG_HOME=/tmp/config \
        --entrypoint /bin/sh t19-caddy:dev -c 'CREW_INGRESS_TOKEN=$(cat /run/secrets/ingress_token) CREW_JWT_KEY=$(cat /run/secrets/jwt_key) exec caddy ingress' >/dev/null
    for _ in $(seq 1 90); do
        $D exec "$SRV" sh -c 'wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || curl -fs http://127.0.0.1:8080/healthz >/dev/null' && { echo "t19: up ($runtime)"; exit 0; }
        sleep 1
    done
    $D logs "$SRV" 2>&1 | tail -20
    echo "t19: the server did not answer" >&2
    exit 1
    ;;
test)
    trap '"$0" down || exit 1' EXIT
    "$0" up
    # The test's keys go by a file only the caller reads, never on a command line any user's ps shows
    # (security review of the release of M20, round 2).
    (umask 077 && "$0" env > "$w/secrets/test.env")
    sock=$(stat -c %g /var/run/docker.sock)
    $D run --rm --user "$uid:$gid" --group-add "$sock" --network "$NET" -v /var/run/docker.sock:/var/run/docker.sock \
        -v "$w/src:/src" -w /src -v crew-gocache:/cache --env-file "$w/secrets/test.env" \
        -e HOME=/tmp -e GOMODCACHE=/cache/mod -e GOCACHE=/cache/build -e GOFLAGS="-mod=readonly -buildvcs=false" -e GOTOOLCHAIN=local \
        "$(lock GO_IMAGE)" go test -tags hosting -count=1 -v -timeout 60m ./tools/hostingtier/
    ;;
env)
    echo "T19_INGRESS_TOKEN=$(cat "$w/secrets/ingress_token")"
    echo "T19_JWT_KEY=$(cat "$w/secrets/jwt_key")"
    echo "T19_PROJECT=$project"
    ;;
down)
    rc=0
    down || rc=1
    [ -d "$w" ] && $D run --rm -v "$w:/w" "$(lock GO_IMAGE)" rm -rf /w/data /w/src /w/secrets >/dev/null
    [ $rc = 0 ] && echo "t19: removed"
    exit $rc
    ;;
*)
    echo "usage: stack.sh up | env | test | down" >&2
    exit 2
    ;;
esac
