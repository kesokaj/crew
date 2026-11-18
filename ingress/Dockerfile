# Crew's ingress: Caddy with the Google sign-in, Cloudflare DNS, rate limits,
# and this module, which serves Crew's one fixed route (design 19.4).
# Build context: ingress/. Every image and plugin is pinned, so a version of
# Crew always names the same Caddy binary; the images are named in
# versions.lock, and make check compares these defaults with it.
ARG GO_IMAGE=golang:1.27.4-trixie@sha256:f18c3d73a184fb0582828510722e723b63a93f7188d11dd223d9be6f6880e65d
ARG CADDY_BUILDER_IMAGE=caddy:2.11.7-builder@sha256:c4cd20eaa0b69112fd5d4f721ffc9671687e1d5d0b155c8620cf7546dbef94c0
ARG CADDY_IMAGE=caddy:2.11.7@sha256:36c24ad91a7ff7bc4c87fe22bfa8df3c4b18da0773d73fc12ee2c52b98c0bb7e

FROM ${GO_IMAGE} AS go-sdk

FROM ${CADDY_BUILDER_IMAGE} AS builder

# Replace caddy-builder's bundled Go SDK when versions.lock moves Go ahead of the upstream builder tag.
RUN rm -rf /usr/local/go
COPY --from=go-sdk /usr/local/go /usr/local/go

# In a Sources sandbox Go's modules come through the server's module proxy, which release.sh passes
# as a build argument; with none, it is empty and Go's command takes its default (13.6).
ARG GOPROXY
COPY . /src/crew-ingress/

RUN xcaddy build \
    --with github.com/greenpau/caddy-security@v1.1.64 \
    --with github.com/caddy-dns/cloudflare@v0.2.4 \
    --with github.com/mholt/caddy-ratelimit@v0.1.0 \
    --with github.com/kesokaj/crew/ingress=/src/crew-ingress

# The Caddyfile the module generates for Crew's route must adapt with the real
# plugins: a directive one of them does not know fails the build, not a start.
# So must the ones with hosted projects: both portals, and the apps domain's wildcard (19.4).
RUN cd /src/crew-ingress \
    && CREW_CADDYFILE_OUT=/tmp/Caddyfile CREW_CADDYFILE_HOSTED_OUT=/tmp/Caddyfile.hosted \
       CREW_CADDYFILE_WILDCARD_OUT=/tmp/Caddyfile.wildcard \
       go test -count=1 -run '^(TestServerRouteCaddyfile|TestHostedProjectsUseTheAppsPortal|TestHostedProjectsShareTheWildcardSite)$' . \
    && for f in /tmp/Caddyfile /tmp/Caddyfile.hosted /tmp/Caddyfile.wildcard; do \
       GOOGLE_CLIENT_ID=build-check GOOGLE_CLIENT_SECRET=build-check \
       /usr/bin/caddy adapt --config "$f" --adapter caddyfile >/dev/null || exit 1; done

FROM ${CADDY_IMAGE}

COPY --from=builder /usr/bin/caddy /usr/bin/caddy

RUN caddy list-modules | grep -E "(security|cloudflare|rate_limit|ingress)"

ARG CREW_VERSION=0.0.0-dev
LABEL crew.version=${CREW_VERSION}

EXPOSE 80 443
CMD ["caddy", "ingress"]
