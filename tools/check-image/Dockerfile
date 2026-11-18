# The image make check runs the Go tests in. It is never shipped: it is the pinned Go image with jq,
# which the test of the credential snippet drives for real, zstd, which the test of the Kata functions
# unpacks a release with (deploy/lib/kata-config-test.sh), openssh-server and libnss-wrapper, which
# internal/github's SSH test runs a local sshd with, the PostgreSQL tools of the pinned database
# image, which the backup tests run, and a user with the caller's own ids, because the copied
# configuration reads the name of the user it runs as.
ARG GO_IMAGE
ARG POSTGRES_IMAGE
FROM ${POSTGRES_IMAGE} AS postgres

FROM ${GO_IMAGE}
ARG UID=1000
ARG GID=1000
ARG GOVULNCHECK_VERSION=v1.9.0
ARG GOPROXY
# A check sandbox in Sources reaches Debian's packages only through a proxy that takes HTTPS (16.2):
# the sources move to HTTPS before apt runs, as the runtime image's do.
RUN sed -i 's|http://deb.debian.org|https://deb.debian.org|g' /etc/apt/sources.list.d/debian.sources \
    && ! grep -q '^URIs:.*http://' /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install --no-install-recommends -y jq zstd openssh-server libnss-wrapper \
       libgssapi-krb5-2 libldap2 liblz4-1 libzstd1 libreadline8t64 libxxhash0 \
    && mkdir -p -m 0755 /run/sshd \
    && rm -rf /var/lib/apt/lists/* \
    && GOBIN=/usr/local/bin go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" \
    && govulncheck -version | grep -q "govulncheck@${GOVULNCHECK_VERSION}" \
    && rm -rf /go/pkg /root/.cache \
    && (getent group "${GID}" >/dev/null || groupadd -g "${GID}" builder) \
    && useradd -u "${UID}" -g "${GID}" -M -d /tmp builder
COPY --from=postgres /usr/lib/x86_64-linux-gnu/libpq.so.5* /usr/lib/x86_64-linux-gnu/
COPY --from=postgres /usr/lib/postgresql/18/bin/pg_dump /usr/lib/postgresql/18/bin/psql /usr/local/bin/
RUN ! ldd /usr/local/bin/pg_dump /usr/local/bin/psql | grep 'not found' && pg_dump --version
