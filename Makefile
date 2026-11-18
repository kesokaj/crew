# make check: the tests, and the checks for a clean and reproducible repository (design 16.12, 22).
# It needs no microVM and no agent CLI. Go runs in the pinned Go image, so the machine needs
# only make, Python 3, and Docker.

include versions.lock

# Docker is run with sudo by anyone but root (AGENTS.md: no account is in the docker group).
DOCKER ?= $(shell [ "$$(id -u)" = 0 ] && echo docker || echo sudo docker)
# A check sandbox in Sources reaches its package sources only through the server's proxy, which
# tunnels HTTPS and nothing else, and its module proxy for Go (design 13.6, 16.2): the containers
# make check runs, and the build of its check image, get the proxy the sandbox was given. sudo docker
# would not pass it on, so each is named with its value. Where none is set, as on the machine,
# nothing is added.
# Each NAME=value is one quoted word, taken as the environment gives it ($(value)), so a space, a
# quote, a $, or a character of the shell in a value is neither expanded, split, nor run.
PROXY_VARS := $(foreach v,HTTPS_PROXY https_proxy NO_PROXY no_proxy,$(if $(value $(v)),$(v)))
shell_word = '$(subst ','\'',$(1))'
PROXY_ENV = $(foreach v,$(PROXY_VARS),-e $(call shell_word,$(v)=$(value $(v))))
PROXY_ARGS = $(foreach v,$(PROXY_VARS),--build-arg $(call shell_word,$(v)=$(value $(v))))
# In a Sources sandbox Go's modules come through the server's module proxy (13.6, since 0.61): the
# containers that run Go's command get GOPROXY too.
GO_PROXY_ENV = $(if $(value GOPROXY),-e $(call shell_word,GOPROXY=$(value GOPROXY)))
GO_PROXY_ARGS = $(if $(value GOPROXY),--build-arg $(call shell_word,GOPROXY=$(value GOPROXY)))
# The tests run as the caller's own user, not as root, so that a test of a refused write means something.
# The volume is a cache only: what is built is fixed by go.sum, and -mod=readonly fails a build that
# would change it.
# Its tag names the Go version, a hash of its Dockerfile and the images it is made from, and the user,
# so a change to any of them builds it again.
CHECK_IMAGE = crew-check:go$(GO_VERSION)-$(shell { cat tools/check-image/Dockerfile; echo $(GO_IMAGE) $(POSTGRES_IMAGE) $(GOVULNCHECK_VERSION); } | sha256sum | cut -c1-12)-u$(shell id -u)
GO_RUN = $(DOCKER) run --rm --user $$(id -u):$$(id -g) $(PROXY_ENV) $(GO_PROXY_ENV) \
	-v $(CURDIR):/src -w /src -v crew-gocache:/cache \
	-e HOME=/tmp -e GOMODCACHE=/cache/mod -e GOCACHE=/cache/build \
	-e GOFLAGS="-mod=readonly -buildvcs=false" -e GOTOOLCHAIN=local
# In a runtime sandbox whose pre-warmed /crew/cache is in place, Go and Node run directly on the
# microVM's own toolchain instead of building and starting nested containers.
CREW_CACHE_DIR ?= /crew/cache
NATIVE_SANDBOX := $(if $(CREW_SANDBOX),$(wildcard $(CREW_CACHE_DIR)/gocache))
NATIVE_GO_ENV = HOME=/tmp GOMODCACHE=$(CREW_CACHE_DIR)/gomod GOCACHE=$(CREW_CACHE_DIR)/gocache \
	GOFLAGS="-mod=readonly -buildvcs=false" GOTOOLCHAIN=local

.PHONY: check-compose check check-plan check-versions check-clean check-tools check-image test-go test-go-pkg check-go test-web test-firewall install-test fmt

check: check-plan check-versions check-clean check-tools check-compose test-go test-web test-firewall
	@echo "make check: ok"

check-plan:
	python3 tools/plan-copies.py --check

check-versions:
	python3 tools/check-versions.py

check-clean:
	python3 tools/check-clean.py

check-tools:
	python3 -I spike/scrub_test.py -q
	python3 -I tools/tools_test.py -q

