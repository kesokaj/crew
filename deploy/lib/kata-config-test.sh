#!/usr/bin/env bash
# deploy/lib/kata-config-test.sh — exercises deploy/lib/kata-config.sh against a scratch tree it makes
# itself: no root, no /opt, no Docker. internal/config/kata_config_script_test.go runs it under
# `go test ./...`; by hand:
#
#   bash deploy/lib/kata-config-test.sh
#
# It needs bash, coreutils, tar with zstd, and python3 with tomllib (3.11 or later).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=kata-config.sh
source "${HERE}/kata-config.sh"
PASS=0; FAIL=0
ok()  { echo "PASS $*"; PASS=$((PASS + 1)); }
bad() { echo "FAIL $*"; FAIL=$((FAIL + 1)); }
expect() { # <name> <want> <got>
    if [[ "$2" == "$3" ]]; then ok "$1"; else bad "$1: want '$2', got '$3'"; fi
}
SCRATCH="$(mktemp -d)"
trap 'rm -rf "${SCRATCH}"' EXIT
ROOT="${SCRATCH}/root"
mkdir -p "${ROOT}/opt"

# A release shaped as Kata's static archive: ./opt/kata with runtime-rs's shim, its Cloud Hypervisor
# configuration (the paths and keys of 4.3.1's), and VERSION.
make_release() { # VERSION OUT
    local t="${SCRATCH}/build-$1"
    mkdir -p "${t}/opt/kata/runtime-rs/bin" "$(dirname "${t}/opt/kata/${KATA_CLH_CONF}")" "${t}/opt/kata/bin"
    printf '#!/bin/sh\n' > "${t}/opt/kata/${KATA_SHIM}"
    chmod 0755 "${t}/opt/kata/${KATA_SHIM}"
    echo "$1" > "${t}/opt/kata/VERSION"
    cat > "${t}/opt/kata/${KATA_CLH_CONF}" <<'TOML'
[hypervisor.clh]
path = "/opt/kata/bin/cloud-hypervisor"
kernel = "/opt/kata/share/kata-containers/vmlinux.container"
image = "/opt/kata/share/kata-containers/kata-containers.img"
# Your distribution recommends: ["/opt/kata/bin/cloud-hypervisor"]
valid_hypervisor_paths = ["/opt/kata/bin/cloud-hypervisor"]
enable_annotations = ["enable_iommu", "kernel_params", "default_vcpus", "default_memory"]
default_vcpus = 1
overhead_vcpus = 0.2
default_memory = 2048
overhead_memory = 32
virtio_fs_daemon = "/opt/kata/libexec/virtiofsd"
reclaim_guest_freed_memory = false
guest_swap_path = "/run/kata-containers/swap"
not_moved = "/opt/katana/x"

[hypervisor.clh.factory]
template_path = "/run/vc/vm/template"

[runtime]
name = "virt_container"
TOML
    tar --zstd -cf "$2" -C "${t}" ./opt
}
make_release 4.3.1 "${SCRATCH}/k431.tar.zst"
make_release 4.4.0 "${SCRATCH}/k440.tar.zst"

# ── Where each version goes ──────────────────────────────────────────────────
expect "dir/3.31 is the old layout" "${ROOT}/opt/kata" "$(kata_dir "${ROOT}" 3.31.0)"
expect "dir/4.x has a folder of its own" "${ROOT}/opt/kata-4.3.1" "$(kata_dir "${ROOT}" 4.3.1)"
expect "dir/on the machine" "/opt/kata-4.3.1" "$(kata_dir "" 4.3.1)"

