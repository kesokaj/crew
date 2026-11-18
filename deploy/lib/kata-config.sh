# shellcheck shell=bash
# deploy/lib/kata-config.sh — Kata Containers on the machine (design 13.2), as functions sourced by
# host-setup.sh and crew-ctl. Paths are arguments, so deploy/lib/kata-config-test.sh points them at a
# scratch dir; each writer prints `changed` or `unchanged`, or the reason and a non-zero status.
#
# Since 0.68 Crew runs Kata 4.x, whose one runtime is runtime-rs. Each version is unpacked to a folder
# of its own, /opt/kata-<version>, beside the one in use. Kata's configuration of Cloud Hypervisor is
# never edited: Crew's settings are drop-ins in config.d beside it, which runtime-rs merges into it, as
# kata-deploy does. Docker's runtime mvm names that folder's shim by its path, with the configuration as
# its ConfigPath option, so containerd keeps each sandbox's own shim: a switch touches only what starts
# after it, and a folder goes once no process runs from it. Kata 3.31, before 0.68, was /opt/kata with a
# shim wrapper on PATH that named its configuration in KATA_CONF_FILE, which runtime-rs refuses.

KATA_SHIM=runtime-rs/bin/containerd-shim-kata-v2
KATA_CLH_CONF=share/defaults/kata-containers/runtime-rs/configuration-clh-runtime-rs.toml
# 3.31's wrapper, which containerd found on PATH for the runtime type io.containerd.mvm.v2. It goes with
# /opt/kata, and only then: containerd runs it again to clean up a sandbox that 3.31 started.
# shellcheck disable=SC2034  # used by host-setup.sh
KATA_WRAPPER=/usr/local/bin/containerd-shim-mvm-v2

# kata_folder ROOT PATH: whether PATH is the name of a folder of Kata under ROOT: /opt/kata, or
# /opt/kata-<version> with no slash and no "..", the version as the server reads it (orchestrator's
# kataFolder). A root rm -rf takes nothing else (security review of 507b11c).
kata_folder() {
    python3 -I -c 'import re, sys
root, path = sys.argv[1], sys.argv[2]
ok = path.startswith(root + "/opt/") and re.fullmatch(r"kata(-[0-9][0-9A-Za-z.+~-]*)?", path[len(root) + 5:]) and ".." not in path[len(root):]
sys.exit(0 if ok else 1)' "$1" "$2"
}

# kata_dir ROOT VERSION: the folder of a Kata version under ROOT ("" on the machine).
kata_dir() {
    case "$2" in
        [0-3].*) echo "$1/opt/kata" ;;
        *) echo "$1/opt/kata-$2" ;;
    esac
}

# kata_unpacked DIR VERSION: whether DIR holds that version whole.
kata_unpacked() {
    [[ ! -L "$1" && -x "$1/${KATA_SHIM}" && -f "$1/${KATA_CLH_CONF}" && "$(cat "$1/VERSION" 2>/dev/null)" == "$2" ]]
}

# kata_unpack ARCHIVE DIR VERSION: unpacks a static release, checked before, whose tree is ./opt/kata,
# to DIR. It is unpacked beside DIR first and then renamed, so DIR is whole or absent; a DIR left half
# made by a run that stopped is made again.
kata_unpack() {
    local archive="$1" dir="$2" version="$3" tmp stale
    # What a run that was stopped while it unpacked left beside DIR: nothing runs from it.
    for stale in "${dir}".unpack.*; do
        [[ -e "${stale}" || -L "${stale}" ]] && rm -rf -- "${stale}"
    done
    if kata_unpacked "${dir}" "${version}"; then echo unchanged; return 0; fi
    if [[ -L "${dir}" ]]; then echo "refused: ${dir} is a symlink"; return 1; fi
    mkdir -p "$(dirname "${dir}")" || { echo "failed: mkdir $(dirname "${dir}")"; return 1; }
    tmp="$(mktemp -d "${dir}.unpack.XXXXXX")" || { echo "failed: mktemp beside ${dir}"; return 1; }
    if ! tar --zstd -xf "${archive}" -C "${tmp}"; then
        rm -rf "${tmp}"; echo "failed: unpacking ${archive}"; return 1
    fi
    if ! kata_unpacked "${tmp}/opt/kata" "${version}"; then
        rm -rf "${tmp}"; echo "failed: the archive holds no Kata ${version} with runtime-rs and Cloud Hypervisor"; return 1
    fi
    if ! { rm -rf "${dir}" && mv "${tmp}/opt/kata" "${dir}" && rm -rf "${tmp}"; }; then
        rm -rf "${tmp}"; echo "failed: moving the release to ${dir}"; return 1
    fi
    echo changed
}

