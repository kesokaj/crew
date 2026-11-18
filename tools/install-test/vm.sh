#!/usr/bin/env bash
# Runs inside the install test's container: boots a fresh Debian or Ubuntu cloud image with KVM,
# copies Crew's checkout into it as a git bundle, and runs tools/install-test/inside.sh there as
# root. Everything it writes goes to /out. The virtual machine reaches the internet through QEMU's
# own user network, so the container needs no privilege but /dev/kvm.
#   install-test-vm DISTRO PHASE      DISTRO: debian | ubuntu   PHASE: cycle | kata4 | halfway | e2e | restart | second
set -euo pipefail
DISTRO="$1" PHASE="$2"
case "${DISTRO}" in
    debian) URL="${INSTALL_TEST_DEBIAN_URL}" SUM="${INSTALL_TEST_DEBIAN_SHA512}" TOOL=sha512sum ;;
    ubuntu) URL="${INSTALL_TEST_UBUNTU_URL}" SUM="${INSTALL_TEST_UBUNTU_SHA256}" TOOL=sha256sum ;;
    *) echo "install-test: debian or ubuntu" >&2; exit 2 ;;
esac
log() { echo "[install-test $(date -u +%T)] $*"; }

base="/cache/$(basename "${URL}")"
if [ ! -s "${base}" ] || ! echo "${SUM}  ${base}" | ${TOOL} -c --quiet - 2>/dev/null; then
    log "downloading ${URL}"
    curl -fsSL -o "${base}.part" "${URL}"
    echo "${SUM}  ${base}.part" | ${TOOL} -c --quiet -
    mv "${base}.part" "${base}"
fi

work=/work; mkdir -p "${work}"
# A thin disk on top of the image: doctor asks for 200 GB free, also after the images are built and
# after the release folders of four versions (about 5 GB each), and only what is written is used.
qemu-img create -q -f qcow2 -b "${base}" -F qcow2 "${work}/disk.qcow2" 320G
ssh-keygen -q -t ed25519 -N '' -f "${work}/key"
cat > "${work}/user-data" <<UD
#cloud-config
users:
  - name: tester
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys: [$(cat "${work}/key.pub")]
growpart: {mode: auto, devices: ['/']}
UD
echo "instance-id: crew-install-test" > "${work}/meta-data"
cloud-localds "${work}/seed.img" "${work}/user-data" "${work}/meta-data"

# 8 virtual CPUs and 32 GiB: the least doctor accepts (16.11).
qemu-system-x86_64 -enable-kvm -cpu host -smp 8 -m 32768 -nographic \
    -drive "file=${work}/disk.qcow2,if=virtio,cache=unsafe" \
    -drive "file=${work}/seed.img,if=virtio,format=raw" \
    -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-pci,netdev=n0 \
    -serial "file:/out/${DISTRO}-${PHASE}-console.log" -monitor none -display none &
qemu=$!
trap 'kill ${qemu} 2>/dev/null || true' EXIT

SSH=(ssh -q -i "${work}/key" -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o ServerAliveInterval=30 tester@127.0.0.1)
log "waiting for the virtual machine"
for _ in $(seq 1 120); do "${SSH[@]}" true 2>/dev/null && break; sleep 5; done
"${SSH[@]}" true || { log "the virtual machine did not come up"; exit 1; }
"${SSH[@]}" 'cloud-init status --wait >/dev/null 2>&1 || true'
log "up: $("${SSH[@]}" '. /etc/os-release; echo $PRETTY_NAME; uname -r')"

"${SSH[@]}" 'mkdir -p crew-src' < /dev/null
"${SSH[@]}" 'cat > crew.bundle' < /src/crew.bundle
# The phase second's cards, the registry's CA and address, and its made-up login (second.sh): what
# another machine is brought by hand.
if [ "${PHASE}" = second ]; then
    # The tester's own there, 0600 as second.sh wrote it: inside.sh runs under sudo, whose SUDO_UID
    # crew-ctl takes as the token file's owner.
    tar -C /src/second -c cards ca.crt token registry-ip | "${SSH[@]}" 'mkdir -p second && tar -C second -x'
fi
"${SSH[@]}" 'git clone -q crew.bundle crew 2>/dev/null || { sudo DEBIAN_FRONTEND=noninteractive apt-get update -qq && sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq git >/dev/null && git clone -q crew.bundle crew; }'
set +e
"${SSH[@]}" "cd crew && sudo env PHASE=${PHASE} DISTRO=${DISTRO} bash tools/install-test/inside.sh" 2>&1 | tee "/out/${DISTRO}-${PHASE}.log"
rc=${PIPESTATUS[0]}
# inside.sh exits 101 when the machine restarts by itself (16.10): wait for it to go, then on as 100.
if [ "${rc}" = 101 ]; then
    log "waiting for the machine to restart by itself"
    for _ in $(seq 1 60); do "${SSH[@]}" true 2>/dev/null || break; sleep 5; done
    if "${SSH[@]}" true 2>/dev/null; then
        log "the machine did not restart within 5 minutes"
        "${SSH[@]}" 'sudo journalctl -u crew-restart.service --no-pager | tail -20' 2>&1 | tee -a "/out/${DISTRO}-${PHASE}.log"
    else
        sleep 15
        for _ in $(seq 1 60); do "${SSH[@]}" true 2>/dev/null && break; sleep 5; done
        log "up again: $("${SSH[@]}" 'uptime -p')"
        "${SSH[@]}" "cd crew && sudo env PHASE=${PHASE}-after-reboot DISTRO=${DISTRO} bash tools/install-test/inside.sh" 2>&1 | tee -a "/out/${DISTRO}-${PHASE}.log"
        rc=${PIPESTATUS[0]}
    fi
fi
# inside.sh exits 100 to ask for a restart of the machine, and is run again after it.
if [ "${rc}" = 100 ]; then
    log "restarting the virtual machine"
    "${SSH[@]}" 'sudo systemctl reboot' 2>/dev/null
    sleep 15
    for _ in $(seq 1 60); do "${SSH[@]}" true 2>/dev/null && break; sleep 5; done
    log "up again: $("${SSH[@]}" 'uptime -p')"
    "${SSH[@]}" "cd crew && sudo env PHASE=${PHASE}-after-reboot DISTRO=${DISTRO} bash tools/install-test/inside.sh" 2>&1 | tee -a "/out/${DISTRO}-${PHASE}.log"
    rc=${PIPESTATUS[0]}
fi
set -e
"${SSH[@]}" 'sudo poweroff' 2>/dev/null || true
wait "${qemu}" 2>/dev/null || true
log "exit ${rc}"
exit "${rc}"