# ── Unpacking beside what runs ───────────────────────────────────────────────
D431="${ROOT}/opt/kata-4.3.1"
expect "unpack: first run" changed "$(kata_unpack "${SCRATCH}/k431.tar.zst" "${D431}" 4.3.1)"
expect "unpack: second run" unchanged "$(kata_unpack "${SCRATCH}/k431.tar.zst" "${D431}" 4.3.1)"
expect "unpack: the version" 4.3.1 "$(cat "${D431}/VERSION")"
if [[ -x "${D431}/${KATA_SHIM}" ]]; then ok "unpack: the shim"; else bad "unpack: no shim"; fi
leftover="$(find "${ROOT}/opt" -maxdepth 1 -name '*.unpack.*' | wc -l)"
expect "unpack: nothing left beside it" 0 "${leftover}"
# A folder half made, by a run that stopped, is made again.
rm "${D431}/${KATA_SHIM}"
expect "unpack: a half folder is made again" changed "$(kata_unpack "${SCRATCH}/k431.tar.zst" "${D431}" 4.3.1)"
if [[ -x "${D431}/${KATA_SHIM}" ]]; then ok "unpack: the half folder has its shim"; else bad "unpack: still no shim"; fi
# An archive of another version than the lock says: refused, and DIR not made.
got="$(kata_unpack "${SCRATCH}/k440.tar.zst" "${ROOT}/opt/kata-9.9.9" 9.9.9)" && rc=0 || rc=$?
expect "unpack/wrong version: exit status" 1 "${rc}"
expect "unpack/wrong version: message" "failed: the archive holds no Kata 9.9.9 with runtime-rs and Cloud Hypervisor" "${got}"
if [[ ! -e "${ROOT}/opt/kata-9.9.9" ]]; then ok "unpack/wrong version: nothing made"; else bad "unpack/wrong version: folder made"; fi
leftover="$(find "${ROOT}/opt" -maxdepth 1 -name '*.unpack.*' | wc -l)"
expect "unpack/wrong version: nothing left beside it" 0 "${leftover}"
ln -s "${D431}" "${ROOT}/opt/kata-7.0.0"
got="$(kata_unpack "${SCRATCH}/k431.tar.zst" "${ROOT}/opt/kata-7.0.0" 7.0.0)" && rc=0 || rc=$?
expect "unpack/symlink: refused" 1 "${rc}"
rm "${ROOT}/opt/kata-7.0.0"

# ── Crew's drop-ins, beside Kata's own configuration ─────────────────────────
CONF="${D431}/${KATA_CLH_CONF}"
cp "${CONF}" "${SCRATCH}/shipped.toml"
expect "dropins: first run" changed "$(kata_dropins "${D431}")"
expect "dropins: second run" unchanged "$(kata_dropins "${D431}")"
if diff -q "${CONF}" "${SCRATCH}/shipped.toml" >/dev/null; then ok "dropins: Kata's file untouched"; else bad "dropins: Kata's file changed"; fi
DD="$(dirname "${CONF}")/config.d"
# What runtime-rs reads: the base, then each drop-in in the order of its name, tables merged, a value
# or an array replaced (kata-types' drop_in.rs).
merged="${SCRATCH}/merged.toml.json"
python3 -I - "${CONF}" "${DD}" > "${merged}" <<'PY'
import json, os, sys, tomllib
def merge(a, b):
    for k, v in b.items():
        if isinstance(v, dict) and isinstance(a.get(k), dict):
            merge(a[k], v)
        else:
            a[k] = v
base = tomllib.load(open(sys.argv[1], "rb"))
for f in sorted(os.listdir(sys.argv[2])):
    merge(base, tomllib.load(open(os.path.join(sys.argv[2], f), "rb")))