# kata_prefix_dropin CONF FROM TO: a drop-in that moves every path of CONF under FROM to TO, as
# kata-deploy's 10-installation-prefix.toml does: the static release names /opt/kata throughout. Empty
# when no path moves. Arrays are written whole, since a drop-in replaces them.
kata_prefix_dropin() {
    python3 -I - "$1" "$2" "$3" <<'PY'
import json, re, sys, tomllib
conf, old, new = sys.argv[1:4]
with open(conf, "rb") as f:
    doc = tomllib.load(f)
def moved(v):
    if isinstance(v, str) and (v == old or v.startswith(old + "/")):
        return new + v[len(old):]
    return None
def key(k):
    return k if re.fullmatch(r"[A-Za-z0-9_-]+", k) else json.dumps(k, ensure_ascii=False)
out = []
def walk(table, path):
    lines = []
    for k, v in table.items():
        if isinstance(v, str) and moved(v) is not None:
            lines.append(f"{key(k)} = {json.dumps(moved(v), ensure_ascii=False)}")
        elif isinstance(v, list) and v and all(isinstance(x, str) for x in v) and any(moved(x) is not None for x in v):
            lines.append(f"{key(k)} = [" + ", ".join(json.dumps(moved(x) or x, ensure_ascii=False) for x in v) + "]")
    if lines:
        out.append(("[" + ".".join(key(p) for p in path) + "]\n" if path else "") + "\n".join(lines))
    for k, v in table.items():
        if isinstance(v, dict):
            walk(v, path + [k])
walk(doc, [])
if out:
    print(f"# Written by Crew's host setup (13.2): the paths of Kata's configuration under {old}, moved to {new}.")
    print("\n\n".join(out))
PY
}

# kata_crew_dropin: Crew's own settings of Cloud Hypervisor. runtime-rs sizes a microVM as what its
# container asks for plus overhead_vcpus and overhead_memory, or default_vcpus and default_memory (1 and
# 2048) when it asks for nothing; Kata ships 0.2 and 32 MiB, which would boot a small sandbox, which asks
# for a CPU and no memory, with 32 MiB. 1 and 2048 keep 3.31's sizes, which Crew's count on (13.10).
kata_crew_dropin() {
    cat <<'TOML'
# Written by Crew's host setup (13.2); edits here are overwritten.
# Each microVM has 1 virtual CPU and 2 GB beside what its container asks for, as with Kata 3.31, which
# Crew's sandbox sizes count on (13.10, 14.1). Memory a guest frees goes back to the machine. No Kata
# annotation is taken from a container: Crew sets none (GHSA-37rf-pj8c-g5g3).
[hypervisor.clh]
overhead_vcpus = 1.0
overhead_memory = 2048
reclaim_guest_freed_memory = true
enable_annotations = []
TOML
}

# kata_write PATH CONTENT: writes PATH when its content differs, by a rename in its own folder.
kata_write() {
    local path="$1" want="$2" tmp
    if [[ -f "${path}" && ! -L "${path}" && "$(cat "${path}")" == "${want}" ]]; then
        echo unchanged
        return 0
    fi
    mkdir -p "$(dirname "${path}")" || { echo "failed: mkdir $(dirname "${path}")"; return 1; }
    tmp="$(mktemp "${path}.XXXXXX")" || { echo "failed: mktemp beside ${path}"; return 1; }
    printf '%s\n' "${want}" > "${tmp}" && chmod 0644 "${tmp}" && mv -f "${tmp}" "${path}" \
        || { rm -f "${tmp}"; echo "failed: writing ${path}"; return 1; }
    echo changed
}

