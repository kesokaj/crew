# crew-runtime: the one runtime image every sandbox starts from (design 12.8, 13.4). AMD64 only.
# It holds agy, the harness built for it, crew-run, and the tools. Build context: the
# repository's root. Every pinned value is the one versions.lock names; make check compares them.
# The build takes nothing from the machine and needs no token: the harness is in the checkout.
# It is given one more context, agy: a folder with agy's tarball, which Crew's server, or crew-ctl on
# the machine, downloaded from the vendor's address in versions.lock and checked (12.8, since 0.57).
ARG GO_IMAGE=golang:1.27.4-trixie@sha256:f18c3d73a184fb0582828510722e723b63a93f7188d11dd223d9be6f6880e65d
ARG RUNTIME_BASE_IMAGE=debian:trixie@sha256:9c6e381421d0f48d1b8133e573f33c185ee16cbf505eda9c4a8a4791b25ea8a6
ARG POSTGRES_IMAGE=postgres:18.6-trixie@sha256:e3d24f6906d0760bf4da0dae872415391ededefad23fd471c47dc53b0ddf3c42

FROM ${POSTGRES_IMAGE} AS postgres

# crew-run, the harness written out for each CLI into a home skeleton (12.1), and the pre-warmed
# caches (/crew/cache/gomod, /crew/cache/gocache, /crew/cache/postgres.tar) and govulncheck for native
# sandbox checks.
FROM ${GO_IMAGE} AS build
# In a Sources sandbox Go's modules come through the server's module proxy, which release.sh passes
# as a build argument; with none, it is empty and Go's command takes its default (13.6).
ARG GOPROXY
ARG GOVULNCHECK_VERSION=v1.9.0
ARG POSTGRES_IMAGE=postgres:18.6-trixie@sha256:e3d24f6906d0760bf4da0dae872415391ededefad23fd471c47dc53b0ddf3c42
ENV GOMODCACHE=/out/cache/gomod GOCACHE=/out/cache/gocache
WORKDIR /src
COPY go.mod go.sum ./
COPY ingress/go.mod ingress/go.sum ./ingress/
RUN go mod download && (cd ingress && go mod download) \
    && GOBIN=/out go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" \
    && /out/govulncheck -version | grep -q "govulncheck@${GOVULNCHECK_VERSION}" \
    && go build -race std \
       github.com/coder/websocket \
       github.com/docker/docker/client \
       github.com/golang-jwt/jwt/v5 \
       github.com/jackc/pgx/v5 \
       github.com/jackc/pgx/v5/pgxpool \
       github.com/opencontainers/image-spec/specs-go/v1 \
       go.yaml.in/yaml/v3 \
       golang.org/x/sys/unix \
    && (cd ingress && go build -race github.com/caddyserver/caddy/v2 github.com/caddyserver/caddy/v2/cmd) \
    && chmod -R a+rX /out/cache
COPY --from=postgres / /pgroot
RUN python3 - <<'PY'
import hashlib, json, os, subprocess, tarfile
tag = os.environ["POSTGRES_IMAGE"].split("@")[0]
pg_major = sorted(os.listdir("/pgroot/usr/lib/postgresql"))[-1]
os.makedirs("/tmp/pgimg", exist_ok=True)
layer_path = "/tmp/pgimg/layer.tar"
subprocess.run(["tar", "-C", "/pgroot", "-cf", layer_path, "."], check=True)
h = hashlib.sha256()
with open(layer_path, "rb") as f:
    for chunk in iter(lambda: f.read(1 << 20), b""):
        h.update(chunk)
