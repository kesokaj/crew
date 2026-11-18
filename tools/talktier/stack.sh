#!/usr/bin/env bash
# The real tier of conversations (plan M20 D24, D39): a Crew server of this checkout with the machine's
# Docker socket and its own PostgreSQL, on a network of its own, and a project t20app whose main and
# one ticket's branch are made here. Every name is t20-…, every secret but agy's login is made up here,
# and nothing of the installed Crew is read or touched but the name of its runtime image. The sandboxes
# start from that image with this checkout's crew-run in it (t20-runtime:dev), since the chat's
# workspace is fetched again by crew-run (D7).
#
# agy runs only for the turns the test asks for (at most 6, D24), never by Crew itself:
#   - the scheduled login check and its daily real request: cli_logins is seeded as checked a day
#     ahead, so none is due (internal/logins Schedule, due);
#   - a login check after a turn that ended for its login or a limit (internal/runs events.go): the
#     guard below removes every login-check sandbox of t20-server on its create event, before its
#     microVM boots, and the test fails on any line of its log;
#   - the pipeline (analysis, builds, summaries, the setup's checks): the project is paused and no
#     ticket finished, so no Analysis run, no improver and no eval suite starts;
#   - the prompt writer and the planner: they run only when asked, and the test asks neither.
# The operator's agy login is read by path from secrets/agy-login (gitignored), stored with crew admin
# login set in a container of its own, never printed, and removed with the stack (D39).
#   stack.sh up        build and start it (without the login file, a dry run whose turns would wait)
#   stack.sh down      remove all of it, the sandboxes and the login file included
#   stack.sh env       the values the test reads, as KEY=value lines
#   stack.sh test      up, the tier's test on the stack's network, and down whatever it said (make test-talk)
# A test that was killed leaves the stack and the login file: stack.sh down removes them.
set -euo pipefail
repo=$(cd "$(dirname "$0")/../.." && pwd)
w=${T20_DIR:-$repo/.agents/scratch/t20}
. "$repo/tools/tier-secrets.sh"
NET=t20-net DB=t20-db SRV=t20-server RUNTIME=t20-runtime:dev
D=${DOCKER:-sudo docker}
lock() { sed -n "s/^$1=//p" "$repo/versions.lock"; }
uid=$(id -u) gid=$(id -g)
project=7e3c2a10-5b9d-4f61-8a2e-1d0c4b7f9a20
login=$w/secrets/agy-login

# base_commit is the commit a runtime image was built from: a dev build's tag names it; a release's tag
# v<version> when Crew's copy to GitHub made one, else the commit its release folder names, as a machine
# with updates.publish off has no tag (BUG-017). T20_BASE_COMMIT names it by hand.
base_commit() {
    local tag=${1##*:} r=${T20_RELEASES:-${CREW_DATA_DIR:-/var/lib/crew}/releases} c
    if [ -n "${T20_BASE_COMMIT:-}" ]; then echo "$T20_BASE_COMMIT"; return; fi
    case "$tag" in *-dev.*) echo "${tag##*.}"; return ;; esac
    if git -C "$repo" rev-parse -q --verify "v$tag^{commit}" >/dev/null; then echo "v$tag"; return; fi
    # The release folder is the crew user's: read by root when this user cannot.
    c=$( { cat "$r/$tag/release.json" 2>/dev/null || sudo -n cat "$r/$tag/release.json" 2>/dev/null; } |
        python3 -I -c 'import json, sys; print(json.load(sys.stdin)["commit"])' 2>/dev/null) || c=""
    [[ "$c" =~ ^[0-9a-f]{7,40}$ ]] || { echo "t20: no commit of $1: no tag v$tag and no release folder $r/$tag that names one; set T20_BASE_COMMIT" >&2; return 1; }
    echo "$c"
}

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
    # What the server started, by its label and by its own database: each sandbox's microVM and its
    # bridge; then the server, the database, and the image of the runtime. Anything of it left fails.
    local started="" left
    started=$($D ps -a --filter label=crew.server=$SRV --format '{{.Names}}')
    if $D inspect "$DB" >/dev/null 2>&1; then
        started="$started $($D exec "$DB" psql -tA -U postgres -d crew -c "SELECT container FROM sandboxes" 2>/dev/null || true)"
    fi
    started=$(printf '%s\n' $started | sort -u)
    for c in $started; do rm_gone "$c" || true; done
    for c in "$SRV" "$DB"; do rm_gone "$c" || true; done
    for c in $started; do $D network rm "$c" >/dev/null 2>&1 || true; done
    $D network rm "$NET" >/dev/null 2>&1 || true
    $D rmi "$RUNTIME" >/dev/null 2>&1 || true
    left=$( { $D ps -a --filter label=crew.server=$SRV --format '{{.Names}}'; $D ps -a --filter name=^t20- --format '{{.Names}}'
        for c in $started; do $D network inspect "$c" >/dev/null 2>&1 && echo "$c (bridge)"; done
        $D network inspect "$NET" >/dev/null 2>&1 && echo "$NET"; $D image inspect "$RUNTIME" >/dev/null 2>&1 && echo "$RUNTIME"; } | sort -u || true)
    if [ -n "$left" ]; then
        echo "t20: FAIL: left on the machine:" $left >&2
        return 1
    fi
}