# kata_dropins DIR: Crew's two drop-ins in DIR's config.d, checked by content on every run. A drop-in
# Crew does not write is taken out: what DIR's configuration is, is these two and Kata's file.
kata_dropins() {
    local dir="$1" d prefix r changed=0 f
    d="$(dirname "${dir}/${KATA_CLH_CONF}")/config.d"
    prefix="$(kata_prefix_dropin "${dir}/${KATA_CLH_CONF}" /opt/kata "${dir}")" || { echo "failed: reading ${dir}/${KATA_CLH_CONF}"; return 1; }
    if [[ -z "${prefix}" ]]; then echo "failed: ${dir}/${KATA_CLH_CONF} names no path under /opt/kata"; return 1; fi
    r="$(kata_write "${d}/10-installation-prefix.toml" "${prefix}")" || { echo "${r}"; return 1; }
    [[ "${r}" == changed ]] && changed=1
    r="$(kata_write "${d}/50-crew.toml" "$(kata_crew_dropin)")" || { echo "${r}"; return 1; }
    [[ "${r}" == changed ]] && changed=1
    for f in "${d}"/*; do
        case "$(basename "${f}")" in 10-installation-prefix.toml|50-crew.toml) ;; *) rm -rf "${f}"; changed=1 ;; esac
    done
    if [[ "${changed}" == 1 ]]; then echo changed; else echo unchanged; fi
}

# kata_runtime_json DIR: Docker's runtime entry for the Kata in DIR: its shim by path, and Crew's
# configuration as the shim's ConfigPath, which Docker passes on as containerd's runtime options.
kata_runtime_json() {
    python3 -I -c 'import json, sys; print(json.dumps({"runtimeType": sys.argv[1], "options": {"ConfigPath": sys.argv[2]}}))' \
        "$1/${KATA_SHIM}" "$1/${KATA_CLH_CONF}"
}

# kata_daemon_runtime FILE NAME JSON|null: sets Docker's runtime NAME in FILE to JSON, or takes it out
# with null, by a rename. Prints `unchanged`, or `changed` and the runtimes the file had before, as JSON
# on the same line, for the record that uninstall puts back.
kata_daemon_runtime() {
    python3 -I - "$1" "$2" "$3" <<'PY'
import json, os, sys, tempfile
path, name, want = sys.argv[1], sys.argv[2], json.loads(sys.argv[3])
cfg = {}
if os.path.exists(path):
    with open(path) as f:
        cfg = json.load(f)
before = cfg.get("runtimes", "<absent>")
runtimes = dict(cfg.get("runtimes", {}))
if want is None:
    if name not in runtimes:
        print("unchanged"); sys.exit(0)
    del runtimes[name]
else:
    if runtimes.get(name) == want:
        print("unchanged"); sys.exit(0)
    runtimes[name] = want
cfg["runtimes"] = runtimes
os.makedirs(os.path.dirname(path) or ".", exist_ok=True)
fd, tmp = tempfile.mkstemp(dir=os.path.dirname(path) or ".")
with os.fdopen(fd, "w") as f:
    json.dump(cfg, f, indent=4, sort_keys=True)
os.chmod(tmp, 0o644)
os.replace(tmp, path)
print("changed " + json.dumps({"runtimes": before}, sort_keys=True))
PY
}

# kata_in_use FILE ROOT: "VERSION DIR" of the Kata that Docker's runtime mvm in FILE starts, or nothing
# and 1. From 4.x the folder in its shim's path, with the folder's VERSION; 3.31's wrapper ran
# /opt/kata, which says its version itself.
kata_in_use() {
    local file="$1" root="$2" dir v
    dir="$(python3 -I -c 'import json, sys
try:
    t = json.load(open(sys.argv[1]))["runtimes"]["mvm"].get("runtimeType", "")
except Exception:
    sys.exit(1)
if t == "io.containerd.mvm.v2":
    print("legacy")
elif t.endswith("/" + sys.argv[2]):
    print(t[: -len(sys.argv[2]) - 1])
else:
    sys.exit(1)' "${file}" "${KATA_SHIM}" 2>/dev/null)" || return 1
    if [[ "${dir}" == legacy ]]; then
        [[ -x "${root}/opt/kata/bin/kata-runtime" ]] || return 1
        v="$("${root}/opt/kata/bin/kata-runtime" --version 2>/dev/null | awk '/kata-runtime/ && !f {print $3; f = 1}')"
        [[ -n "${v}" ]] || return 1
        echo "${v} ${root}/opt/kata"
        return 0
    fi
    # Only a folder of Kata's own shape, and no symlink on the way to it.
    [[ "${dir}" != "${root}/opt/kata" ]] && kata_folder "${root}" "${dir}" || return 1
    local rr; rr="$(realpath -e -- "${root:-/}" 2>/dev/null)" || return 1
    [[ "$(realpath -e -- "${dir}" 2>/dev/null)" == "${rr%/}${dir#"${root}"}" ]] || return 1
    v="$(cat "${dir}/VERSION" 2>/dev/null)" || return 1
    [[ -n "${v}" ]] || return 1
    echo "${v} ${dir}"
}

# kata_shim_of ID PROC: the path of the containerd shim that runs the container ID, read from PROC
# ("/proc" on the machine): the process whose executable is a containerd shim and whose command line
# names the ID. Docker's info does not say which shim a runtime starts (BUG-002); this does.
kata_shim_of() {
    local id="$1" proc="$2" d p
    [[ "${id}" =~ ^[0-9a-f]{12,64}$ ]] || return 1
    for d in "${proc}"/[0-9]*; do
        p="$(readlink "${d}/exe" 2>/dev/null)" || continue
        [[ "$(basename "${p}")" == containerd-shim-* ]] || continue
        { tr '\0' ' ' < "${d}/cmdline"; } 2>/dev/null | grep -F -- "${id}" >/dev/null || continue
        echo "${p}"
        return 0
    done
    return 1
}

# kata_older A B: whether version A is older than B.
kata_older() {
    [[ "$1" != "$2" && "$(printf '%s\n%s\n' "$1" "$2" | sort -V | head -1)" == "$1" ]]
}

# kata_proof_check HOST_KERNEL OUTPUT: the proof of a new Kata before Docker's runtime mvm names it. OUTPUT
# is what a microVM of the small size printed: its kernel, its CPUs, and its MemTotal in kB. It must have
# Kata's own kernel, 2 virtual CPUs and about 2 GB, which shows Crew's drop-in is read. Prints ok, or the
# reason and 1.
kata_proof_check() {
    local host="$1" kernel cpus mem
    { read -r kernel; read -r cpus; read -r mem; } <<< "$2"
    if [[ -z "${kernel}" || "${kernel}" == "${host}" ]]; then echo "the microVM has the machine's own kernel, or none: ${kernel:-nothing}"; return 1; fi
    if [[ "${cpus}" != 2 ]]; then echo "the microVM of the small size has ${cpus:-no} virtual CPUs, not 2"; return 1; fi
    if [[ ! "${mem}" =~ ^[0-9]+$ ]] || (( mem < 1500000 || mem > 2200000 )); then
        echo "the microVM of the small size has ${mem:-no} kB of memory, not about 2 GB"; return 1
    fi
    echo ok
}

# kata_old_dirs ROOT KEEP DAEMON_JSON OURS: the folders the host setup may remove once nothing runs from
# them. OURS is the list install.json keeps of the folders Crew unpacked or installed, one per line;
# nothing else is ever a candidate, whatever its name (security review of 507b11c). Not KEEP, not a
# symlink, not a name of another shape, and not one any runtime in DAEMON_JSON still names, by its
# shim, its ConfigPath, or 3.31's io.containerd.mvm.v2 for /opt/kata. A folder renamed for removal by a
# run that was stopped (FOLDER.removing) comes too.
kata_old_dirs() {
    local root="$1" keep="$2" daemon="$3" ours="$4" d
    while IFS= read -r d; do
        [[ -n "${d}" && "${d}" != "${keep}" && -d "${d}" && ! -L "${d}" ]] || continue
        kata_folder "${root}" "${d}" || continue
        kata_named "${daemon}" "${d}" "${root}" && continue
        echo "${d}"
    done <<< "${ours}"
    while IFS= read -r d; do
        d="${d%.removing}"
        [[ -n "${d}" && "${d}" != "${keep}" ]] || continue
        kata_folder "${root}" "${d}" && grep -qxF -- "${d}" <<< "${ours}" && echo "${d}.removing"
    done < <(compgen -G "${root}/opt/kata*.removing" || true)
    return 0
}

# kata_named DAEMON_JSON DIR ROOT: whether a runtime in DAEMON_JSON still names DIR.
kata_named() {
    python3 -I -c 'import json, sys
path, d, root = sys.argv[1:4]
import os
# A file that cannot be read, or a runtime of another shape, says "named": nothing is removed on a doubt
# (security review of 0001b6a). Paths are compared resolved, so /opt//kata-x or a link to it count too.
try:
    rts = json.load(open(path)).get("runtimes", {})
    rd = os.path.realpath(d)
    for r in rts.values():
        opts = r.get("options") or {}
        for v in (r.get("runtimeType", ""), r.get("path", ""), opts.get("ConfigPath", "")):
            if not isinstance(v, str):
                sys.exit(0)
            if v and os.path.isabs(v):
                rv = os.path.realpath(v)
                if rv == rd or rv.startswith(rd + "/") or v == d or v.startswith(d + "/"):
                    sys.exit(0)
        if r.get("runtimeType") == "io.containerd.mvm.v2" and d == root + "/opt/kata":
            sys.exit(0)
except FileNotFoundError:
    sys.exit(1)
except Exception:
    sys.exit(0)
sys.exit(1)' "$1" "$2" "$3"
}

# kata_busy DIR PROC: the first "PID PATH" of a process that runs or maps a file under DIR (a shim, Cloud
# Hypervisor, virtiofsd, a library of them), read from PROC ("/proc" on the machine), and 0; 1 when none.
kata_busy() {
    local dir="$1" proc="$2" exe p
    for exe in "${proc}"/[0-9]*/exe; do
        p="$(readlink "${exe}" 2>/dev/null)" || continue
        if [[ "${p}" == "${dir}/"* ]]; then
            echo "$(basename "$(dirname "${exe}")") ${p}"
            return 0
        fi
    done
    p="$(grep -lF -- " ${dir}/" "${proc}"/[0-9]*/maps 2>/dev/null | head -1)"
    if [[ -n "${p}" ]]; then
        echo "$(basename "$(dirname "${p}")") a file under ${dir} mapped"
        return 0
    fi
    return 1
}

# kata_remove DIR PROC: removes a folder nothing runs from. It is renamed aside first, so nothing can
# start from it while it is checked again and removed, and renamed back if something did start from it
# in between (security review of 507b11c). Prints `removed`, or `busy PID PATH` and returns 1.
kata_remove() {
    local dir="$1" proc="$2" aside b
    if b="$(kata_busy "${dir}" "${proc}")"; then echo "busy ${b}"; return 1; fi
    aside="${dir}.removing"
    if [[ "${dir}" != *.removing ]]; then
        [[ -e "${aside}" ]] && rm -rf -- "${aside}"
        mv -T -- "${dir}" "${aside}" || { echo "failed: renaming ${dir}"; return 1; }
    else
        aside="${dir}"
    fi
    if b="$(kata_busy "${aside}" "${proc}")"; then
        [[ "${aside}" != "${dir}" ]] && mv -T -- "${aside}" "${dir}"
        echo "busy ${b}"
        return 1
    fi
    rm -rf -- "${aside}" || { echo "failed: removing ${aside}"; return 1; }
    echo removed
}