diff_id = "sha256:" + h.hexdigest()
cfg = {
    "architecture": "amd64",
    "os": "linux",
    "config": {
        "ExposedPorts": {"5432/tcp": {}},
        "Env": [
            f"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/lib/postgresql/{pg_major}/bin",
            "LANG=en_US.utf8",
            f"PG_MAJOR={pg_major}",
            f"PGDATA=/var/lib/postgresql/{pg_major}/docker",
        ],
        "Entrypoint": ["docker-entrypoint.sh"],
        "Cmd": ["postgres"],
        "Volumes": {"/var/lib/postgresql": {}},
        "StopSignal": "SIGINT",
    },
    "rootfs": {"type": "layers", "diff_ids": [diff_id]},
    "history": [{"created": "1970-01-01T00:00:00Z"}],
}
cfg_bytes = json.dumps(cfg, sort_keys=True).encode()
cfg_name = hashlib.sha256(cfg_bytes).hexdigest() + ".json"
with open("/tmp/pgimg/" + cfg_name, "wb") as f:
    f.write(cfg_bytes)
manifest = [{"Config": cfg_name, "RepoTags": [tag], "Layers": ["layer.tar"]}]
with open("/tmp/pgimg/manifest.json", "w") as f:
    json.dump(manifest, f)
os.makedirs("/out/cache", exist_ok=True)
with tarfile.open("/out/cache/postgres.tar", "w") as t:
    for name in (cfg_name, "manifest.json", "layer.tar"):
        t.add("/tmp/pgimg/" + name, arcname=name)
os.chmod("/out/cache/postgres.tar", 0o644)
PY
RUN rm -rf /pgroot /tmp/pgimg
COPY cmd/ cmd/
COPY internal/ internal/
COPY harness/ harness/
ARG CREW_VERSION=0.0.0-dev
RUN CGO_ENABLED=0 GOFLAGS=-mod=readonly go build -trimpath \
      -ldflags "-s -w -X github.com/kesokaj/crew/internal/version.Version=${CREW_VERSION}" \
      -o /out/crew-run ./cmd/crew-run \
    && CGO_ENABLED=0 GOFLAGS=-mod=readonly go build -trimpath -o /out/crew-harnessbuild ./cmd/crew-harnessbuild \
    && /out/crew-harnessbuild -src harness -out /out/home-skel \
    && chmod -R a+rX /out/cache

# agy's tarball, from the build context agy, which replaces this empty stage. A build given no context
# fails at the copy below, and pulls no image named agy.
FROM scratch AS agy

FROM ${RUNTIME_BASE_IMAGE}

ENV DEBIAN_FRONTEND=noninteractive
ENV TERM=xterm-256color

# A release's build reads its package sources through a proxy that takes only HTTPS (16.2 step 4):
# Debian's sources move to HTTPS, with the CA bundle of the pinned Go image, before apt runs.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources \
    && ! grep -q '^URIs:.*http://' /etc/apt/sources.list.d/debian.sources \
    && echo 'Acquire::https::CAInfo "/etc/ssl/certs/ca-certificates.crt";' > /etc/apt/apt.conf.d/99crew-https