print(json.dumps(base))
PY
m() { python3 -I -c 'import json,sys
d = json.load(open(sys.argv[1]))
for k in sys.argv[2].split("."):
    d = d[k]
print(json.dumps(d))' "${merged}" "$1"; }
expect "merged: Cloud Hypervisor moved" "\"${D431}/bin/cloud-hypervisor\"" "$(m hypervisor.clh.path)"
expect "merged: the kernel moved" "\"${D431}/share/kata-containers/vmlinux.container\"" "$(m hypervisor.clh.kernel)"
expect "merged: the image moved" "\"${D431}/share/kata-containers/kata-containers.img\"" "$(m hypervisor.clh.image)"
expect "merged: virtiofsd moved" "\"${D431}/libexec/virtiofsd\"" "$(m hypervisor.clh.virtio_fs_daemon)"
expect "merged: the allowed hypervisor moved" "[\"${D431}/bin/cloud-hypervisor\"]" "$(m hypervisor.clh.valid_hypervisor_paths)"
expect "merged: a path that only begins like it stays" '"/opt/katana/x"' "$(m hypervisor.clh.not_moved)"
expect "merged: a path elsewhere stays" '"/run/kata-containers/swap"' "$(m hypervisor.clh.guest_swap_path)"
expect "merged: a sub-table stays" '"/run/vc/vm/template"' "$(m hypervisor.clh.factory.template_path)"
expect "merged: overhead CPUs as 3.31's base" 1.0 "$(m hypervisor.clh.overhead_vcpus)"
expect "merged: overhead memory as 3.31's base" 2048 "$(m hypervisor.clh.overhead_memory)"
expect "merged: the base itself" "1 2048" "$(m hypervisor.clh.default_vcpus) $(m hypervisor.clh.default_memory)"
expect "merged: memory given back" true "$(m hypervisor.clh.reclaim_guest_freed_memory)"
expect "merged: no annotation taken" '[]' "$(m hypervisor.clh.enable_annotations)"
expect "merged: the runtime's own table stays" '"virt_container"' "$(m runtime.name)"
# A drop-in Crew does not write is taken out; one Crew writes, changed by hand, is written again.
echo '[hypervisor.clh]' > "${DD}/60-other.toml"
echo 'overhead_memory = 32' >> "${DD}/60-other.toml"
echo '# changed by hand' >> "${DD}/50-crew.toml"
expect "dropins: a stranger and an edit" changed "$(kata_dropins "${D431}")"
if [[ ! -e "${DD}/60-other.toml" ]]; then ok "dropins: the stranger is gone"; else bad "dropins: the stranger stays"; fi
expect "dropins: the edit undone" "$(kata_crew_dropin)" "$(cat "${DD}/50-crew.toml")"
# A configuration with no path under /opt/kata: refused, as a release that moved would otherwise
# start with the paths of no folder at all.
D_NONE="${SCRATCH}/none"
mkdir -p "$(dirname "${D_NONE}/${KATA_CLH_CONF}")"
printf '[hypervisor.clh]\npath = "/usr/bin/cloud-hypervisor"\n' > "${D_NONE}/${KATA_CLH_CONF}"
got="$(kata_dropins "${D_NONE}")" && rc=0 || rc=$?
expect "dropins/no path: exit status" 1 "${rc}"
expect "dropins/no path: message" "failed: ${D_NONE}/${KATA_CLH_CONF} names no path under /opt/kata" "${got}"

# ── Docker's runtime mvm ─────────────────────────────────────────────────────
DJ="${ROOT}/etc/docker/daemon.json"
mkdir -p "$(dirname "${DJ}")"
printf '{"live-restore": true, "runtimes": {"gvisor": {"path": "/usr/bin/runsc"}, "mvm": {"runtimeType": "io.containerd.mvm.v2"}}}\n' > "${DJ}"
want="$(kata_runtime_json "${D431}")"
expect "runtime: the shim by path" "\"${D431}/${KATA_SHIM}\"" "$(python3 -I -c 'import json,sys; print(json.dumps(json.loads(sys.argv[1])["runtimeType"]))' "${want}")"
expect "runtime: ConfigPath" "\"${D431}/${KATA_CLH_CONF}\"" "$(python3 -I -c 'import json,sys; print(json.dumps(json.loads(sys.argv[1])["options"]["ConfigPath"]))' "${want}")"
got="$(kata_daemon_runtime "${DJ}" mvm-next "${want}")"
expect "runtime/trial: the runtimes before" 'changed {"runtimes": {"gvisor": {"path": "/usr/bin/runsc"}, "mvm": {"runtimeType": "io.containerd.mvm.v2"}}}' "${got}"
expect "runtime/trial: again" unchanged "$(kata_daemon_runtime "${DJ}" mvm-next "${want}")"
expect "runtime/trial: mvm still 3.31's" '{"runtimeType": "io.containerd.mvm.v2"}' "$(python3 -I -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))["runtimes"]["mvm"]))' "${DJ}")"
kata_daemon_runtime "${DJ}" mvm "${want}" >/dev/null
expect "runtime/trial out" "changed" "$(kata_daemon_runtime "${DJ}" mvm-next null | cut -d' ' -f1)"
expect "runtime/trial out again" unchanged "$(kata_daemon_runtime "${DJ}" mvm-next null)"
expect "runtime: the other keys kept" 'true {"path": "/usr/bin/runsc"}' "$(python3 -I -c 'import json,sys; d=json.load(open(sys.argv[1])); print(json.dumps(d["live-restore"]), json.dumps(d["runtimes"]["gvisor"]))' "${DJ}")"
NEW_DJ="${SCRATCH}/fresh/daemon.json"
expect "runtime: a file that was not there" 'changed {"runtimes": "<absent>"}' "$(kata_daemon_runtime "${NEW_DJ}" mvm "${want}")"

# ── What runs now ─────────────────────────────────────────────────────────────
expect "in use: 4.x by its folder" "4.3.1 ${D431}" "$(kata_in_use "${DJ}" "${ROOT}")"
LEGACY_DJ="${SCRATCH}/legacy.json"
printf '{"runtimes": {"mvm": {"runtimeType": "io.containerd.mvm.v2"}}}\n' > "${LEGACY_DJ}"
mkdir -p "${ROOT}/opt/kata/bin"
printf '#!/bin/sh\necho "kata-runtime  : 3.31.0"\necho "   commit   : abc"\n' > "${ROOT}/opt/kata/bin/kata-runtime"
chmod 0755 "${ROOT}/opt/kata/bin/kata-runtime"
expect "in use: 3.31 says its version" "3.31.0 ${ROOT}/opt/kata" "$(kata_in_use "${LEGACY_DJ}" "${ROOT}")"
printf '{"runtimes": {}}\n' > "${SCRATCH}/none.json"
if kata_in_use "${SCRATCH}/none.json" "${ROOT}" >/dev/null; then bad "in use: none said to be"; else ok "in use: no mvm, none"; fi
if kata_in_use "${SCRATCH}/absent.json" "${ROOT}" >/dev/null; then bad "in use: no file said to be"; else ok "in use: no file, none"; fi
printf '{"runtimes": {"mvm": {"runtimeType": "%s/opt/kata-5.0.0/%s"}}}\n' "${ROOT}" "${KATA_SHIM}" > "${SCRATCH}/gone.json"
if kata_in_use "${SCRATCH}/gone.json" "${ROOT}" >/dev/null; then bad "in use: a folder that is gone"; else ok "in use: a folder that is gone, none"; fi

# ── Older ────────────────────────────────────────────────────────────────────
older_case() { if kata_older "$1" "$2"; then echo yes; else echo no; fi; }
expect "older: 3.31.0 than 4.3.1" yes "$(older_case 3.31.0 4.3.1)"
expect "older: 4.3.1 than 4.10.0" yes "$(older_case 4.3.1 4.10.0)"
expect "older: 4.3.1 than itself" no "$(older_case 4.3.1 4.3.1)"
expect "older: 4.4.0 than 4.3.1" no "$(older_case 4.4.0 4.3.1)"

# ── The new runtime's proof ──────────────────────────────────────────────────
expect "proof: small, as 3.31" ok "$(kata_proof_check 6.12.0-host $'6.18.56\n2\n2014908')"
got="$(kata_proof_check 6.12.0-host $'6.12.0-host\n2\n2014908')" && rc=0 || rc=$?
expect "proof: the machine's kernel" "1 the microVM has the machine's own kernel, or none: 6.12.0-host" "${rc} ${got}"
got="$(kata_proof_check 6.12.0-host $'6.18.56\n2\n28000')" && rc=0 || rc=$?
expect "proof: Kata's 32 MiB" "1 the microVM of the small size has 28000 kB of memory, not about 2 GB" "${rc} ${got}"
got="$(kata_proof_check 6.12.0-host $'6.18.56\n1\n2014908')" && rc=0 || rc=$?
expect "proof: one CPU" "1 the microVM of the small size has 1 virtual CPUs, not 2" "${rc} ${got}"
got="$(kata_proof_check 6.12.0-host '')" && rc=0 || rc=$?
expect "proof: nothing printed" 1 "${rc}"

# ── The old folders, and what still runs from them ─────────────────────────────
mkdir -p "${ROOT}/opt/kata-4.2.0" "${ROOT}/opt/kata-4.1.0" "${ROOT}/opt/kata-4.0.0"
ln -sfn "${D431}" "${ROOT}/opt/kata-4.9.9"
OLD_DJ="${SCRATCH}/old-daemon.json"
# The operator's own Kata 4.0.0 behind a runtime of their own; Crew's 4.3.1 is mvm.
printf '{"runtimes": {"mvm": {"runtimeType": "%s/%s", "options": {"ConfigPath": "%s/%s"}}, "kata": {"runtimeType": "io.containerd.kata.v2", "options": {"ConfigPath": "%s/opt/kata-4.0.0/x.toml"}}}}\n' \
    "${D431}" "${KATA_SHIM}" "${D431}" "${KATA_CLH_CONF}" "${ROOT}" > "${OLD_DJ}"
OURS="${ROOT}/opt/kata-4.2.0
${ROOT}/opt/kata-4.0.0
${ROOT}/opt/kata-4.9.9
${D431}
${ROOT}/opt/kata-1/../../etc
${ROOT}/etc"
expect "old: only what install.json lists, and no runtime names" "${ROOT}/opt/kata-4.2.0" "$(kata_old_dirs "${ROOT}" "${D431}" "${OLD_DJ}" "${OURS}")"
if kata_old_dirs "${ROOT}" "${D431}" "${OLD_DJ}" "${OURS}" | grep -x "${ROOT}/opt/kata-4.1.0" >/dev/null; then bad "old: a folder Crew did not record"; else ok "old: a folder Crew did not record stays"; fi
expect "old: 3.31's when listed and mvm left it" "${ROOT}/opt/kata-4.2.0 ${ROOT}/opt/kata" "$(kata_old_dirs "${ROOT}" "${D431}" "${OLD_DJ}" "${OURS}
${ROOT}/opt/kata" | tr '\n' ' ' | sed 's/ $//')"
expect "old: 3.31's not while mvm still starts it" "" "$(kata_old_dirs "${ROOT}" "${D431}" "${LEGACY_DJ}" "${ROOT}/opt/kata")"
expect "old: never what runs" "" "$(kata_old_dirs "${ROOT}" "${ROOT}/opt/kata-4.2.0" "${OLD_DJ}" "${ROOT}/opt/kata-4.2.0")"
expect "old: no daemon.json, the list alone" "${ROOT}/opt/kata-4.2.0" "$(kata_old_dirs "${ROOT}" "${D431}" "${SCRATCH}/absent.json" "${ROOT}/opt/kata-4.2.0")"
folder_case() { if kata_folder "${ROOT}" "$1"; then echo yes; else echo no; fi; }
expect "folder: /opt/kata" yes "$(folder_case "${ROOT}/opt/kata")"
expect "folder: a version" yes "$(folder_case "${ROOT}/opt/kata-4.3.1")"
expect "folder: a way out" no "$(folder_case "${ROOT}/opt/kata-1/../../etc")"
expect "folder: two dots in a name" no "$(folder_case "${ROOT}/opt/kata-4..3")"
expect "folder: elsewhere" no "$(folder_case "${ROOT}/etc")"
expect "folder: a name with a space" no "$(folder_case "${ROOT}/opt/kata-4.3.1 x")"
PROC="${SCRATCH}/proc"
mkdir -p "${PROC}/101" "${PROC}/202" "${PROC}/303" "${PROC}/self"
ln -s /usr/bin/dockerd "${PROC}/101/exe"
ln -s "${ROOT}/opt/kata/bin/containerd-shim-kata-v2" "${PROC}/202/exe"
ln -s /usr/bin/sleep "${PROC}/303/exe"
: > "${PROC}/101/maps"; : > "${PROC}/202/maps"; : > "${PROC}/303/maps"
expect "busy: a 3.31 shim runs" "202 ${ROOT}/opt/kata/bin/containerd-shim-kata-v2" "$(kata_busy "${ROOT}/opt/kata" "${PROC}")"
if kata_busy "${ROOT}/opt/kata-4.2.0" "${PROC}" >/dev/null; then bad "busy: 4.2.0 said to run"; else ok "busy: nothing from 4.2.0"; fi
# A folder whose name begins like another's is not it.
ln -sfn "${ROOT}/opt/kata-4.2.0x/bin/cloud-hypervisor" "${PROC}/101/exe"
if kata_busy "${ROOT}/opt/kata-4.2.0" "${PROC}" >/dev/null; then bad "busy: a longer name said to be it"; else ok "busy: a longer name is another folder"; fi
# A file mapped from the folder counts, as an executable does.
echo "7f00-7f01 r-xp 00000000 08:01 42 ${ROOT}/opt/kata-4.2.0/lib/libx.so" > "${PROC}/303/maps"
expect "busy: a mapped file" "303 a file under ${ROOT}/opt/kata-4.2.0 mapped" "$(kata_busy "${ROOT}/opt/kata-4.2.0" "${PROC}")"
got="$(kata_remove "${ROOT}/opt/kata-4.2.0" "${PROC}")" && rc=0 || rc=$?
expect "remove: busy is kept" "1 busy 303 a file under ${ROOT}/opt/kata-4.2.0 mapped" "${rc} ${got}"
if [[ -d "${ROOT}/opt/kata-4.2.0" && ! -e "${ROOT}/opt/kata-4.2.0.removing" ]]; then ok "remove: busy, in its place"; else bad "remove: busy, moved"; fi
: > "${PROC}/303/maps"
expect "remove: idle" removed "$(kata_remove "${ROOT}/opt/kata-4.2.0" "${PROC}")"
if [[ ! -e "${ROOT}/opt/kata-4.2.0" && ! -e "${ROOT}/opt/kata-4.2.0.removing" ]]; then ok "remove: gone, nothing aside"; else bad "remove: something left"; fi
# Something that starts from the folder between the two looks: renamed back.
mkdir -p "${ROOT}/opt/kata-4.1.0"
ln -sfn "${ROOT}/opt/kata-4.1.0.removing/bin/cloud-hypervisor" "${PROC}/303/exe"
got="$(kata_remove "${ROOT}/opt/kata-4.1.0" "${PROC}")" && rc=0 || rc=$?
expect "remove: started in between" "1 busy 303 ${ROOT}/opt/kata-4.1.0.removing/bin/cloud-hypervisor" "${rc} ${got}"
if [[ -d "${ROOT}/opt/kata-4.1.0" && ! -e "${ROOT}/opt/kata-4.1.0.removing" ]]; then ok "remove: renamed back"; else bad "remove: not renamed back"; fi
ln -sfn /usr/bin/sleep "${PROC}/303/exe"
# A run stopped between the rename and the removal: the next run takes FOLDER.removing, if listed.
mkdir -p "${ROOT}/opt/kata-4.1.0.removing" "${ROOT}/opt/kata-3.9.9.removing"
expect "old: a folder left renamed" "${ROOT}/opt/kata-4.1.0 ${ROOT}/opt/kata-4.1.0.removing" "$(kata_old_dirs "${ROOT}" "${D431}" "${OLD_DJ}" "${ROOT}/opt/kata-4.1.0" | tr '\n' ' ' | sed 's/ $//')"
expect "remove: one left renamed" removed "$(kata_remove "${ROOT}/opt/kata-4.1.0.removing" "${PROC}")"
if [[ -d "${ROOT}/opt/kata-3.9.9.removing" ]]; then ok "old: an unlisted one left renamed stays"; else bad "old: an unlisted one removed"; fi

# ── Which shim runs a container, from /proc (Docker's info does not say: BUG-002) ──
ID=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
mkdir -p "${PROC}/404" "${PROC}/505"
ln -sfn "${D431}/${KATA_SHIM}" "${PROC}/404/exe"
printf 'containerd-shim-kata-v2\0-namespace\0moby\0-id\0%s\0-address\0/run/containerd.sock\0' "${ID}" > "${PROC}/404/cmdline"
ln -sfn /usr/bin/sleep "${PROC}/505/exe"
printf 'sleep\0%s\0' "${ID}" > "${PROC}/505/cmdline"
expect "shim: the one that runs the container" "${D431}/${KATA_SHIM}" "$(kata_shim_of "${ID}" "${PROC}")"
if kata_shim_of "fedcba9876543210" "${PROC}" >/dev/null; then bad "shim: one for a container it does not run"; else ok "shim: none for another container"; fi
if kata_shim_of "a b" "${PROC}" >/dev/null; then bad "shim: an id of another shape"; else ok "shim: an id of another shape, none"; fi

# ── A runtime that names a folder keeps it, and a doubt keeps it too ─────────────
printf '{"runtimes": {"other": {"runtimeType": "%s/opt//kata-4.1.0/runtime-rs/bin/containerd-shim-kata-v2"}}}\n' "${ROOT}" > "${SCRATCH}/slashes.json"
if kata_named "${SCRATCH}/slashes.json" "${ROOT}/opt/kata-4.1.0" "${ROOT}"; then ok "named: by a path with two slashes"; else bad "named: two slashes missed"; fi
ln -sfn "${ROOT}/opt/kata-4.1.0" "${SCRATCH}/alias"
printf '{"runtimes": {"other": {"runtimeType": "%s/alias/runtime-rs/bin/containerd-shim-kata-v2"}}}\n' "${SCRATCH}" > "${SCRATCH}/alias.json"
if kata_named "${SCRATCH}/alias.json" "${ROOT}/opt/kata-4.1.0" "${ROOT}"; then ok "named: through a link"; else bad "named: a link missed"; fi
printf '{"runtimes": {"mvm": ' > "${SCRATCH}/cut.json"
if kata_named "${SCRATCH}/cut.json" "${ROOT}/opt/kata-4.1.0" "${ROOT}"; then ok "named: a file cut short keeps it"; else bad "named: a file cut short frees it"; fi
printf '{"runtimes": {"mvm": null}}\n' > "${SCRATCH}/null.json"
if kata_named "${SCRATCH}/null.json" "${ROOT}/opt/kata-4.1.0" "${ROOT}"; then ok "named: a runtime of null keeps it"; else bad "named: a runtime of null frees it"; fi
if kata_named "${SCRATCH}/absent.json" "${ROOT}/opt/kata-4.1.0" "${ROOT}"; then bad "named: no file keeps it"; else ok "named: no file, nothing names it"; fi
expect "old: a doubt about daemon.json removes nothing" "" "$(kata_old_dirs "${ROOT}" "${D431}" "${SCRATCH}/cut.json" "${ROOT}/opt/kata-4.1.0")"

# ── A stopped unpack leaves nothing for good ──────────────────────────────────
mkdir -p "${D431}.unpack.stale1/opt/kata"
expect "unpack: a stale half unpack is taken out" unchanged "$(kata_unpack "${SCRATCH}/k431.tar.zst" "${D431}" 4.3.1)"
if [[ ! -e "${D431}.unpack.stale1" ]]; then ok "unpack: the stale folder is gone"; else bad "unpack: the stale folder stays"; fi

# ── Paths read from daemon.json are held to Kata's shape ──────────────────────
printf '{"runtimes": {"mvm": {"runtimeType": "%s/opt/kata-4.3.1/../../srv/k/%s"}}}\n' "${ROOT}" "${KATA_SHIM}" > "${SCRATCH}/way-out.json"
if kata_in_use "${SCRATCH}/way-out.json" "${ROOT}" >/dev/null; then bad "in use: a way out of /opt taken"; else ok "in use: a way out of /opt refused"; fi
ln -sfn "${D431}" "${ROOT}/opt/kata-8.0.0"
printf '{"runtimes": {"mvm": {"runtimeType": "%s/opt/kata-8.0.0/%s"}}}\n' "${ROOT}" "${KATA_SHIM}" > "${SCRATCH}/link.json"
if kata_in_use "${SCRATCH}/link.json" "${ROOT}" >/dev/null; then bad "in use: a symlinked folder taken"; else ok "in use: a symlinked folder refused"; fi
rm "${ROOT}/opt/kata-8.0.0"
expect "runtime: a quote stays in its string" '{"runtimeType": "/opt/x\"y/runtime-rs/bin/containerd-shim-kata-v2", "options": {"ConfigPath": "/opt/x\"y/share/defaults/kata-containers/runtime-rs/configuration-clh-runtime-rs.toml"}}' "$(kata_runtime_json '/opt/x"y')"

echo "kata-config: ${PASS} passed, ${FAIL} failed"
[[ "${FAIL}" -eq 0 ]]