# The Compose file reads, with what crew-ctl and an install file give it.
# The Hosting tier (design 22, plan M19 D4): real microVMs through a throwaway stack of this checkout,
# named t19-…, removed again; on demand on a machine with Kata and machine setup 6, never in check.
test-hosting:
	bash tools/hostingtier/stack.sh test

# The real tier of conversations (design 11.8, 11.9, plan M20 D24, D39): at most six real agy turns on
# the operator's subscription, through a throwaway stack named t20-…, with the agy login the operator
# put at .agents/scratch/t20/secrets/agy-login; Crew starts no agy of its own there, and all of it,
# the login file included, is removed again. Only with the operator's yes, never in check.
test-talk:
	bash tools/talktier/stack.sh test

check-compose:
	$(DOCKER) compose -f deploy/compose.yaml --env-file versions.lock --env-file .env.example \
		--env-file tools/check-compose.vars config -q

# The check image's base images are pulled first, by the daemon, which has the proxy of a Sources
# sandbox: a build resolves a FROM itself and does not use it.
check-image:
	@$(DOCKER) image inspect $(CHECK_IMAGE) >/dev/null 2>&1 || { \
		for i in $(GO_IMAGE) $(POSTGRES_IMAGE); do $(DOCKER) image inspect $$i >/dev/null 2>&1 || $(DOCKER) pull -q $$i >/dev/null || exit 1; done; \
		$(DOCKER) build -q -t $(CHECK_IMAGE) $(PROXY_ARGS) $(GO_PROXY_ARGS) --build-arg GO_IMAGE=$(GO_IMAGE) --build-arg POSTGRES_IMAGE=$(POSTGRES_IMAGE) \
			--build-arg GOVULNCHECK_VERSION=$(GOVULNCHECK_VERSION) --build-arg UID=$$(id -u) --build-arg GID=$$(id -g) tools/check-image; }
	@$(DOCKER) run --rm -v crew-gocache:/cache $(CHECK_IMAGE) sh -c 'mkdir -p /cache/mod /cache/build && chown -R '$$(id -u):$$(id -g)' /cache'

# Both Go modules: the server's, and the ingress's. The database tests run against a real
# PostgreSQL that tools/check-db.sh starts on a network of its own, and removes again however the
# tests end. Each package has 20 minutes, not Go's 10: internal/httpapi takes about 9 under -race on
# a busy host and more in a check sandbox, and 20 stays below limits.check_timeout_minutes (30), so a
# hang still prints its goroutines and names the test.
test-go: $(if $(NATIVE_SANDBOX),,check-image)
	DOCKER="$(DOCKER)" tools/check-db.sh start
ifeq ($(NATIVE_SANDBOX),)
	$(GO_RUN) --network crew-check -e CREW_TEST_DB_HOST=crew-check-db -e CREW_DB_SECRETS_DIR=/src/.check/secrets \
		$(CHECK_IMAGE) sh -c 'out="$$(gofmt -l */ 2>&1)"; test -z "$$out" || { echo "$$out"; echo "gofmt: the files above are not formatted, or could not be read"; exit 1; }; \
		go vet ./... && go test -race -p 4 -count=1 -timeout 20m ./... && \
		cd ingress && go vet ./... && go test -race -count=1 -timeout 20m ./...'; \
	status=$$?; DOCKER="$(DOCKER)" tools/check-db.sh stop; exit $$status