# Base tools + everything the entrypoint needs (e2fsprogs for the per-project
# ext4 docker image, tini as the PID-1 reaper). The keyring packages
# (dbus/gnome-keyring/libsecret-tools) were dropped with the broker keyring tier
# — every credential is env/file/git-helper now.
RUN apt-get update && apt-get install --no-install-recommends -y \
    ca-certificates curl wget gnupg lsb-release locales sudo tini procps psmisc \
    git jq ripgrep fd-find bat tmux htop btop less vim nano unzip zip tree \
    openssh-client openssh-server libnss-wrapper zstd rsync make build-essential python3 python3-venv python3-pip \
    python-is-python3 e2fsprogs \
    iproute2 iputils-ping dnsutils netcat-openbsd \
    bash-completion lsof mtr-tiny traceroute net-tools gettext-base openssl \
    shellcheck libgssapi-krb5-2 libldap2 liblz4-1 libzstd1 libreadline8t64 libxxhash0 \
    && mkdir -p -m 0755 /run/sshd \
    && rm -rf /var/lib/apt/lists/* \
    && sed -i 's/# en_US.UTF-8/en_US.UTF-8/' /etc/locale.gen && locale-gen

COPY --from=postgres /usr/lib/x86_64-linux-gnu/libpq.so.5* /usr/lib/x86_64-linux-gnu/
COPY --from=postgres /usr/lib/postgresql/18/bin/pg_dump /usr/lib/postgresql/18/bin/psql /usr/local/bin/
RUN ! ldd /usr/local/bin/pg_dump /usr/local/bin/psql | grep 'not found' && pg_dump --version

ENV LANG=en_US.UTF-8 LANGUAGE=en_US:en LC_ALL=en_US.UTF-8

# Docker for the sandbox's own daemon, gh, gcloud, and kubectl, from their package sources at the
# versions versions.lock names. Docker's and Google's sources keep their older versions; GitHub's
# keeps only its newest, so the gh pin moves when gh releases. Chromium and Python come from
# Debian's archive, which keeps only the current one: /crew/versions.json records what came.
ARG RUNTIME_DOCKER_CE_VERSION=5:29.9.0-1~debian.13~trixie
ARG RUNTIME_CONTAINERD_VERSION=2.4.1-2~debian.13~trixie
ARG RUNTIME_BUILDX_VERSION=0.38.0-1~debian.13~trixie
ARG RUNTIME_COMPOSE_VERSION=5.6.0-1~debian.13~trixie
ARG RUNTIME_GH_VERSION=2.103.3
ARG RUNTIME_GCLOUD_VERSION=588.0.0-0
ARG RUNTIME_KUBECTL_VERSION=1:588.0.0-0
RUN install -m 0755 -d /etc/apt/keyrings && \
    curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc && \
    echo "deb [signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable" > /etc/apt/sources.list.d/docker.list && \
    curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /etc/apt/keyrings/githubcli.gpg && \
    echo "deb [signed-by=/etc/apt/keyrings/githubcli.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list && \
    curl -fsSL https://packages.cloud.google.com/apt/doc/apt-key.gpg -o /etc/apt/keyrings/cloud.google.asc && \
    echo "deb [signed-by=/etc/apt/keyrings/cloud.google.asc] https://packages.cloud.google.com/apt cloud-sdk main" > /etc/apt/sources.list.d/google-cloud-sdk.list && \
    apt-get update && apt-get install --no-install-recommends -y \
    "docker-ce=${RUNTIME_DOCKER_CE_VERSION}" "docker-ce-cli=${RUNTIME_DOCKER_CE_VERSION}" \
    "containerd.io=${RUNTIME_CONTAINERD_VERSION}" "docker-buildx-plugin=${RUNTIME_BUILDX_VERSION}" \
    "docker-compose-plugin=${RUNTIME_COMPOSE_VERSION}" "gh=${RUNTIME_GH_VERSION}" \
    "google-cloud-cli=${RUNTIME_GCLOUD_VERSION}" "google-cloud-cli-gke-gcloud-auth-plugin=${RUNTIME_GCLOUD_VERSION}" \
    "kubectl=${RUNTIME_KUBECTL_VERSION}" \
    && rm -rf /var/lib/apt/lists/*

# What is pinned, and how: OpenTofu, yq, hadolint, the Go toolchain, Node, and agy are fetched by
# version and checked against the publisher's own checksum, never a sum computed from what the
# download returned; chrome-devtools-mcp is pinned further down. A tool that moved upstream fails
# the build. agy, the only agent CLI since design 0.42, is the tarball its vendor's manifest names,
# with that manifest's sha512 (12.8, since 0.56), which the build is given rather than fetches (since
# 0.57); the server's daily look reads the manifest for a newer one. A version and its checksum move together, in versions.lock and here: `make check`
# holds the two in step.
ARG TOFU_VERSION=1.13.1
ARG TOFU_SHA256=8ccbc6f8ee21d2827715f3c6e08a9b3e0209b1e62057c05067ef117e047c1a80
ARG YQ_VERSION=4.54.1
ARG YQ_SHA256=8e34fc298390875de416e6a4afcb8cabeceb25d9aa8506c1a2f9353cf702ea5f
ARG HADOLINT_VERSION=2.15.1
ARG HADOLINT_SHA256=c7187db94eeeeca956519a6af171adc31453941a1e777961f6e680f697c8c507
# OpenTofu + yq + hadolint
# hadolint is the Dockerfile checker behind the editor's lint gutter
# (internal/httpapi/check.go); shellcheck, its shell counterpart, comes from
# apt above. Helm was REMOVED — it was the
# worst of the unpinned fetches (`curl … get-helm-3 | bash`) and is not wanted
# in the image; a project that needs it can install it itself.
RUN set -eux; \
    curl -fsSL -o /tmp/tofu.zip "https://github.com/opentofu/opentofu/releases/download/v${TOFU_VERSION}/tofu_${TOFU_VERSION}_linux_amd64.zip"; \
    echo "${TOFU_SHA256}  /tmp/tofu.zip" | sha256sum -c -; \
    unzip -q -o -d /tmp/tofu /tmp/tofu.zip; \
    install -m 0755 /tmp/tofu/tofu /usr/local/bin/tofu; \
    curl -fsSL -o /usr/local/bin/yq "https://github.com/mikefarah/yq/releases/download/v${YQ_VERSION}/yq_linux_amd64"; \
    echo "${YQ_SHA256}  /usr/local/bin/yq" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/yq; \
    curl -fsSL -o /usr/local/bin/hadolint "https://github.com/hadolint/hadolint/releases/download/v${HADOLINT_VERSION}/hadolint-Linux-x86_64"; \
    echo "${HADOLINT_SHA256}  /usr/local/bin/hadolint" | sha256sum -c -; \
    chmod 0755 /usr/local/bin/hadolint; \
    rm -rf /tmp/tofu.zip /tmp/tofu

# Go + Node, pinned (sums from go.dev/dl and nodejs.org/dist/vX/SHASUMS256.txt, which the server's
# daily look reads for a newer version, 12.8). A changed upstream tarball fails the build loudly.
ARG RUNTIME_GO_VERSION=1.27.4
ARG RUNTIME_GO_SHA256=7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840
ARG RUNTIME_NODE_VERSION=22.23.4
ARG RUNTIME_NODE_SHA256=1805bcd35e6815559bd2eb97db438d3fdb0eb3a5e6a977b7c1badca76337e9ce
RUN set -eux; \
    curl -fsSL -o /tmp/go.tgz "https://go.dev/dl/go${RUNTIME_GO_VERSION}.linux-amd64.tar.gz"; \
    echo "${RUNTIME_GO_SHA256}  /tmp/go.tgz" | sha256sum -c -; \
    tar -C /usr/local -xzf /tmp/go.tgz; rm -f /tmp/go.tgz; \
    ln -s /usr/local/go/bin/go /usr/local/bin/go && ln -s /usr/local/go/bin/gofmt /usr/local/bin/gofmt; \
    curl -fsSL -o /tmp/node.txz "https://nodejs.org/dist/v${RUNTIME_NODE_VERSION}/node-v${RUNTIME_NODE_VERSION}-linux-x64.tar.xz"; \
    echo "${RUNTIME_NODE_SHA256}  /tmp/node.txz" | sha256sum -c -; \
    tar -C /usr/local --strip-components=1 -xJf /tmp/node.txz --exclude='*/CHANGELOG.md' --exclude='*/README.md' --exclude='*/LICENSE'; \
    rm -f /tmp/node.txz; node --version; npm --version