# guard removes every login-check sandbox t20-server makes, on its create event, and writes a line for
# each to logs/login-checks.log. It runs until killed by its pid.
guard() {
    mkdir -p "$w/logs"
    : > "$w/logs/login-checks.log"
    $D events --filter type=container --filter event=create \
        --filter label=crew.server=$SRV --filter label=crew.kind=login-check --format '{{.Actor.ID}}' |
        while read -r id; do
            echo "$(date -u +%FT%TZ) removed a login-check sandbox $id" >> "$w/logs/login-checks.log"
            $D rm -fv "$id" >/dev/null 2>&1 || true
        done
}

# stop ends a process and everything it started, by their pids.
stop() {
    local c
    for c in $(pgrep -P "$1" || true); do stop "$c"; done
    kill "$1" 2>/dev/null || true
}

case "${1:-}" in
up)
    # The runtime image the installed server names, with this checkout's crew-run: image/ and harness/
    # are as that release built them, or the stack does not start; checked before anything is built.
    base=${T20_BASE_RUNTIME:-$($D inspect crew-server --format '{{range .Config.Env}}{{println .}}{{end}}' 2>/dev/null | sed -n 's/^CREW_RUNTIME_IMAGE=//p')}
    [ -n "$base" ] || { echo "t20: no runtime image of the installed server; set T20_BASE_RUNTIME" >&2; exit 1; }
    commit=$(base_commit "$base")
    git -C "$repo" diff --quiet "$commit" HEAD -- image harness ||
        { echo "t20: image/ or harness/ changed since $base ($commit): the runtime image must be built anew" >&2; exit 1; }
    down
    [ -d "$w/data" ] && $D run --rm -v "$w:/w" "$(lock GO_IMAGE)" rm -rf /w/data /w/src /w/run /w/fx >/dev/null
    mkdir -p "$w/src" "$w/data/canonical" "$w/data/repos" "$w/data/sandboxes" "$w/data/builds" "$w/data/backups"
    (cd "$repo" && git ls-files -co --exclude-standard -z | tar --null -T - -c) | tar -x -C "$w/src"
    # The made-up secrets go in run/, which the server mounts; the operator's login stays in secrets/,
    # which only the container that stores it mounts.
    tier_secrets "$w/run"
    # The markers the test asks for: only the files hold them, never a message.
    for m in mark_main mark_branch; do head -c 6 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$w/run/$m"; done
    go_build() {
        $D run --rm --user "$uid:$gid" -v "$w/src:/src" -w /src -v crew-gocache:/cache \
            -e HOME=/tmp -e GOMODCACHE=/cache/mod -e GOCACHE=/cache/build -e GOFLAGS="-mod=readonly -buildvcs=false" -e GOTOOLCHAIN=local -e CGO_ENABLED=0 \
            "$(lock GO_IMAGE)" go build -trimpath -ldflags "-s -w -X github.com/kesokaj/crew/internal/version.Version=0.0.0-dev.t20" "$@"
    }
    go_build -o /src/out/crew ./cmd/crew
    go_build -o /src/rt/crew-run ./cmd/crew-run
    printf 'FROM %s\nCOPY crew-run /usr/local/bin/crew-run\n' "$base" > "$w/src/rt/Dockerfile"
    $D build -q -t "$RUNTIME" "$w/src/rt" >/dev/null
    # The fixture's repository: main with a README that holds one marker, and the ticket's branch with
    # a NOTE.md that holds the other, as a project with no remote has them.
    fx=$w/fx
    rm -rf "$fx" && mkdir -p "$fx"
    g() { git -C "$fx" -c user.name=t20 -c user.email=t20@example.test "$@"; }
    git -C "$fx" init -q -b main
    printf '# t20app\n\nThe fixture of the conversations tier. Its mark is %s.\n' "$(cat "$w/run/mark_main")" > "$fx/README.md"
    g add -A && g commit -qm "the fixture"
    g checkout -qb task/TA-1-note
    printf 'The note of TA-1. Its mark is %s.\n' "$(cat "$w/run/mark_branch")" > "$fx/NOTE.md"
    g add -A && g commit -qm "TA-1: the note"
    g checkout -q main
    git clone -q --bare "$fx" "$w/data/canonical/t20app.git" && git clone -q --bare "$fx" "$w/data/repos/t20app.git"
    rm -rf "$fx"
    $D network create "$NET" >/dev/null
    $D run -d --name "$DB" --network "$NET" --shm-size 256m -e POSTGRES_PASSWORD_FILE=/run/secrets/db_postgres \
        -v "$w/run:/run/secrets:ro" -v "$w/src/deploy/db/10-roles.sh:/docker-entrypoint-initdb.d/10-roles.sh:ro" \
        --tmpfs /var/lib/postgresql:rw "$(lock POSTGRES_IMAGE)" >/dev/null
    for _ in $(seq 1 60); do $D exec "$DB" pg_isready -q -h 127.0.0.1 -d crew -U crew 2>/dev/null && break; sleep 1; done
    sock=$(stat -c %g /var/run/docker.sock)
    env=(-e CREW_DB_HOST="$DB" -e CREW_HOSTNAME=crew.test -e CREW_ADMIN_EMAIL=admin@example.test
        -e CREW_DATA_DIR="$w/data" -e CREW_INGRESS_TOKEN_FILE=/run/secrets/ingress_token -e CREW_RUNTIME_IMAGE="$RUNTIME"
        -e CREW_SELF_CONTAINER="$SRV" -e CREW_TLS=internal -e CREW_INGRESS_ADDR=t20-none:443 -e HOME=/tmp)
    mounts=(-v "$w/src/out:/opt/crew:ro" -v "$w/run:/run/secrets:ro" -v "$w/data:$w/data")
    $D run --rm --user "$uid:$gid" --network "$NET" "${env[@]}" "${mounts[@]}" "$(lock GO_IMAGE)" /opt/crew/crew admin migrate >/dev/null
    # The project, paused, with its ticket at work on its branch; the machine's settings: mail to a
    # port where nothing answers, the shortest idle time, and the assistant on a quick model; agy's
    # login as checked a day ahead, so no scheduled check is due while the tier runs.
    $D exec "$DB" psql -q -v ON_ERROR_STOP=1 -U postgres -d crew -c "
        INSERT INTO projects (id, name, key, repo_source, state, next_number) VALUES ('$project', 't20app', 'TA', 'local', 'paused', 2);
        INSERT INTO git_repos (project, name) VALUES ('$project', 't20app') ON CONFLICT DO NOTHING;
        INSERT INTO tickets (project, number, title, kind, stage, origin, created_by, position, why, what, goal, analysis, branch) VALUES
          ('$project', 1, 'Add a note', 'feature', 'in_progress', 'operator', 'operator', 1, 'The project needs a note.',
           'Add NOTE.md at the root.', 'NOTE.md says what TA-1 is.', '{\"status\": \"approved\"}', 'task/TA-1-note');
        INSERT INTO settings (project, doc, updated_by) VALUES (NULL, '{\"notifications\": {\"mail_smtp\": \"127.0.0.1:9\"},
          \"conversations\": {\"idle_minutes\": 5}, \"agents\": {\"roles\": {\"assistant\": {\"model\": [\"agy/gemini-3.8-flash\"], \"effort\": \"medium\"}}}}', 't20');
        INSERT INTO cli_logins (cli, state, failures, checked_at, result, real_at)
          VALUES ('agy', 'signed_in', 0, now() + interval '1 day', 'held off by the conversations tier', now() + interval '1 day')" >/dev/null
    if [ -f "$login" ]; then
        # Stored by path in a container of its own; what it prints names the account, so it is not shown,
        # and an error is shown with any mail address taken out.
        if ! err=$($D run --rm --user "$uid:$gid" --group-add "$sock" --network "$NET" "${env[@]}" "${mounts[@]}" \
            -v /var/run/docker.sock:/var/run/docker.sock -v "$login:/run/agy-login:ro" \
            "$(lock GO_IMAGE)" /opt/crew/crew admin login set agy /run/agy-login 2>&1 >/dev/null); then
            printf '%s\n' "$err" | sed -E 's/[[:alnum:]._%+-]+@[[:alnum:].-]+/[an address]/g' >&2
            exit 1
        fi
        echo "t20: agy's login is stored"
    else
        echo "t20: no login file at $login: a dry run, whose turns wait for agy's login"
    fi
    # It joins every sandbox's bridge, so it routes nothing, as compose starts crew-server (13.6). Its
    # door answers only this network, which it finds by an alias of this network alone: its own name
    # would give the bridge it joined last, as crew-server-door on Compose's.
    $D run -d --name "$SRV" --user "$uid:$gid" --group-add "$sock" --network "$NET" --network-alias t20-server-door \
        -e CREW_DOOR_NAME=t20-server-door "${env[@]}" "${mounts[@]}" \
        --sysctl net.ipv4.ip_forward=0 -v /var/run/docker.sock:/var/run/docker.sock "$(lock GO_IMAGE)" /opt/crew/crew serve >/dev/null
    for _ in $(seq 1 90); do
        $D exec "$SRV" sh -c 'wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || curl -fs http://127.0.0.1:8080/healthz >/dev/null' && { echo "t20: up ($base with this crew-run)"; exit 0; }
        sleep 1
    done
    $D logs "$SRV" 2>&1 | tail -20
    echo "t20: the server did not answer" >&2
    exit 1
    ;;
test)
    [ -f "$login" ] || { echo "t20: no login file at $login; nothing was started" >&2; exit 2; }
    guard &
    gpid=$!
    trap 'stop $gpid; "$0" down || exit 1' EXIT
    sleep 1
    "$0" up
    # The test's values go in a file only the caller reads, not on Docker's command line.
    (umask 077 && "$0" env > "$w/run/test.env")
    sock=$(stat -c %g /var/run/docker.sock)
    rc=0
    $D run --rm --user "$uid:$gid" --group-add "$sock" --network "$NET" -v /var/run/docker.sock:/var/run/docker.sock \
        -v "$w/src:/src" -w /src -v crew-gocache:/cache -v "$w/logs:/logs:ro" --env-file "$w/run/test.env" \
        -v "$w/run/secret_key:/run/t20-key:ro" -e T20_KEY_FILE=/run/t20-key \
        -e HOME=/tmp -e GOMODCACHE=/cache/mod -e GOCACHE=/cache/build -e GOFLAGS="-mod=readonly -buildvcs=false" -e GOTOOLCHAIN=local \
        "$(lock GO_IMAGE)" go test -tags talk -count=1 -v -timeout 40m ./tools/talktier/ || rc=$?
    # The guard's log is read again after the test, whatever it said.
    if [ -s "$w/logs/login-checks.log" ]; then
        echo "t20: FAIL: Crew started a login check of agy by itself:" >&2
        cat "$w/logs/login-checks.log" >&2
        rc=1
    fi
    exit $rc
    ;;
base-commit)
    # The commit a runtime image was built from (BUG-017's test).
    base_commit "$2"
    ;;
guard)
    # The guard alone, for its probe: it runs until killed.
    guard
    ;;
env)
    echo "T20_INGRESS_TOKEN=$(cat "$w/run/ingress_token")"
    echo "T20_DB_PASSWORD=$(cat "$w/run/db_postgres")"
    echo "T20_PROJECT=$project"
    echo "T20_MARK_MAIN=$(cat "$w/run/mark_main")"
    echo "T20_MARK_BRANCH=$(cat "$w/run/mark_branch")"
    ;;
down)
    rc=0
    down || rc=1
    [ -d "$w" ] && $D run --rm -v "$w:/w" "$(lock GO_IMAGE)" rm -rf /w/data /w/src /w/run /w/fx /w/secrets >/dev/null
    [ $rc = 0 ] && echo "t20: removed"
    exit $rc
    ;;
*)
    echo "usage: stack.sh up | env | test | down | guard | base-commit IMAGE" >&2
    exit 2
    ;;
esac