else
	db_host=$$($(DOCKER) inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' crew-check-db); \
	$(NATIVE_GO_ENV) CREW_TEST_DB_HOST="$$db_host" CREW_DB_SECRETS_DIR="$(CURDIR)/.check/secrets" \
		sh -c 'out="$$(gofmt -l */ 2>&1)"; test -z "$$out" || { echo "$$out"; echo "gofmt: the files above are not formatted, or could not be read"; exit 1; }; \
		go vet ./... && go test -race -p 4 -count=1 -timeout 20m ./... && \
		cd ingress && go vet ./... && go test -race -count=1 -timeout 20m ./...'; \
	status=$$?; DOCKER="$(DOCKER)" tools/check-db.sh stop; exit $$status
endif

# One package pattern under -race against the same throwaway PostgreSQL as test-go, for timing a
# package check or running a changed package alone (PKG is required; GO_TEST_TIMEOUT defaults to 20m).
GO_TEST_TIMEOUT ?= 20m
test-go-pkg: $(if $(strip $(PKG)),$(if $(NATIVE_SANDBOX),,check-image))
	@test -n "$(strip $(PKG))" || { echo "test-go-pkg: set PKG (e.g. make test-go-pkg PKG=./internal/httpapi/...)"; exit 1; }
	DOCKER="$(DOCKER)" tools/check-db.sh start
ifeq ($(NATIVE_SANDBOX),)
	$(GO_RUN) --network crew-check -e CREW_TEST_DB_HOST=crew-check-db -e CREW_DB_SECRETS_DIR=/src/.check/secrets \
		$(CHECK_IMAGE) go test -race -count=1 -timeout $(GO_TEST_TIMEOUT) $(PKG); \
	status=$$?; DOCKER="$(DOCKER)" tools/check-db.sh stop; exit $$status
else
	db_host=$$($(DOCKER) inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' crew-check-db); \
	$(NATIVE_GO_ENV) CREW_TEST_DB_HOST="$$db_host" CREW_DB_SECRETS_DIR="$(CURDIR)/.check/secrets" \
		go test -race -count=1 -timeout $(GO_TEST_TIMEOUT) $(PKG); \
	status=$$?; DOCKER="$(DOCKER)" tools/check-db.sh stop; exit $$status
endif

# Targeted package and test check (PKG defaults to ./..., RUN is an optional -run regex). In a sandbox
# checkout it keeps crew-check-db warm so repeated agent or cmd: checks finish in seconds.
RUN ?=
check-go: $(if $(NATIVE_SANDBOX),,check-image)
	DOCKER="$(DOCKER)" tools/check-db.sh start
ifeq ($(NATIVE_SANDBOX),)
	$(GO_RUN) --network crew-check -e CREW_TEST_DB_HOST=crew-check-db -e CREW_DB_SECRETS_DIR=/src/.check/secrets \
		$(CHECK_IMAGE) go test -race -count=1 -timeout $(GO_TEST_TIMEOUT) $(if $(strip $(RUN)),-run $(call shell_word,$(RUN))) $(if $(strip $(PKG)),$(PKG),./...); \
	status=$$?; DOCKER="$(DOCKER)" tools/check-db.sh stop; exit $$status
else
	db_host=$$($(DOCKER) inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' crew-check-db); \
	$(NATIVE_GO_ENV) CREW_TEST_DB_HOST="$$db_host" CREW_DB_SECRETS_DIR="$(CURDIR)/.check/secrets" \
		go test -race -count=1 -timeout $(GO_TEST_TIMEOUT) $(if $(strip $(RUN)),-run $(call shell_word,$(RUN))) $(if $(strip $(PKG)),$(PKG),./...); \
	status=$$?; if [ ! -e .git ]; then DOCKER="$(DOCKER)" tools/check-db.sh stop; fi; exit $$status
endif

# The UI: its build, which checks its types and makes the route tree they read, and its unit tests,
# in the pinned Node image as the caller's own user. npm ci installs exactly the lock file; its cache
# is a volume, as the Go cache is. The build's output is shown only when it fails.
test-web:
ifeq ($(NATIVE_SANDBOX),)
	@$(DOCKER) run --rm -v crew-npmcache:/cache $(WEB_NODE_IMAGE) chown $$(id -u):$$(id -g) /cache
	$(DOCKER) run --rm --user $$(id -u):$$(id -g) $(PROXY_ENV) -e HOME=/tmp -e npm_config_cache=/cache -e npm_config_update_notifier=false \
		-v $(CURDIR)/web:/web -w /web -v crew-npmcache:/cache $(WEB_NODE_IMAGE) \
		sh -c 'npm ci --no-audit --no-fund --loglevel=error && { npm run build >/tmp/build.log 2>&1 || { cat /tmp/build.log; exit 1; }; } && npx vitest run'
else
	cd web && HOME=/tmp npm_config_cache=$(CREW_CACHE_DIR)/npm npm_config_update_notifier=false \
		sh -c 'if [ ! -d node_modules ]; then npm ci --prefer-offline --no-audit --no-fund --loglevel=error; fi && { npm run build >/tmp/build.log 2>&1 || { cat /tmp/build.log; exit 1; }; } && npx vitest run'
endif

# The real firewall script against scratch chains, in a user and network namespace of its own on the
# machine, with no root and no change to the machine's rules. Docker gives a container no namespace
# in which /sys can be mounted again, so the Go test that wraps this script skips there, and this
# target runs it. Where the machine has no user namespaces, it says so and fails. In a check sandbox
# (CREW_SANDBOX), Docker's masked paths keep a user namespace from mounting /sys ("Mount too
# revealing"), so the sandbox's own root runs it, in a network and mount namespace of its own: the
# sandbox is a microVM, whose root is not the machine's.
test-firewall:
	$(if $(CREW_SANDBOX),,@unshare -Ur true 2>/dev/null || { echo "test-firewall: this machine refuses $$(id -un) a user namespace; the firewall test runs in one (on Ubuntu: kernel.apparmor_restrict_unprivileged_userns=1)"; exit 1; })
	$(if $(CREW_SANDBOX),sudo -n unshare -nm,unshare -Urnm) bash deploy/firewall/test.sh

fmt: check-image
	$(GO_RUN) $(CHECK_IMAGE) gofmt -w */

# The install test (design 16.11, 22): a fresh virtual machine on this machine, in a container that
# holds QEMU and is given /dev/kvm, on a sandbox bridge (sdb-) so that it reaches the internet and
# not this machine. Its subnet is none a machine Crew sets up uses (Docker's pools there are 172.16.0.0/12,
# QEMU's own network 10.0.2.0/24): the virtual machine reaches the phase second's registry by its address,
# which a bridge of its own Docker would otherwise take. Not part of make check: it runs for about an hour and builds every image.
#   make install-test DISTRO=debian|ubuntu PHASE=cycle|kata4|halfway|e2e|restart|second
# PHASE=second first builds two releases here and serves them from a throwaway registry on the install
# test's network (tools/install-test/second.sh), and removes it all after the run.
DISTRO ?= debian
PHASE ?= cycle
INSTALL_TEST_IMAGE = crew-install-test:$(shell { cat tools/install-test/Dockerfile tools/install-test/vm.sh; echo $(SERVER_BASE_IMAGE); } | sha256sum | cut -c1-12)
install-test:
	@test -e /dev/kvm || { echo "install-test: needs /dev/kvm"; exit 1; }
	$(DOCKER) build -q -t $(INSTALL_TEST_IMAGE) --build-arg SERVER_BASE_IMAGE=$(SERVER_BASE_IMAGE) tools/install-test
	mkdir -p .install-test
	git bundle create .install-test/crew.bundle HEAD
	$(DOCKER) network inspect crew-install-test >/dev/null 2>&1 || \
		$(DOCKER) network create -o com.docker.network.bridge.name=sdb-installtest --subnet 10.199.7.0/24 crew-install-test
	if [ "$(PHASE)" = second ]; then IMG=$(INSTALL_TEST_IMAGE) DOCKER="$(DOCKER)" tools/install-test/second.sh up \
		|| { DOCKER="$(DOCKER)" tools/install-test/second.sh down; $(DOCKER) network rm crew-install-test >/dev/null 2>&1; exit 1; }; fi
	$(DOCKER) run --rm --device /dev/kvm --network crew-install-test --dns 8.8.8.8 \
		--env-file versions.lock \
		-v crew-install-test-cache:/cache -v $(CURDIR)/.install-test:/src:ro \
		-v $(CURDIR)/.install-test:/out \
		--name crew-install-test-$(DISTRO)-$(PHASE) $(INSTALL_TEST_IMAGE) $(DISTRO) $(PHASE); \
	status=$$?; [ "$(PHASE)" != second ] || DOCKER="$(DOCKER)" tools/install-test/second.sh down; \
	$(DOCKER) network rm crew-install-test >/dev/null 2>&1; exit $$status