# The agent (12.8): agy at its pin, the tarball the vendor's manifest names, given to the build as its
# context agy and checked here again by its sha512. It does not update itself in a sandbox: the
# variable below says so, and /opt/agy stays root's. The build runs it only as nobody, to read its
# version (safety rule 11: agy never runs as root).
ARG AGY_VERSION=1.5.6
ARG AGY_SHA512=0c67824908caf9cc3156699802b8097f2fd02c4608ae9293c9aa7f17e1bcd342c4b9c2e158388253a37804ef6bd9c6b1d25cfaca3c07bbb7587378fc91839bb4
ENV AGY_CLI_DISABLE_AUTO_UPDATE=true
COPY --from=agy cli_linux_x64.tar.gz /tmp/agy.tgz
RUN set -eux; \
    echo "${AGY_SHA512}  /tmp/agy.tgz" | sha512sum -c -; \
    tar -xzf /tmp/agy.tgz -C /tmp antigravity; \
    install -d -m 0755 /opt/agy; \
    install -m 0755 /tmp/antigravity /opt/agy/agy; \
    rm -f /tmp/agy.tgz /tmp/antigravity; \
    ln -sfn /opt/agy/agy /usr/local/bin/agy; \
    test "$(setpriv --reuid=65534 --regid=65534 --clear-groups --no-new-privs env HOME=/tmp agy --version)" = "${AGY_VERSION}"

