# image/: crew-runtime, the one runtime image

Every sandbox except a hosted project starts from this image (design 12.8, 13.4). It holds:

- `agy`, the only agent CLI since design 0.42, at the version `versions.lock` pins: the tarball its
  vendor's manifest names, checked by its sha512 (since design 0.56), and `/crew/versions.json`
  records it. The build is given the tarball as its build context `agy` and downloads none: Crew's
  server, or `crew-ctl build` on the machine, downloads it from the pinned address and checks it
  first (since design 0.57). A plain `docker build -f image/Dockerfile .` needs
  `--build-context agy=DIR`, DIR holding `cli_linux_x64.tar.gz`, or it fails at the copy. It does not update itself in a sandbox: the image sets
  `AGY_CLI_DISABLE_AUTO_UPDATE=true`, and `/opt/agy` stays root's.
- The harness, already in the home skeleton `/crew/home-skel`: the image build runs
  `crew-harnessbuild` over `harness/` and writes it out for agy, in the place agy reads it (12.1). The entrypoint copies the skeleton into the home before the sandbox is ready, and
  keeps a file the home already has. Nothing is installed when a sandbox starts.
- `crew-run`, which runs a CLI inside the sandbox (12.4; its verbs come with M2 and M3).
- The tools: Docker with buildx and compose, Go, Node.js, Python 3, build tools, git, `gh`, the
  Google Cloud CLI, kubectl, OpenTofu, tmux, jq, yq, hadolint, Chromium with the browser MCP server.
  What has a version or a checksum in `versions.lock` is checked against it; what comes from a
  package source is recorded in `/crew/versions.json`.

The user in every sandbox is `agent`, uid 1000, home `/home/agent`. It may use `sudo` and its own
Docker daemon, and is not root (13.5).

## Build

The build context is the repository's root, and it needs no token. It takes one more context, `agy`:
a folder holding agy's tarball as `versions.lock` pins it (`cli_linux_x64.tar.gz`), which the build
checks by its sha512 and does not download (since design 0.57):

```bash
sudo docker build -f image/Dockerfile --build-context agy=<folder> --build-arg CREW_VERSION=<version> -t crew-runtime:<version> .
```

`crew-ctl install --build` and `crew-ctl build` download the tarball from the pinned address, check it,
and build the image with it, the server's and the ingress's images beside it; Crew's own build does the
same from the server. The server's daily
look reads newer pins from the publishers and makes a ticket for each on Crew's own project (design
12.8); `make check` fails when a pin here differs from `versions.lock`.
