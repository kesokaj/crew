# The machine of the install test (design 16.11): an ordinary container that holds QEMU and is given
# /dev/kvm. It is never shipped. Base: the server's base image, pinned in versions.lock.
ARG SERVER_BASE_IMAGE
FROM ${SERVER_BASE_IMAGE}
RUN apt-get update \
    && apt-get install --no-install-recommends -y qemu-system-x86 qemu-utils cloud-image-utils \
       openssh-client curl ca-certificates openssl apache2-utils \
    && rm -rf /var/lib/apt/lists/*
COPY vm.sh /usr/local/bin/install-test-vm
ENTRYPOINT ["/usr/local/bin/install-test-vm"]