# Browser view: one headless Chromium per project, driven by the
# operator through the SPA's screencast and by the agents through
# chrome-devtools-mcp (pinned by its version and its integrity; it attaches to the RUNNING
# Chromium over CDP and downloads no browser of its own). Debian's own
# chromium package: its version moves with the archive's security updates,
# exactly like docker-ce above, so it is pinned by the archive, not by a
# sha256. fonts-liberation gives pages metric-compatible Arial/Times/Courier
# instead of boxes. socat publishes DevTools on the guest's bridge address:
# new-headless Chromium ignores --remote-debugging-address and binds loopback
# only (verified 2026-09-11), and the crew server reaches the guest over the
# bridge, never through loopback. Sits BEFORE the base-package snapshot below so apt
# persistence never records chromium as a user install. Launcher:
# configs/crew-browser → /crew/bin (installed with the others below).
# --ignore-scripts: no transitive package runs a lifecycle script as root at
# build time (the package needs none — proven by
# the --help run). The resolved GLOBAL tree is
# recorded at /crew/npm-global.tree.json so two builds of one version can
# be diffed: only the top-level versions are pinned, the rest resolves by
# semver. The package's own tarball is checked against the integrity
# versions.lock names, so the build installs the bytes the pin approved,
# whatever registry answers (security review of the release of 0.3.4).
ARG CHROME_DEVTOOLS_MCP_VERSION=1.12.0
ARG CHROME_DEVTOOLS_MCP_INTEGRITY=sha512-lAGpH0zXT1ZdaPefP1KK/MvxwihOlu7VGQ7p4wbhD9OhN17vUC4KWk/ImUYR9DE4s+7r4r0ySV4xr2OkTa7ZMA==
RUN mkdir -p /crew && apt-get update && apt-get install --no-install-recommends -y chromium fonts-liberation socat \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL -o /tmp/cdm.tgz "https://registry.npmjs.org/chrome-devtools-mcp/-/chrome-devtools-mcp-${CHROME_DEVTOOLS_MCP_VERSION}.tgz" \
    && got="sha512-$(openssl dgst -sha512 -binary /tmp/cdm.tgz | base64 -w0)" \
    && { [ "$got" = "${CHROME_DEVTOOLS_MCP_INTEGRITY}" ] || { echo "chrome-devtools-mcp: $got is not the pinned ${CHROME_DEVTOOLS_MCP_INTEGRITY}" >&2; exit 1; }; } \
    && npm install -g --ignore-scripts /tmp/cdm.tgz && rm -f /tmp/cdm.tgz \
    && npm ls -g --all --json > /crew/npm-global.tree.json \
    && chromium --version && chrome-devtools-mcp --help >/dev/null

# In-guest defaults: DNS template (Kata has no embedded Docker DNS) and a
# package snapshot for drift debugging.
RUN printf "nameserver 8.8.8.8\nnameserver 1.1.1.1\n" > /etc/resolv.conf.crew && \
    dpkg-query -W -f='${Package}\n' | sort > /etc/crew-base-packages

