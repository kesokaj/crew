---
title: Setup
covers: [Makefile, versions.lock, deploy/**, tools/**, .env.example]
---
# Setup

Crew builds and runs entirely in Docker containers; the host machine needs no local Go or Node installation. All version pins and image digests live in `versions.lock`.

## Prerequisites

- Linux host (Debian 13 or Ubuntu 24.04+) with KVM (`/dev/kvm`), `make`, `python3`, `git`, and Docker with the `mvm` (Kata Containers + Cloud Hypervisor) runtime configured via `deploy/host-setup.sh`.
- For unprivileged firewall tests on Ubuntu: `sysctl kernel.apparmor_restrict_unprivileged_userns=0`.

## Running Checks and Tests

From the repository root (see [`docs/testing/guide.md`](testing/guide.md) for all test tiers):

```bash
# Full pre-commit verification suite (plan copies, version pins, clean scan,
# tool unit tests, Compose config, Go tests with -race on a throwaway Postgres,
# web TypeScript + Vitest suite, and firewall namespace tests):
make check

# Run a single Go package with -race against the throwaway Postgres:
make test-go-pkg PKG=./internal/board/... GO_TEST_TIMEOUT=6m

# Run only the web TypeScript build and Vitest unit tests:
make test-web

# Fast pre-commit static checks:
python3 tools/plan-copies.py --check
python3 tools/check-versions.py
python3 tools/check-clean.py
python3 tools/check-clean.py --staged
```

## Installing and Updating on the Host (`crew-ctl`)

Always run `deploy/crew-ctl` from the checkout when building, gating, or updating a release. See the full [Install, Update, and Uninstall Guide](install-and-uninstall.md) for local VM (`CREW_TLS=internal`) vs production (`CREW_TLS=acme`) tracks, second-machine release cards, `versions.lock` apt rotation recovery, and Level 1-3 uninstall.

```bash
# First-time machine setup and install:
cp .env.example my.env
sudo deploy/crew-ctl doctor --env-file my.env
sudo deploy/crew-ctl install --build --env-file my.env
sudo crew-ctl status

# Build, gate, sync deploy files when changed, and install a numbered release:
sudo deploy/crew-ctl build --version X.Y.Z --title "Release summary"
sudo deploy/crew-ctl gate X.Y.Z
sudo deploy/crew-ctl install --deploy-files --version X.Y.Z
sudo deploy/crew-ctl update --version X.Y.Z
sudo crew-ctl doctor

# Install a release folder by hand without the live gate (marks byHand: true):
sudo deploy/crew-ctl install --from /var/lib/crew/releases/X.Y.Z --confirm install
```