# The user in every sandbox is agent, uid 1000, home /home/agent (16.11). It may use sudo and
# its own Docker; it is not root (13.5).
RUN groupadd -g 1000 agent && useradd -u 1000 -g 1000 -m -d /home/agent -s /bin/bash -G docker agent \
    && echo "agent ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/agent && chmod 440 /etc/sudoers.d/agent

COPY image/entrypoint.sh /crew/entrypoint.sh
COPY image/configs/ /crew/configs/
# Default shell dotfiles; the entrypoint seeds each home write-once.
COPY image/skel/ /crew/skel/
COPY --from=build /out/crew-run /usr/local/bin/crew-run
COPY --from=build /out/govulncheck /usr/local/bin/govulncheck
COPY --from=build --chown=1000:1000 /out/cache/ /crew/cache/
COPY --from=build --chown=1000:1000 /out/home-skel/ /crew/home-skel/
COPY web/package.json web/package-lock.json /tmp/npm-warm/web/
COPY tools/e2e/browser/package.json tools/e2e/browser/package-lock.json /tmp/npm-warm/browser/
RUN set -eux; \
    mkdir -p /crew/cache/npm; \
    (cd /tmp/npm-warm/web && HOME=/tmp npm_config_cache=/crew/cache/npm npm ci --ignore-scripts --no-audit --no-fund --loglevel=error); \
    (cd /tmp/npm-warm/browser && HOME=/tmp npm_config_cache=/crew/cache/npm npm ci --ignore-scripts --no-audit --no-fund --loglevel=error); \
    rm -rf /tmp/npm-warm; \
    chown -R 1000:1000 /crew/cache && chmod -R 0777 /crew/cache/gomod /crew/cache/gocache /crew/cache/npm
ENV CREW_SANDBOX=1
ARG CREW_VERSION=0.0.0-dev
RUN echo "${CREW_VERSION}" > /crew/VERSION && chmod +x /crew/entrypoint.sh && \
    install -m 755 /crew/configs/crew-setup /usr/local/bin/crew-setup && \
    install -m 755 /crew/configs/crew-git-credential /usr/local/bin/crew-git-credential && \
    install -D -m 755 /crew/configs/crew-browser /crew/bin/crew-browser

# Credential broker git helper: a SYSTEM gitconfig makes every
# project use the broker for HTTPS credentials — no per-home setup, no stored
# token. useHttpPath=false so one credential covers a whole host.
RUN git config --system credential.helper /usr/local/bin/crew-git-credential && \
    git config --system credential.useHttpPath false

# What the image got, for the release to record: agy's version, and the tools from package sources,
# which are what the source gave on the build day (12.8, 16.12).
RUN python3 - <<'PY'
import json, subprocess
def v(*cmd):
    try:
        return subprocess.run(cmd, capture_output=True, text=True, timeout=60).stdout.strip().splitlines()[0]
    except Exception as e:
        return "unknown: %s" % e
out = {
    "crew": open("/crew/VERSION").read().strip(),
    "agy": v("setpriv", "--reuid=65534", "--regid=65534", "--clear-groups", "--no-new-privs", "env", "HOME=/tmp", "agy", "--version"),
    "crew-run": v("crew-run", "version"), "go": v("go", "version"), "node": v("node", "--version"),
    "docker": v("docker", "--version"), "gh": v("gh", "--version"), "gcloud": v("gcloud", "--version"),
    "kubectl": v("kubectl", "version", "--client"), "chromium": v("chromium", "--version"),
    "python": v("python3", "--version"), "tofu": v("tofu", "version"),
}
json.dump(out, open("/crew/versions.json", "w"), indent=2, sort_keys=True)
print(json.dumps(out, indent=2, sort_keys=True))
PY
ARG CREW_VERSION=0.0.0-dev
LABEL crew.version="${CREW_VERSION}"
ENTRYPOINT ["/usr/bin/tini", "-s", "--", "/crew/entrypoint.sh"]
